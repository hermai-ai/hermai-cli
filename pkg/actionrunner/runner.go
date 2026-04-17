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

func saveCookies(domain, siteDir string, cookies map[string]string) error {
	if err := os.MkdirAll(siteDir, 0700); err != nil {
		return err
	}
	cf := actions.CookieFile{Site: domain, SavedAt: time.Now().UTC(), Cookies: cookies}
	b, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(siteDir, "cookies.json"), b, 0600)
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
	if err := os.MkdirAll(siteDir, 0700); err == nil {
		sf := StateFile{Site: sch.Domain, SavedAt: time.Now().UTC(), TTL: ttl, State: state}
		if b, err := json.MarshalIndent(sf, "", "  "); err == nil {
			_ = os.WriteFile(path, b, 0600)
		}
	}
	return state, true, nil
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
	// BodyTemplate is the production path — the schema ships a verbatim
	// JSON template with {{var}} placeholders. We substitute user args
	// with JSON-escaped values so they can't break out of their
	// surrounding string.
	if action.BodyTemplate != "" {
		rendered, err := renderJSONTemplate(action.BodyTemplate, args)
		if err != nil {
			return nil, fmt.Errorf("render body template: %w", err)
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

// renderJSONTemplate substitutes {{var}} in a JSON body template,
// JSON-escaping each value so it can't break out of the surrounding
// string. Crucially, we strip the leading+trailing double quotes the
// JSON encoder produces — templates write "{{text}}" (with quotes) and
// we insert the inner bytes. This keeps schema authoring natural:
//
//	{"tweet_text":"{{text}}"}
//
// with text="hello" becomes {"tweet_text":"hello"}, and text=`hi"bye`
// becomes {"tweet_text":"hi\"bye"} — the caller's string never escapes
// its quotes.
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
		name := strings.TrimSpace(out[i+2 : i+j])
		val := args[name]
		encoded, err := json.Marshal(val)
		if err != nil {
			return "", fmt.Errorf("json-escape %q: %w", name, err)
		}
		// json.Marshal(string) yields "quoted" — strip the surrounding
		// quotes since the template is expected to provide them.
		inner := string(encoded)
		if len(inner) >= 2 && inner[0] == '"' && inner[len(inner)-1] == '"' {
			inner = inner[1 : len(inner)-1]
		}
		out = out[:i] + inner + out[i+j+2:]
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
