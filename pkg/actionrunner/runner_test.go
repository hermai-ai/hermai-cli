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
