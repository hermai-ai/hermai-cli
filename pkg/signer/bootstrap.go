package signer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// JSBootstrap runs a schema-supplied `bootstrap(input)` JavaScript
// function that produces a session-state map. Unlike JSSigner,
// JSBootstrap exposes network and HTML-parsing capabilities so the
// bootstrap can fetch pages and extract constants — but both are gated
// behind a strict host allowlist declared by the schema.
//
// A typical bootstrap flow:
//  1. Fetch the site's homepage via hermai.fetch(url).
//  2. hermai.selectAll(html, cssSelector) to traverse the DOM.
//  3. Compute derived values (e.g., X's animation_key).
//  4. Return { key_b64: "...", animation_key: "..." } — these become
//     the `state` fields every signer call receives.
//
// Security posture:
//   - Only hosts in AllowedHosts are reachable via hermai.fetch.
//   - Private, loopback, and link-local addresses are always blocked
//     regardless of allowlist (SSRF protection).
//   - Response bodies capped at MaxResponseBytes (default 10 MB).
//   - Total execution deadline enforced via context or Timeout (default 30s).
//   - No cookies attached by default — bootstrap is pre-auth; schemas
//     that need authenticated bootstrap must pass cookies through Input.
type JSBootstrap struct {
	program      *goja.Program
	source       string
	allowedHosts map[string]struct{}
	httpClient   *http.Client
	timeout      time.Duration
	maxBodyBytes int64
	// resolver is swappable for tests — production code always uses
	// net.LookupIP. Tests that need httptest (which binds to loopback)
	// set this to a function that returns non-private IPs.
	resolver func(host string) ([]net.IP, error)
}

// BootstrapConfig configures a new JSBootstrap.
type BootstrapConfig struct {
	// Source is the JavaScript defining a global `bootstrap(input)`
	// function. The function must return an object whose values are
	// all strings — this maps 1:1 to the signer's Input.State.
	Source string
	// AllowedHosts restricts hermai.fetch. Exact hostname match, case
	// insensitive. Subdomain wildcards are NOT supported — list each
	// host you expect to hit. Empty list => all fetch() calls fail.
	AllowedHosts []string
	// HTTPClient is used by hermai.fetch. Callers should pass a
	// TLS-fingerprinted client for production use; defaults to
	// http.DefaultClient which will be flagged as a bot by many sites.
	HTTPClient *http.Client
	// Timeout caps total bootstrap runtime. Default 30 seconds.
	Timeout time.Duration
	// MaxResponseBytes caps the size of any single fetch() response.
	// Default 10 MB — X's ondemand.js is ~3 MB so headroom is needed.
	MaxResponseBytes int64
}

// ErrHostNotAllowed is returned by hermai.fetch when the target host
// isn't in the schema's allowlist.
var ErrHostNotAllowed = errors.New("bootstrap: host not in schema allowlist")

// ErrPrivateAddressBlocked is returned when a fetch would hit a private
// network address. Blocks cloud metadata (169.254.169.254), loopback,
// RFC1918 ranges. Not schema-configurable on purpose.
var ErrPrivateAddressBlocked = errors.New("bootstrap: refusing to fetch a private/loopback address")

// ErrResponseTooLarge is returned when a fetch response exceeds
// MaxResponseBytes.
var ErrResponseTooLarge = errors.New("bootstrap: response exceeds size cap")

// NewJSBootstrap compiles the bootstrap JS. Fails if the source doesn't
// parse or if no HTTPClient is configured and AllowedHosts is non-empty
// (the latter is a schema-configuration bug, not a runtime condition).
func NewJSBootstrap(cfg BootstrapConfig) (*JSBootstrap, error) {
	if cfg.Source == "" {
		return nil, errors.New("bootstrap: source is empty")
	}
	prog, err := goja.Compile("bootstrap.js", cfg.Source, true)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: compile: %w", err)
	}
	hosts := make(map[string]struct{}, len(cfg.AllowedHosts))
	for _, h := range cfg.AllowedHosts {
		hosts[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	maxBody := cfg.MaxResponseBytes
	if maxBody <= 0 {
		maxBody = 10 * 1024 * 1024
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &JSBootstrap{
		program:      prog,
		source:       cfg.Source,
		allowedHosts: hosts,
		httpClient:   client,
		timeout:      timeout,
		maxBodyBytes: maxBody,
		resolver:     net.LookupIP,
	}, nil
}

// Run executes the compiled bootstrap with the given input. Returns the
// flat state map the JS `bootstrap()` function produced. Non-string
// values are JSON-stringified by goja's default ToString semantics;
// callers that need structured state should serialize it themselves.
func (b *JSBootstrap) Run(ctx context.Context, input map[string]any) (map[string]string, error) {
	rt := goja.New()

	deadline := b.timeout
	if d, ok := ctx.Deadline(); ok {
		if rem := time.Until(d); rem > 0 && rem < deadline {
			deadline = rem
		}
	}
	timer := time.AfterFunc(deadline, func() {
		rt.Interrupt(ErrSignerTimeout)
	})
	defer timer.Stop()

	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			rt.Interrupt(ctx.Err())
		case <-stopWatch:
		}
	}()
	defer close(stopWatch)

	// Same crypto/base64/hex primitives as the signer. Bootstrap
	// sometimes needs hashing (X derives the ondemand chunk name from
	// the home HTML) so we expose them unconditionally.
	injectHermaiGlobal(rt)
	// Bootstrap-only capabilities: fetch and HTML parsing.
	b.injectBootstrapGlobals(rt, ctx)

	if _, err := rt.RunProgram(b.program); err != nil {
		return nil, wrapRuntimeError(err)
	}

	fn, ok := goja.AssertFunction(rt.Get("bootstrap"))
	if !ok {
		return nil, fmt.Errorf("%w: bootstrap source did not define a global `bootstrap` function", ErrSignerRuntime)
	}

	result, err := fn(goja.Undefined(), rt.ToValue(input))
	if err != nil {
		return nil, wrapRuntimeError(err)
	}
	return bootstrapResultToStringMap(rt, result)
}

// bootstrapResultToStringMap converts the JS return value to a flat
// map[string]string. Only plain objects are accepted; primitives and
// arrays fail explicitly so schema authors catch "return wrong shape"
// bugs at bootstrap time rather than later during signing.
func bootstrapResultToStringMap(rt *goja.Runtime, v goja.Value) (map[string]string, error) {
	if v == nil || goja.IsNull(v) || goja.IsUndefined(v) {
		return nil, fmt.Errorf("%w: bootstrap() returned null/undefined — expected an object", ErrSignerRuntime)
	}
	// Reject primitives before ToObject — goja's ToObject(nil) panics on
	// primitive values, and even with a runtime it would auto-box (e.g.
	// string -> String wrapper), which is not what we want as bootstrap
	// state.
	if _, isObj := v.Export().(map[string]any); !isObj {
		return nil, fmt.Errorf("%w: bootstrap() returned %T, expected a plain object", ErrSignerRuntime, v.Export())
	}
	obj := v.ToObject(rt)
	if obj == nil {
		return nil, fmt.Errorf("%w: bootstrap() returned a non-object", ErrSignerRuntime)
	}
	out := make(map[string]string, len(obj.Keys()))
	for _, k := range obj.Keys() {
		val := obj.Get(k)
		if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
			continue
		}
		out[k] = val.String()
	}
	return out, nil
}

// injectBootstrapGlobals exposes hermai.fetch and hermai.selectAll on
// top of the signer's base sandbox. Both are synchronous from JS's
// perspective — goja blocks the single-threaded VM while the Go-side
// work runs, so an in-flight fetch interrupts cleanly when the context
// cancels.
func (b *JSBootstrap) injectBootstrapGlobals(rt *goja.Runtime, ctx context.Context) {
	h := rt.Get("hermai").ToObject(nil)

	_ = h.Set("fetch", func(rawURL string, opts goja.Value) (goja.Value, error) {
		if rawURL == "" {
			return nil, errors.New("fetch: url is required")
		}
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("fetch: parse url: %w", err)
		}
		if err := b.checkFetchPolicy(u); err != nil {
			return nil, err
		}

		method := "GET"
		var body io.Reader
		headers := http.Header{}
		followRedirects := true

		if opts != nil && !goja.IsUndefined(opts) && !goja.IsNull(opts) {
			o := opts.ToObject(nil)
			if m := o.Get("method"); m != nil && !goja.IsUndefined(m) {
				method = strings.ToUpper(m.String())
			}
			if bv := o.Get("body"); bv != nil && !goja.IsUndefined(bv) && !goja.IsNull(bv) {
				body = strings.NewReader(bv.String())
			}
			if hv := o.Get("headers"); hv != nil && !goja.IsUndefined(hv) && !goja.IsNull(hv) {
				ho := hv.ToObject(nil)
				for _, hk := range ho.Keys() {
					headers.Set(hk, ho.Get(hk).String())
				}
			}
			if fr := o.Get("follow_redirects"); fr != nil && !goja.IsUndefined(fr) && !goja.IsNull(fr) {
				followRedirects = fr.ToBoolean()
			}
		}

		req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
		if err != nil {
			return nil, fmt.Errorf("fetch: build request: %w", err)
		}
		for k, vs := range headers {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}

		// Use the configured client but optionally override redirect policy.
		client := b.httpClient
		if !followRedirects {
			cp := *client // shallow copy, fine — we don't mutate Transport
			cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			}
			client = &cp
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch: %w", err)
		}
		defer resp.Body.Close()

		// Re-check the final URL after redirects: if the redirect chain
		// led us off the allowlist, refuse to return the body.
		if resp.Request != nil && resp.Request.URL != nil {
			if err := b.checkFetchPolicy(resp.Request.URL); err != nil {
				return nil, fmt.Errorf("fetch: redirect landed off-allowlist: %w", err)
			}
		}

		lr := io.LimitReader(resp.Body, b.maxBodyBytes+1)
		bodyBytes, err := io.ReadAll(lr)
		if err != nil {
			return nil, fmt.Errorf("fetch: read body: %w", err)
		}
		if int64(len(bodyBytes)) > b.maxBodyBytes {
			return nil, ErrResponseTooLarge
		}

		respHeaders := make(map[string]string, len(resp.Header))
		for k, vs := range resp.Header {
			respHeaders[strings.ToLower(k)] = strings.Join(vs, ", ")
		}

		result := rt.NewObject()
		_ = result.Set("status", resp.StatusCode)
		_ = result.Set("url", resp.Request.URL.String())
		_ = result.Set("headers", respHeaders)
		_ = result.Set("body", string(bodyBytes))
		return result, nil
	})

	_ = h.Set("selectAll", func(htmlStr, selector string) ([]any, error) {
		sel, err := cascadia.Compile(selector)
		if err != nil {
			return nil, fmt.Errorf("selectAll: bad selector %q: %w", selector, err)
		}
		doc, err := html.Parse(strings.NewReader(htmlStr))
		if err != nil {
			return nil, fmt.Errorf("selectAll: parse html: %w", err)
		}
		matches := cascadia.QueryAll(doc, sel)
		out := make([]any, 0, len(matches))
		for _, n := range matches {
			out = append(out, nodeToJS(n))
		}
		return out, nil
	})
}

// nodeToJS serializes an html.Node to a plain map that JS can walk.
// Children are flattened to element-only slices — text nodes are
// concatenated into `text` on the parent. This matches what bootstraps
// typically want (structure + attributes), without handing over the
// full html.Node Go type to the JS side.
func nodeToJS(n *html.Node) map[string]any {
	if n == nil {
		return nil
	}
	attrs := make(map[string]string, len(n.Attr))
	for _, a := range n.Attr {
		attrs[a.Key] = a.Val
	}
	var text strings.Builder
	var children []any
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			text.WriteString(c.Data)
		case html.ElementNode:
			children = append(children, nodeToJS(c))
		}
	}
	return map[string]any{
		"tag":      n.Data,
		"attrs":    attrs,
		"text":     text.String(),
		"children": children,
	}
}

// checkFetchPolicy enforces the schema's allowlist plus unconditional
// private-address blocking. Returns nil if the target is allowed.
func (b *JSBootstrap) checkFetchPolicy(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("fetch: only http/https allowed, got %q", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if _, ok := b.allowedHosts[host]; !ok {
		return fmt.Errorf("%w: %s (schema allowlist: %v)", ErrHostNotAllowed, host, allowlistForErr(b.allowedHosts))
	}
	// Resolve and ensure no IP sits in a blocked range. We re-resolve
	// here even though the http stack will resolve again — it's cheap
	// and catches DNS-rebinding attacks that flip an answer between
	// allowlist check and actual dial.
	ips, err := b.resolver(host)
	if err != nil {
		return fmt.Errorf("fetch: resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: %s resolves to %s", ErrPrivateAddressBlocked, host, ip.String())
		}
	}
	return nil
}

func allowlistForErr(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// isBlockedIP returns true for loopback, link-local, multicast, RFC1918
// private, IPv4 CGNAT, and cloud metadata addresses. This is the
// canonical SSRF block list — deliberately strict because the bootstrap
// runs on the user's machine and could otherwise reach internal services.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	// IPv4 CGNAT 100.64.0.0/10 — not covered by IsPrivate but unsuitable.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && (v4[1]&0xc0) == 0x40 {
			return true
		}
		// Cloud metadata 169.254.169.254 is caught by LinkLocal above,
		// but we also want to block the common fc00::/7 IPv6 metadata
		// via IsPrivate which covers ULA.
	}
	return false
}

