// Package actionrunner executes a schema's Action against a live site.
// It is the runtime half of the registry: given a schema (loaded from
// the registry or a local file) and the name of an action the user
// wants to invoke, it resolves session state, runs the schema-supplied
// bootstrap and signer JS, fires the HTTP request through a Chrome-TLS
// fingerprinted client, and rotates Set-Cookie values back into the
// session store.
//
// The runtime is deliberately site-agnostic — every piece of x.com /
// tiktok.com / etc. knowledge lives in the schema JSON. Adding a new
// Path-1 site is "push a schema to the registry", never "release a new
// CLI."
package actionrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
	"github.com/hermai-ai/hermai-cli/pkg/actions"
	"github.com/hermai-ai/hermai-cli/pkg/browsercookies"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
	"github.com/hermai-ai/hermai-cli/pkg/signer"
)

// Result is what Run returns on success.
type Result struct {
	Status     int
	Headers    http.Header
	Body       []byte
	SignedReq  *http.Request // for --dry-run consumers
	Bootstraps int           // how many bootstraps we ran (usually 0 or 1)
}

// Request carries everything Run needs.
type Request struct {
	// Schema is the loaded schema for the target site.
	Schema *schema.Schema
	// ActionName is the schema.Action.Name to invoke.
	ActionName string
	// Args are user-supplied key/value pairs for the action's template
	// params (e.g. {text: "hello world"} for CreateDraftTweet).
	Args map[string]string
	// SessionsDir is the parent directory for session state
	// (~/.hermai/sessions on a user machine). Each site gets its own
	// subdirectory containing cookies.json and state.json.
	SessionsDir string
	// HTTPClient is used for the outgoing request. Callers should pass
	// an httpclient.Doer wrapped in http.Client so tls-client's Chrome
	// fingerprint is applied. Required — Run refuses to run without it.
	HTTPClient httpclient.Doer
	// BootstrapClient is used for bootstrap JS fetches. Separate from
	// HTTPClient so the two can have different timeouts / redirect
	// policies. If nil, falls back to HTTPClient wrapped in http.Client.
	BootstrapClient *http.Client
	// DryRun skips the actual network call and returns the fully-signed
	// request for inspection.
	DryRun bool
}

// Run resolves the session, runs bootstrap + signer as the schema
// declares, fires the request, and returns the result.
func Run(ctx context.Context, req Request) (*Result, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}

	action, err := findAction(req.Schema, req.ActionName)
	if err != nil {
		return nil, err
	}
	for _, p := range action.Params {
		if p.Required {
			if _, ok := req.Args[p.Name]; !ok {
				return nil, fmt.Errorf("missing required argument %q for action %q", p.Name, action.Name)
			}
		}
	}

	siteDir := filepath.Join(req.SessionsDir, req.Schema.Domain)

	cookies, err := resolveCookies(ctx, req.Schema.Domain, siteDir)
	if err != nil {
		return nil, fmt.Errorf("resolve cookies: %w", err)
	}

	bootstraps := 0
	var state map[string]string
	if req.Schema.Runtime.NeedsBootstrap() {
		s, ran, err := resolveState(ctx, req.Schema, siteDir, req.BootstrapClient, req.HTTPClient)
		if err != nil {
			return nil, fmt.Errorf("bootstrap state: %w", err)
		}
		state = s
		if ran {
			bootstraps++
		}
	}

	httpReq, err := buildRequest(action, req.Args, cookies)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	if req.Schema.Runtime.NeedsSigner() {
		if err := sign(ctx, req.Schema, httpReq, cookies, state); err != nil {
			return nil, fmt.Errorf("sign: %w", err)
		}
	}

	if req.DryRun {
		return &Result{SignedReq: httpReq, Bootstraps: bootstraps}, nil
	}

	resp, err := req.HTTPClient.Do(httpReq.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// Rotate cookies only on successful responses. X (and other anti-bot
	// sites) deliberately return GUEST Set-Cookie headers on 401/403 —
	// if we blindly save those, the user's real auth_token gets
	// overwritten with a logged-out value and every subsequent call
	// fails forever. Only trust cookie rotation when the response
	// indicates the request actually reached the authenticated surface.
	if resp.StatusCode < 400 && len(resp.Cookies()) > 0 {
		for _, c := range resp.Cookies() {
			cookies[c.Name] = c.Value
		}
		_ = saveCookies(req.Schema.Domain, siteDir, cookies)
	}

	// Auth errors on a schema with bootstrap state are ambiguous: they
	// can mean the cookies went stale, OR the bootstrap-computed state
	// did. We can't tell from the response alone, so we invalidate the
	// state cache. Next call re-bootstraps; if the problem was cookies,
	// the user will get the same 401 and know to re-import. If it was
	// stale state, the re-bootstrap fixes it automatically.
	// Cookies are NOT invalidated here — the user's authenticated
	// browser session is still the source of truth for those.
	if (resp.StatusCode == 401 || resp.StatusCode == 403) && req.Schema.Runtime.NeedsBootstrap() {
		if err := invalidateState(siteDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not invalidate stale state for %s: %v\n", req.Schema.Domain, err)
		} else {
			fmt.Fprintf(os.Stderr, "cleared %s bootstrap state after HTTP %d — next call will re-bootstrap\n",
				req.Schema.Domain, resp.StatusCode)
		}
	}

	return &Result{
		Status:     resp.StatusCode,
		Headers:    resp.Header,
		Body:       body,
		Bootstraps: bootstraps,
	}, nil
}

func validateRequest(req Request) error {
	if req.Schema == nil {
		return errors.New("actionrunner: Schema is required")
	}
	if req.ActionName == "" {
		return errors.New("actionrunner: ActionName is required")
	}
	if req.SessionsDir == "" {
		return errors.New("actionrunner: SessionsDir is required")
	}
	if req.HTTPClient == nil {
		return errors.New("actionrunner: HTTPClient is required")
	}
	if req.Schema.Runtime == nil {
		// A schema without Runtime is an all-public, no-auth schema —
		// legal, but surprising for the `hermai action` command. Allow
		// it; downstream calls just skip bootstrap + signer.
		req.Schema.Runtime = &schema.Runtime{}
	}
	return nil
}

func findAction(sch *schema.Schema, name string) (schema.Action, error) {
	for _, a := range sch.Actions {
		if a.Name == name {
			return a, nil
		}
	}
	names := make([]string, 0, len(sch.Actions))
	for _, a := range sch.Actions {
		names = append(names, a.Name)
	}
	return schema.Action{}, fmt.Errorf("action %q not in schema — known: %v", name, names)
}

// resolveCookies reads cookies.json; falls back to the user's browser;
// writes through so subsequent calls are fast.
func resolveCookies(ctx context.Context, domain, siteDir string) (map[string]string, error) {
	path := filepath.Join(siteDir, "cookies.json")
	if b, err := os.ReadFile(path); err == nil {
		var cf actions.CookieFile
		if json.Unmarshal(b, &cf) == nil && len(cf.Cookies) > 0 {
			return cf.Cookies, nil
		}
	}
	fmt.Fprintf(os.Stderr, "no cookies on disk for %s — reading from your browser\n", domain)
	src := browsercookies.NewSource()
	got, err := src.GetCookies(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("read browser cookies: %w", err)
	}
	if len(got) == 0 {
		return nil, fmt.Errorf("no cookies for %s — sign in to the site in your browser, then retry", domain)
	}
	m := make(map[string]string, len(got))
	for _, c := range got {
		m[c.Name] = c.Value
	}
	_ = saveCookies(domain, siteDir, m)
	return m, nil
}

// sessionFileMu serializes writes to the per-site session files across
// goroutines in one process. goja.Runtime isn't concurrency-safe but
// that's handled separately by creating a fresh runtime per Sign call;
// here the concern is two goroutines calling Run on the same site
// racing on cookies.json/state.json. Cross-process concurrency (two
// `hermai` processes on the same site) is NOT handled by this mutex;
// users running parallel invocations on the same site should serialize
// at their level. Documented in architecture/session-storage.md.
var sessionFileMu sync.Mutex

// saveCookies atomically persists the cookie map to
// {siteDir}/cookies.json. Atomic rename ensures readers never see a
// partial file if the writer crashes mid-write. Cookie attributes
// (Path, Expires, Secure, HttpOnly) are intentionally NOT preserved —
// we reattach cookies as a single Cookie: name=value; ... header, which
// doesn't carry attributes, so storing them would just bloat the file.
func saveCookies(domain, siteDir string, cookies map[string]string) error {
	sessionFileMu.Lock()
	defer sessionFileMu.Unlock()

	if err := os.MkdirAll(siteDir, 0700); err != nil {
		return err
	}
	cf := actions.CookieFile{
		Site:    domain,
		SavedAt: time.Now().UTC(),
		Domain:  domain, // keep in sync with pkg/actions.BootstrapSession
		Cookies: cookies,
	}
	b, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(siteDir, "cookies.json"), b)
}

// atomicWrite writes bytes to path via a temp file + rename. A crash
// mid-write leaves the temp file behind (next call cleans up) but never
// a truncated cookies.json. Permissions are 0600 regardless of umask.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Ensure 0600 regardless of umask.
	if err := os.Chmod(tmpPath, 0600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// StateFile persists bootstrap output.
type StateFile struct {
	Site    string            `json:"site"`
	SavedAt time.Time         `json:"saved_at"`
	TTL     int               `json:"ttl_seconds"`
	State   map[string]string `json:"state"`
}

// resolveState loads cached state or re-runs the bootstrap JS. Returns
// the state and whether bootstrap actually ran this call.
func resolveState(ctx context.Context, sch *schema.Schema, siteDir string, bootstrapClient *http.Client, doer httpclient.Doer) (map[string]string, bool, error) {
	path := filepath.Join(siteDir, "state.json")
	if b, err := os.ReadFile(path); err == nil {
		var sf StateFile
		if json.Unmarshal(b, &sf) == nil && len(sf.State) > 0 {
			ttl := time.Duration(sf.TTL) * time.Second
			if ttl <= 0 {
				ttl = time.Hour
			}
			if time.Since(sf.SavedAt) < ttl {
				return sf.State, false, nil
			}
		}
	}

	// Bootstrap. Use the provided client (or wrap the doer) so fetches
	// look like browser traffic.
	if bootstrapClient == nil {
		bootstrapClient = &http.Client{
			Transport: doerTransport{doer: doer},
			Timeout:   30 * time.Second,
		}
	}
	bs, err := signer.NewJSBootstrap(signer.BootstrapConfig{
		Source:       sch.Runtime.BootstrapJS,
		AllowedHosts: sch.Runtime.AllowedHosts,
		HTTPClient:   bootstrapClient,
	})
	if err != nil {
		return nil, false, err
	}
	state, err := bs.Run(ctx, map[string]any{})
	if err != nil {
		return nil, false, err
	}

	ttl := sch.Runtime.BootstrapTTLSeconds
	if ttl <= 0 {
		ttl = 3600
	}
	if err := saveState(sch.Domain, siteDir, state, ttl); err != nil {
		// State write failure is non-fatal — the signer can still use
		// the just-computed state for this call. Next call re-runs
		// bootstrap, which is fine if slow. Surface the error so
		// operators notice persistent disk issues.
		fmt.Fprintf(os.Stderr, "warning: could not persist state for %s: %v\n", sch.Domain, err)
	}
	return state, true, nil
}

// saveState persists a bootstrap-produced state map to
// {siteDir}/state.json atomically and under the session-file mutex.
// Called from resolveState after a fresh bootstrap; also called by
// invalidateStateOnAuthError with an empty state (effectively deleting
// the cache so the next call triggers a rebootstrap).
func saveState(domain, siteDir string, state map[string]string, ttl int) error {
	sessionFileMu.Lock()
	defer sessionFileMu.Unlock()

	if err := os.MkdirAll(siteDir, 0700); err != nil {
		return err
	}
	sf := StateFile{Site: domain, SavedAt: time.Now().UTC(), TTL: ttl, State: state}
	b, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(siteDir, "state.json"), b)
}

// invalidateState removes the cached state file so the next Run will
// trigger a fresh bootstrap. Called when a 401/403 suggests the cached
// bootstrap output (animation_key, msToken, etc.) has gone stale on
// the server side — X and TikTok can invalidate per-session derived
// state without notification. Also covers the case where the schema's
// bootstrap algorithm changed between CLI versions.
func invalidateState(siteDir string) error {
	sessionFileMu.Lock()
	defer sessionFileMu.Unlock()
	err := os.Remove(filepath.Join(siteDir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

type doerTransport struct {
	doer httpclient.Doer
}

func (t doerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.doer.Do(req)
}

// buildRequest assembles the HTTP request from the schema's Action.
// URL template, method, and static headers come from the schema;
// cookies are attached as a single Cookie header.
func buildRequest(action schema.Action, args, cookies map[string]string) (*http.Request, error) {
	url, err := renderTemplate(action.URLTemplate, args)
	if err != nil {
		return nil, fmt.Errorf("render url template: %w", err)
	}

	method := action.Method
	if method == "" {
		method = "GET"
	}

	var body io.Reader
	// BodyTemplate is the production path. Most schemas ship JSON with
	// {{var}} placeholders, but some authenticated write endpoints still
	// expect application/x-www-form-urlencoded. Render according to the
	// declared Content-Type so the schema can faithfully replay browser
	// requests instead of forcing every action into JSON.
	if action.BodyTemplate != "" {
		contentType := ""
		for k, v := range action.Headers {
			if strings.EqualFold(k, "Content-Type") {
				contentType = strings.ToLower(v)
				break
			}
		}

		var rendered string
		if strings.Contains(contentType, "application/x-www-form-urlencoded") {
			rendered, err = renderFormTemplate(action.BodyTemplate, args)
			if err != nil {
				return nil, fmt.Errorf("render form body template: %w", err)
			}
		} else {
			rendered, err = renderJSONTemplate(action.BodyTemplate, args)
			if err != nil {
				return nil, fmt.Errorf("render JSON body template: %w", err)
			}
		}
		body = strings.NewReader(rendered)
	} else {
		// Fallback for flat APIs without a template: JSON-marshal every
		// Param with in="body" as a top-level object. Useful for toy
		// schemas; real sites almost always need BodyTemplate.
		bodyParams := map[string]any{}
		for _, p := range action.Params {
			if p.In == "body" {
				if v, ok := args[p.Name]; ok {
					bodyParams[p.Name] = v
				}
			}
		}
		if len(bodyParams) > 0 {
			b, err := json.Marshal(bodyParams)
			if err != nil {
				return nil, err
			}
			body = strings.NewReader(string(b))
		}
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	for k, v := range action.Headers {
		rendered, err := renderTemplate(v, args)
		if err != nil {
			return nil, fmt.Errorf("render header %q: %w", k, err)
		}
		req.Header.Set(k, rendered)
	}
	if _, ok := action.Headers["User-Agent"]; !ok {
		req.Header.Set("User-Agent", httpclient.BrowserUserAgent)
	}
	if len(cookies) > 0 {
		var parts []string
		for name, val := range cookies {
			parts = append(parts, name+"="+val)
		}
		req.Header.Set("Cookie", strings.Join(parts, "; "))
	}
	return req, nil
}

// renderTemplate does minimal {{var}} substitution for URL + header
// templates. Values are substituted raw; callers are expected to
// pass already-escaped input for URL components. Unknown vars expand
// to empty strings — the required-args check upstream catches real
// gaps before the request goes anywhere.
func renderTemplate(tpl string, args map[string]string) (string, error) {
	out := tpl
	for {
		i := strings.Index(out, "{{")
		if i < 0 {
			return out, nil
		}
		j := strings.Index(out[i:], "}}")
		if j < 0 {
			return "", fmt.Errorf("unclosed {{ in template")
		}
		name := strings.TrimSpace(out[i+2 : i+j])
		out = out[:i] + args[name] + out[i+j+2:]
	}
}

// renderJSONTemplate substitutes {{var}} and {{var|filter}} in a JSON
// body template, JSON-escaping each value so it can't break out of the
// surrounding string. Default (no filter): json-encode, strip outer
// quotes, insert. Templates write "{{text}}" (with quotes) and we insert
// the inner bytes, so authoring is natural:
//
//	{"tweet_text":"{{text}}"}
//
// with text="hello" becomes {"tweet_text":"hello"}, and text=`hi"bye`
// becomes {"tweet_text":"hi\"bye"} — the caller's string never escapes
// its quotes.
//
// Supported filters:
//
//   - |json        full JSON encoding, quotes included. Use when the
//     placeholder sits OUTSIDE a string context:
//     {"count": {{count|json}}}   with count="3" → {"count": "3"}
//     (caller is responsible for numeric vs string intent).
//
//   - |json_array  comma-split the value, JSON-encode each piece as a
//     string, join with commas. Use when the placeholder
//     sits inside a JSON array literal:
//     [{{product_ids|json_array}}]
//     --arg product_ids=119586,99811
//     → ["119586","99811"]
//     Values containing commas must use a different filter;
//     this one is for simple id/tag lists.
//
// Unknown filters produce a typed error — catches typos like
// {{text|string}} at render time rather than silently emitting the raw
// unfiltered value.
func renderJSONTemplate(tpl string, args map[string]string) (string, error) {
	out := tpl
	for {
		i := strings.Index(out, "{{")
		if i < 0 {
			return out, nil
		}
		j := strings.Index(out[i:], "}}")
		if j < 0 {
			return "", fmt.Errorf("unclosed {{ in template")
		}
		name, filter := parseTemplateVar(out[i+2 : i+j])
		val := args[name]
		rendered, err := applyJSONFilter(val, filter, name)
		if err != nil {
			return "", err
		}
		out = out[:i] + rendered + out[i+j+2:]
	}
}

// renderFormTemplate substitutes {{var}} in a
// application/x-www-form-urlencoded template, URL-encoding each value.
// Unknown vars expand to empty string, mirroring renderTemplate.
func renderFormTemplate(tpl string, args map[string]string) (string, error) {
	out := tpl
	for {
		i := strings.Index(out, "{{")
		if i < 0 {
			return out, nil
		}
		j := strings.Index(out[i:], "}}")
		if j < 0 {
			return "", fmt.Errorf("unclosed {{ in template")
		}
		name := strings.TrimSpace(out[i+2 : i+j])
		out = out[:i] + url.QueryEscape(args[name]) + out[i+j+2:]
	}
}

// parseTemplateVar splits a placeholder like "name|filter" into its
// components. No filter → empty filter string.
func parseTemplateVar(raw string) (name, filter string) {
	if pipe := strings.Index(raw, "|"); pipe >= 0 {
		return strings.TrimSpace(raw[:pipe]), strings.TrimSpace(raw[pipe+1:])
	}
	return strings.TrimSpace(raw), ""
}

// applyJSONFilter renders a single value for renderJSONTemplate, honoring
// the optional pipe filter. See renderJSONTemplate doc for filter list.
func applyJSONFilter(val, filter, nameForErr string) (string, error) {
	switch filter {
	case "":
		encoded, err := json.Marshal(val)
		if err != nil {
			return "", fmt.Errorf("json-escape %q: %w", nameForErr, err)
		}
		s := string(encoded)
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			s = s[1 : len(s)-1]
		}
		return s, nil
	case "json":
		encoded, err := json.Marshal(val)
		if err != nil {
			return "", fmt.Errorf("json-encode %q: %w", nameForErr, err)
		}
		return string(encoded), nil
	case "json_array":
		// Empty value → empty array body (the caller's brackets in the
		// template make it `[]`).
		if strings.TrimSpace(val) == "" {
			return "", nil
		}
		parts := strings.Split(val, ",")
		encoded := make([]string, len(parts))
		for idx, p := range parts {
			b, err := json.Marshal(strings.TrimSpace(p))
			if err != nil {
				return "", fmt.Errorf("json_array: marshal %q[%d]: %w", nameForErr, idx, err)
			}
			encoded[idx] = string(b)
		}
		return strings.Join(encoded, ","), nil
	default:
		return "", fmt.Errorf("unknown template filter %q on {{%s|%s}} — supported: json, json_array",
			filter, nameForErr, filter)
	}
}

// sign runs the schema's signer JS and merges its output headers into
// req. The signer sees the fully-constructed request body, headers, and
// cookies — everything that goes into the txid computation must be set
// before we call it.
func sign(ctx context.Context, sch *schema.Schema, req *http.Request, cookies, state map[string]string) error {
	s, err := signer.CachedJSSigner(sch.Runtime.SignerJS)
	if err != nil {
		return err
	}
	bodyStr, err := consumeBody(req)
	if err != nil {
		return err
	}
	headers := make(map[string]string, len(req.Header))
	for k, v := range req.Header {
		headers[k] = strings.Join(v, ", ")
	}
	out, err := s.Sign(ctx, signer.Input{
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: headers,
		Body:    bodyStr,
		Cookies: cookies,
		State:   state,
		NowMS:   time.Now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	for k, v := range out.Headers {
		req.Header.Set(k, v)
	}
	// Signers may return an augmented URL — TikTok's signer appends
	// X-Bogus / _signature as query params, Xiaohongshu appends X-s/X-t.
	// If the signer returned a URL that differs from the input, swap it
	// in. Empty out.URL means "no change."
	if out.URL != "" && out.URL != req.URL.String() {
		parsed, err := url.Parse(out.URL)
		if err != nil {
			return fmt.Errorf("signer returned unparseable URL %q: %w", out.URL, err)
		}
		req.URL = parsed
		req.Host = parsed.Host
	}
	return nil
}

// consumeBody reads req.Body and rewinds it so the subsequent Do() can
// re-read. Signers need to see the body for path+body-dependent hashes.
func consumeBody(req *http.Request) (string, error) {
	if req.Body == nil {
		return "", nil
	}
	b, err := io.ReadAll(req.Body)
	if err != nil {
		return "", err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(strings.NewReader(string(b)))
	req.ContentLength = int64(len(b))
	return string(b), nil
}
