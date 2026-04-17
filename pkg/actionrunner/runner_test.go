package actionrunner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/actions"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// --- helpers ----------------------------------------------------------------

// writeCookies writes a CookieFile to <dir>/cookies.json so tests can
// skip the browser-read fallback path.
func writeCookies(t *testing.T, dir string, cookies map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cf := actions.CookieFile{Site: filepath.Base(dir), SavedAt: time.Now().UTC(), Cookies: cookies}
	b, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cookies.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

// writeState writes a StateFile so tests can skip the bootstrap path.
func writeState(t *testing.T, dir string, state map[string]string, savedAt time.Time, ttlSeconds int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	sf := StateFile{Site: filepath.Base(dir), SavedAt: savedAt, TTL: ttlSeconds, State: state}
	b, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

// --- tests ------------------------------------------------------------------

func TestRunner_SimpleAction_NoRuntime(t *testing.T) {
	var seenMethod, seenURL, seenCookie, seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		seenURL = r.URL.Path + "?" + r.URL.RawQuery
		seenCookie = r.Header.Get("Cookie")
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeCookies(t, filepath.Join(dir, "example.com"), map[string]string{"session": "abc"})

	sch := &schema.Schema{
		Domain: "example.com",
		Actions: []schema.Action{
			{
				Name:        "Echo",
				Method:      "POST",
				URLTemplate: srv.URL + "/echo?x={{x}}",
				Headers:     map[string]string{"X-Custom": "v1"},
				Params: []schema.ActionParam{
					{Name: "x", In: "query", Required: true},
					{Name: "msg", In: "body", Required: true},
				},
			},
		},
	}
	r, err := Run(context.Background(), Request{
		Schema:      sch,
		ActionName:  "Echo",
		Args:        map[string]string{"x": "42", "msg": "hi"},
		SessionsDir: dir,
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Status != 200 {
		t.Errorf("status = %d, want 200", r.Status)
	}
	if seenMethod != "POST" {
		t.Errorf("method = %q, want POST", seenMethod)
	}
	if !strings.Contains(seenURL, "x=42") {
		t.Errorf("url should contain x=42, got %q", seenURL)
	}
	if !strings.Contains(seenCookie, "session=abc") {
		t.Errorf("cookie should contain session=abc, got %q", seenCookie)
	}
	if !strings.Contains(seenBody, `"msg":"hi"`) {
		t.Errorf("body should contain msg=hi, got %q", seenBody)
	}
	if r.Bootstraps != 0 {
		t.Errorf("bootstraps = %d, want 0 (no runtime)", r.Bootstraps)
	}
}

func TestRunner_SignerAugmentedURL(t *testing.T) {
	// A signer that returns {url: augmented, headers: {}} must have its
	// URL written back to the outgoing request — otherwise sites that
	// sign via query parameters (TikTok's X-Bogus, Xiaohongshu's X-s/X-t)
	// silently fail. The signer appends a query param; we assert the
	// server sees it.
	var seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeCookies(t, filepath.Join(dir, "example.com"), map[string]string{"session": "abc"})

	sch := &schema.Schema{
		Domain: "example.com",
		Runtime: &schema.Runtime{
			// Return input.url + "&X-Bogus=bogus-value". No headers touched.
			SignerJS: `
				function sign(input) {
					var sep = input.url.indexOf("?") >= 0 ? "&" : "?";
					return { url: input.url + sep + "X-Bogus=bogus-value", headers: {} };
				}
			`,
		},
		Actions: []schema.Action{
			{Name: "Ping", Method: "GET", URLTemplate: srv.URL + "/ping"},
		},
	}
	_, err := Run(context.Background(), Request{
		Schema: sch, ActionName: "Ping", SessionsDir: dir, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(seenQuery, "X-Bogus=bogus-value") {
		t.Errorf("server did not see X-Bogus query param; saw: %q", seenQuery)
	}
}

func TestRunner_SignerOnly(t *testing.T) {
	var seenTxID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTxID = r.Header.Get("X-Signed")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeCookies(t, filepath.Join(dir, "example.com"), map[string]string{"session": "abc"})

	sch := &schema.Schema{
		Domain: "example.com",
		Runtime: &schema.Runtime{
			SignerJS: `
				function sign(input) {
					return { url: input.url, headers: {
						"X-Signed": hermai.sha256(input.method + "|" + input.url).substring(0, 16)
					}};
				}
			`,
		},
		Actions: []schema.Action{
			{Name: "Ping", Method: "GET", URLTemplate: srv.URL + "/ping"},
		},
	}
	_, err := Run(context.Background(), Request{
		Schema:      sch,
		ActionName:  "Ping",
		SessionsDir: dir,
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seenTxID) != 16 {
		t.Errorf("X-Signed header len = %d, want 16 (signer didn't run)", len(seenTxID))
	}
}

func TestRunner_BootstrapRunsWhenStateMissing(t *testing.T) {
	// Mock the site's homepage so the bootstrap JS has something to
	// fetch. The SSRF guard blocks loopback — override via the test
	// flag on the underlying JSBootstrap. Since JSBootstrap is created
	// inside runner, we can't swap the resolver directly; instead we
	// test the decision logic by checking the Bootstraps counter.

	// A simpler approach: use a trivial bootstrap that returns static
	// state without calling hermai.fetch. This exercises the "state
	// missing → bootstrap ran → state written" path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-State-Key") != "my-static-state" {
			t.Errorf("signer didn't see state — header was %q", r.Header.Get("X-State-Key"))
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeCookies(t, filepath.Join(dir, "example.com"), map[string]string{"session": "abc"})

	sch := &schema.Schema{
		Domain: "example.com",
		Runtime: &schema.Runtime{
			BootstrapJS: `function bootstrap(input) { return { my_key: "my-static-state" }; }`,
			SignerJS: `
				function sign(input) {
					return { url: input.url, headers: {
						"X-State-Key": input.state.my_key
					}};
				}
			`,
		},
		Actions: []schema.Action{
			{Name: "Ping", Method: "GET", URLTemplate: srv.URL + "/ping"},
		},
	}
	r, err := Run(context.Background(), Request{
		Schema:      sch,
		ActionName:  "Ping",
		SessionsDir: dir,
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Bootstraps != 1 {
		t.Errorf("bootstraps = %d, want 1", r.Bootstraps)
	}
	// State must have been written to disk.
	if _, err := os.Stat(filepath.Join(dir, "example.com", "state.json")); err != nil {
		t.Errorf("state.json was not written: %v", err)
	}
}

func TestRunner_BootstrapSkippedWhenStateFresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-State-Key") != "cached-value" {
			t.Errorf("signer didn't read cached state; header=%q", r.Header.Get("X-State-Key"))
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	siteDir := filepath.Join(dir, "example.com")
	writeCookies(t, siteDir, map[string]string{"session": "abc"})
	writeState(t, siteDir, map[string]string{"my_key": "cached-value"}, time.Now().UTC(), 3600)

	bootstrapCalls := 0
	sch := &schema.Schema{
		Domain: "example.com",
		Runtime: &schema.Runtime{
			// If bootstrap runs, it'd overwrite state with "live-bootstrap".
			// Assertion below catches that.
			BootstrapJS: `function bootstrap(input) { return { my_key: "live-bootstrap" }; }`,
			SignerJS: `
				function sign(input) {
					return { url: input.url, headers: { "X-State-Key": input.state.my_key }};
				}
			`,
			BootstrapTTLSeconds: 3600,
		},
		Actions: []schema.Action{
			{Name: "Ping", Method: "GET", URLTemplate: srv.URL + "/ping"},
		},
	}
	r, err := Run(context.Background(), Request{
		Schema: sch, ActionName: "Ping", SessionsDir: dir, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Bootstraps != 0 {
		t.Errorf("bootstraps = %d, want 0 (state was fresh)", r.Bootstraps)
	}
	_ = bootstrapCalls
}

func TestRunner_BootstrapRerunsWhenStateStale(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	siteDir := filepath.Join(dir, "example.com")
	writeCookies(t, siteDir, map[string]string{"session": "abc"})
	// Write state that's been aged past the TTL.
	writeState(t, siteDir, map[string]string{"my_key": "old"}, time.Now().Add(-2*time.Hour), 3600)

	sch := &schema.Schema{
		Domain: "example.com",
		Runtime: &schema.Runtime{
			BootstrapJS:         `function bootstrap(input) { return { my_key: "fresh" }; }`,
			BootstrapTTLSeconds: 3600,
		},
		Actions: []schema.Action{{Name: "Ping", Method: "GET", URLTemplate: srv.URL + "/ping"}},
	}
	r, err := Run(context.Background(), Request{
		Schema: sch, ActionName: "Ping", SessionsDir: dir, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Bootstraps != 1 {
		t.Errorf("bootstraps = %d, want 1 (state was stale)", r.Bootstraps)
	}
	// Verify the new state landed on disk.
	b, err := os.ReadFile(filepath.Join(siteDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "fresh") {
		t.Errorf("state.json does not reflect re-bootstrap: %s", string(b))
	}
}

func TestRunner_CookieRotation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "rotated-value", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "new_cookie", Value: "added", Path: "/"})
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	siteDir := filepath.Join(dir, "example.com")
	writeCookies(t, siteDir, map[string]string{"session": "original-value"})

	sch := &schema.Schema{
		Domain: "example.com",
		Actions: []schema.Action{{Name: "Rotate", Method: "GET", URLTemplate: srv.URL + "/x"}},
	}
	_, err := Run(context.Background(), Request{
		Schema: sch, ActionName: "Rotate", SessionsDir: dir, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(siteDir, "cookies.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cf actions.CookieFile
	if err := json.Unmarshal(b, &cf); err != nil {
		t.Fatal(err)
	}
	if cf.Cookies["session"] != "rotated-value" {
		t.Errorf("session cookie not rotated: got %q", cf.Cookies["session"])
	}
	if cf.Cookies["new_cookie"] != "added" {
		t.Errorf("new_cookie not captured: got %q", cf.Cookies["new_cookie"])
	}
}

func TestRunner_DryRunDoesNotHitNetwork(t *testing.T) {
	// Fail the test if the network is touched.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("dry-run must not hit the network; got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeCookies(t, filepath.Join(dir, "example.com"), map[string]string{"session": "abc"})

	sch := &schema.Schema{
		Domain: "example.com",
		Actions: []schema.Action{
			{Name: "Echo", Method: "POST", URLTemplate: srv.URL + "/x",
				Headers: map[string]string{"Authorization": "Bearer static"}},
		},
	}
	r, err := Run(context.Background(), Request{
		Schema: sch, ActionName: "Echo", SessionsDir: dir, HTTPClient: srv.Client(), DryRun: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.SignedReq == nil {
		t.Fatal("dry-run returned no SignedReq")
	}
	if r.SignedReq.Method != "POST" {
		t.Errorf("method = %q, want POST", r.SignedReq.Method)
	}
	if r.SignedReq.Header.Get("Cookie") == "" {
		t.Error("cookie header missing on signed request")
	}
}

func TestRunner_MissingRequiredArgFails(t *testing.T) {
	dir := t.TempDir()
	writeCookies(t, filepath.Join(dir, "example.com"), map[string]string{"session": "abc"})
	sch := &schema.Schema{
		Domain: "example.com",
		Actions: []schema.Action{
			{Name: "Echo", Method: "GET", URLTemplate: "https://example.com/?q={{q}}",
				Params: []schema.ActionParam{{Name: "q", In: "query", Required: true}}},
		},
	}
	_, err := Run(context.Background(), Request{
		Schema: sch, ActionName: "Echo", SessionsDir: dir, HTTPClient: http.DefaultClient,
	})
	if err == nil {
		t.Fatal("expected error for missing required arg")
	}
	if !strings.Contains(err.Error(), "q") {
		t.Errorf("error should name the missing arg, got: %v", err)
	}
}

func TestRunner_UnknownActionFails(t *testing.T) {
	dir := t.TempDir()
	writeCookies(t, filepath.Join(dir, "example.com"), map[string]string{"session": "abc"})
	sch := &schema.Schema{
		Domain: "example.com",
		Actions: []schema.Action{
			{Name: "RealAction", Method: "GET", URLTemplate: "https://example.com/"},
		},
	}
	_, err := Run(context.Background(), Request{
		Schema: sch, ActionName: "GhostAction", SessionsDir: dir, HTTPClient: http.DefaultClient,
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "RealAction") {
		t.Errorf("error should list known actions, got: %v", err)
	}
}

func TestRunner_CookieFileCarriesDomain(t *testing.T) {
	// saveCookies should populate CookieFile.Domain — a consistency
	// fix so the on-disk shape matches what pkg/actions.BootstrapSession
	// writes for the same file.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "new-value", Path: "/"})
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	siteDir := filepath.Join(dir, "example.com")
	writeCookies(t, siteDir, map[string]string{"session": "original"})

	sch := &schema.Schema{
		Domain: "example.com",
		Actions: []schema.Action{{Name: "Rotate", Method: "GET", URLTemplate: srv.URL + "/x"}},
	}
	_, err := Run(context.Background(), Request{
		Schema: sch, ActionName: "Rotate", SessionsDir: dir, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(siteDir, "cookies.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cf actions.CookieFile
	if err := json.Unmarshal(b, &cf); err != nil {
		t.Fatal(err)
	}
	if cf.Domain != "example.com" {
		t.Errorf("CookieFile.Domain = %q, want example.com", cf.Domain)
	}
	if cf.Site != "example.com" {
		t.Errorf("CookieFile.Site = %q, want example.com", cf.Site)
	}
}

func TestRunner_AtomicCookieWrite(t *testing.T) {
	// Simulate a torn write by counting files in the site dir after a
	// save — there should be exactly one cookies.json, no stray .tmp
	// files. Atomic rename guarantees this.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "v", Path: "/"})
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	siteDir := filepath.Join(dir, "example.com")
	writeCookies(t, siteDir, map[string]string{"session": "o"})

	sch := &schema.Schema{
		Domain:  "example.com",
		Actions: []schema.Action{{Name: "X", Method: "GET", URLTemplate: srv.URL + "/"}},
	}
	_, err := Run(context.Background(), Request{
		Schema: sch, ActionName: "X", SessionsDir: dir, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entries, err := os.ReadDir(siteDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after atomic write: %s", e.Name())
		}
	}
}

func TestRunner_AuthErrorInvalidatesState(t *testing.T) {
	// On a 401 response against a schema with bootstrap, state.json
	// should be deleted so the next call re-bootstraps. Cookies are
	// NOT touched (the user's browser session is still authoritative).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"auth failed"}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	siteDir := filepath.Join(dir, "example.com")
	writeCookies(t, siteDir, map[string]string{"session": "good"})
	writeState(t, siteDir, map[string]string{"my_key": "stale-value"}, time.Now().UTC(), 3600)

	sch := &schema.Schema{
		Domain: "example.com",
		Runtime: &schema.Runtime{
			BootstrapJS: `function bootstrap(input) { return { my_key: "fresh" }; }`,
			SignerJS:    `function sign(input) { return { url: input.url, headers: {} }; }`,
		},
		Actions: []schema.Action{{Name: "Ping", Method: "GET", URLTemplate: srv.URL + "/"}},
	}
	result, err := Run(context.Background(), Request{
		Schema: sch, ActionName: "Ping", SessionsDir: dir, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != 401 {
		t.Errorf("status = %d, want 401", result.Status)
	}

	// state.json should be gone.
	if _, err := os.Stat(filepath.Join(siteDir, "state.json")); !os.IsNotExist(err) {
		t.Errorf("state.json should be removed after 401; stat err = %v", err)
	}

	// cookies.json should still exist — auth errors don't destroy the
	// user's session cache.
	if _, err := os.Stat(filepath.Join(siteDir, "cookies.json")); err != nil {
		t.Errorf("cookies.json should still exist after 401; got %v", err)
	}
}

func TestRunner_AuthErrorLeavesStateAloneWithoutBootstrap(t *testing.T) {
	// If the schema has no bootstrap (no state to invalidate), the 401
	// path should not try to touch state.json even if one happens to
	// exist (stale leftover from a previous schema).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()

	dir := t.TempDir()
	siteDir := filepath.Join(dir, "example.com")
	writeCookies(t, siteDir, map[string]string{"session": "good"})
	// No bootstrap, but a state file left from an earlier schema version.
	writeState(t, siteDir, map[string]string{"legacy": "value"}, time.Now().UTC(), 3600)

	sch := &schema.Schema{
		Domain:  "example.com",
		Actions: []schema.Action{{Name: "Ping", Method: "GET", URLTemplate: srv.URL + "/"}},
	}
	_, _ = Run(context.Background(), Request{
		Schema: sch, ActionName: "Ping", SessionsDir: dir, HTTPClient: srv.Client(),
	})
	// state.json should still be there.
	if _, err := os.Stat(filepath.Join(siteDir, "state.json")); err != nil {
		t.Errorf("state.json should be untouched without bootstrap; got err %v", err)
	}
}

func TestRenderTemplate(t *testing.T) {
	cases := []struct {
		tpl  string
		args map[string]string
		want string
	}{
		{"no vars", nil, "no vars"},
		{"hello {{name}}", map[string]string{"name": "world"}, "hello world"},
		{"{{a}}/{{b}}", map[string]string{"a": "x", "b": "y"}, "x/y"},
		{"{{unknown}}", map[string]string{}, ""},
		{"/{{path}}?q={{q}}", map[string]string{"path": "search", "q": "dogs"}, "/search?q=dogs"},
	}
	for _, c := range cases {
		got, err := renderTemplate(c.tpl, c.args)
		if err != nil {
			t.Errorf("renderTemplate(%q): unexpected error: %v", c.tpl, err)
			continue
		}
		if got != c.want {
			t.Errorf("renderTemplate(%q) = %q, want %q", c.tpl, got, c.want)
		}
	}
}

func TestRenderJSONTemplate_NoFilter(t *testing.T) {
	cases := []struct {
		tpl, arg, want string
	}{
		{`"text":"{{t}}"`, "hello", `"text":"hello"`},
		{`"text":"{{t}}"`, `hi "friend"`, `"text":"hi \"friend\""`},
		{`"text":"{{t}}"`, "line1\nline2", `"text":"line1\nline2"`},
	}
	for _, c := range cases {
		got, err := renderJSONTemplate(c.tpl, map[string]string{"t": c.arg})
		if err != nil {
			t.Errorf("arg=%q: %v", c.arg, err)
			continue
		}
		if got != c.want {
			t.Errorf("arg=%q got %q, want %q", c.arg, got, c.want)
		}
	}
}

func TestRenderJSONTemplate_JSONFilter(t *testing.T) {
	// |json keeps the outer quotes — use when the placeholder is NOT
	// already wrapped in quotes in the template.
	got, err := renderJSONTemplate(`{"text": {{t|json}}}`, map[string]string{"t": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"text": "hello"}` {
		t.Errorf("got %q, want {\"text\": \"hello\"}", got)
	}
}

func TestRenderJSONTemplate_JSONArrayFilter(t *testing.T) {
	cases := []struct {
		arg, want string
	}{
		// Typical comma-separated list
		{"119586,99811", `"119586","99811"`},
		// Whitespace around commas is trimmed
		{"a, b, c", `"a","b","c"`},
		// Single value → single-element array
		{"solo", `"solo"`},
		// Values with quotes get JSON-escaped
		{`hi"bye,plain`, `"hi\"bye","plain"`},
		// Empty value → empty output (caller's [] makes it a literal empty array)
		{"", ``},
		{"   ", ``},
	}
	for _, c := range cases {
		got, err := renderJSONTemplate(`[{{ids|json_array}}]`, map[string]string{"ids": c.arg})
		if err != nil {
			t.Errorf("arg=%q: %v", c.arg, err)
			continue
		}
		expected := "[" + c.want + "]"
		if got != expected {
			t.Errorf("arg=%q got %q, want %q", c.arg, got, expected)
		}
	}
}

func TestRenderJSONTemplate_UnknownFilter(t *testing.T) {
	_, err := renderJSONTemplate(`{{x|bogus}}`, map[string]string{"x": "v"})
	if err == nil {
		t.Fatal("expected error on unknown filter")
	}
	if !strings.Contains(err.Error(), "unknown template filter") {
		t.Errorf("error text should mention 'unknown template filter', got: %v", err)
	}
}

func TestRenderJSONTemplate_MultiFilterInOneTemplate(t *testing.T) {
	// Realistic shape: a GraphQL variables body that mixes a scalar
	// string arg with an array of ids.
	tpl := `{"variables":{"query":"{{q}}","ids":[{{ids|json_array}}],"count":{{n|json}}}}`
	got, err := renderJSONTemplate(tpl, map[string]string{
		"q":   "matte lipstick",
		"ids": "119586,99811,40112",
		"n":   "3",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"variables":{"query":"matte lipstick","ids":["119586","99811","40112"],"count":"3"}}`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
