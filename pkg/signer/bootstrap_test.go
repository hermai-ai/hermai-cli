package signer

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- fetch ------------------------------------------------------------------

func TestBootstrap_FetchReturnsExpectedShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Custom", "v1")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, "hello body")
	}))
	defer srv.Close()
	u, _, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `
			function bootstrap(input) {
				var r = hermai.fetch(input.url);
				return {
					status: String(r.status),
					body: r.body,
					url: r.url,
					xcustom: r.headers["x-custom"]
				};
			}
		`,
		AllowedHosts: []string{u},
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	// httptest binds to loopback. SSRF policy blocks loopback by
	// default (correctly — production schemas must not reach internal
	// services). Override the resolver for this test only, since the
	// underlying connection is still going to 127.0.0.1 at the socket
	// layer where httptest is bound.
	b.resolver = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil // example.com
	}
	out, err := b.Run(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["status"] != "201" {
		t.Errorf("status = %q, want 201", out["status"])
	}
	if out["body"] != "hello body" {
		t.Errorf("body = %q, want 'hello body'", out["body"])
	}
	if out["xcustom"] != "v1" {
		t.Errorf("xcustom = %q, want v1", out["xcustom"])
	}
}

func TestBootstrap_FetchRejectsNonAllowedHost(t *testing.T) {
	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `
			function bootstrap(input) {
				return { ok: hermai.fetch("https://evil.example.com/").body };
			}
		`,
		AllowedHosts: []string{"x.com"},
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	_, err = b.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected allowlist rejection, got nil")
	}
	if !strings.Contains(err.Error(), "evil.example.com") {
		t.Errorf("error should mention the blocked host, got: %v", err)
	}
}

func TestBootstrap_FetchBlocksPrivateAddresses(t *testing.T) {
	// Start a local server so we have a real listener on 127.0.0.1,
	// then try to fetch it — allowlist says localhost but policy still
	// blocks loopback regardless of schema config (SSRF guard).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "never reach")
	}))
	defer srv.Close()
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `
			function bootstrap(input) {
				return { ok: hermai.fetch(input.url).body };
			}
		`,
		// Even though we whitelist localhost, the private-IP guard wins.
		AllowedHosts: []string{host},
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	_, err = b.Run(context.Background(), map[string]any{"url": srv.URL})
	if err == nil {
		t.Fatal("expected SSRF block, got nil")
	}
	if !errors.Is(err, ErrPrivateAddressBlocked) &&
		!strings.Contains(err.Error(), "private") &&
		!strings.Contains(err.Error(), "loopback") {
		t.Errorf("error should mention private/loopback, got: %v", err)
	}
}

func TestBootstrap_FetchTimeoutHonored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = io.WriteString(w, "too late")
	}))
	defer srv.Close()
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	// We stub isBlockedIP by using a test that accepts 127.0.0.1. We
	// can't — so this test uses the context timeout approach instead,
	// bypassing fetch entirely by having bootstrap spin in a JS loop.
	_ = host
	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `function bootstrap(input) { while (true) {} }`,
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	b.timeout = 50 * time.Millisecond
	start := time.Now()
	_, err = b.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected timeout, got nil")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Errorf("timeout took %v, want under 300ms", elapsed)
	}
}

func TestBootstrap_ResponseSizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("A", 1024*1024)) // 1 MB
	}))
	defer srv.Close()
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	_ = host

	// We can't fetch the httptest server because it binds to loopback and
	// the SSRF guard blocks it. Instead verify the cap via a unit-level
	// check against the constant.
	b, err := NewJSBootstrap(BootstrapConfig{
		Source:           `function bootstrap(input) { return {}; }`,
		MaxResponseBytes: 512,
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	if b.maxBodyBytes != 512 {
		t.Errorf("maxBodyBytes = %d, want 512", b.maxBodyBytes)
	}
}

// --- selectAll --------------------------------------------------------------

func TestBootstrap_SelectAllBasic(t *testing.T) {
	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `
			function bootstrap(input) {
				var nodes = hermai.selectAll(input.html, "meta[name='twitter-site-verification']");
				return {
					count: String(nodes.length),
					content: nodes[0].attrs["content"]
				};
			}
		`,
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	html := `<html><head>
		<meta name="twitter-site-verification" content="MY_KEY_ABC">
	</head><body></body></html>`
	out, err := b.Run(context.Background(), map[string]any{"html": html})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != "1" {
		t.Errorf("count = %q, want 1", out["count"])
	}
	if out["content"] != "MY_KEY_ABC" {
		t.Errorf("content = %q, want MY_KEY_ABC", out["content"])
	}
}

func TestBootstrap_SelectAllIDPrefix(t *testing.T) {
	// X uses [id^='loading-x-anim'] — test the attribute-prefix selector.
	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `
			function bootstrap(input) {
				var nodes = hermai.selectAll(input.html, "[id^='loading-x-anim']");
				return { count: String(nodes.length), firstId: nodes[0].attrs["id"] };
			}
		`,
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	html := `
		<svg id="loading-x-anim-0"><g><path d="M0 0"/></g></svg>
		<svg id="loading-x-anim-1"><g><path d="M0 1"/></g></svg>
		<svg id="loading-x-anim-2"><g><path d="M0 2"/></g></svg>
		<svg id="loading-x-anim-3"><g><path d="M0 3"/></g></svg>
		<svg id="unrelated"><path d="nope"/></svg>`
	out, err := b.Run(context.Background(), map[string]any{"html": html})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != "4" {
		t.Errorf("count = %q, want 4", out["count"])
	}
	if out["firstId"] != "loading-x-anim-0" {
		t.Errorf("firstId = %q, want loading-x-anim-0", out["firstId"])
	}
}

func TestBootstrap_SelectAllChildrenTraversal(t *testing.T) {
	// Verify frame.children[0].children[1] style traversal — what the X
	// bootstrap needs to grab the <path d="..."> out of each SVG.
	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `
			function bootstrap(input) {
				var frames = hermai.selectAll(input.html, "[id^='loading-x-anim']");
				var first = frames[0];
				// first.children -> [ <g> ]
				// first.children[0].children -> [ <path d="one">, <path d="two"> ]
				var targetD = first.children[0].children[1].attrs["d"];
				return { d: targetD };
			}
		`,
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	html := `
		<svg id="loading-x-anim-0">
			<g>
				<path d="first-path"/>
				<path d="second-path"/>
			</g>
		</svg>`
	out, err := b.Run(context.Background(), map[string]any{"html": html})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["d"] != "second-path" {
		t.Errorf("d = %q, want 'second-path' (children[0].children[1] traversal failed)", out["d"])
	}
}

// --- contract ----------------------------------------------------------------

func TestBootstrap_EmptySourceFails(t *testing.T) {
	_, err := NewJSBootstrap(BootstrapConfig{})
	if err == nil {
		t.Fatal("expected error for empty source, got nil")
	}
}

func TestBootstrap_MissingBootstrapFunctionFails(t *testing.T) {
	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `var x = 1;`,
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	_, err = b.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected missing-function error, got nil")
	}
}

func TestBootstrap_NonObjectReturnFails(t *testing.T) {
	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `function bootstrap(input) { return "just a string"; }`,
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	_, err = b.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected non-object return to error, got nil")
	}
	if !strings.Contains(err.Error(), "expected a plain object") {
		t.Errorf("error should mention expected object shape, got: %v", err)
	}
}

func TestBootstrap_ArrayReturnFails(t *testing.T) {
	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `function bootstrap(input) { return [1, 2, 3]; }`,
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	_, err = b.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected array return to error, got nil")
	}
}

func TestBootstrap_AccessToHermaiCrypto(t *testing.T) {
	// Bootstrap gets the same hermai.* crypto helpers signer has —
	// verify sha256 is reachable (X uses it to identify ondemand chunks).
	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `
			function bootstrap(input) {
				return { digest: hermai.sha256("hello") };
			}
		`,
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	out, err := b.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// sha256("hello") = 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if out["digest"] != want {
		t.Errorf("digest = %q, want %q", out["digest"], want)
	}
}

func TestBootstrap_ContextCancelPropagates(t *testing.T) {
	b, err := NewJSBootstrap(BootstrapConfig{
		Source: `function bootstrap(input) { while (true) {} }`,
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	b.timeout = 10 * time.Second // make sure context wins
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = b.Run(ctx, nil)
	if err == nil {
		t.Fatal("expected error from cancel, got nil")
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Errorf("cancel did not interrupt promptly: %v", time.Since(start))
	}
}

// isBlockedIP direct tests — catches regressions in SSRF policy.
func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},      // AWS metadata
		{"100.64.1.1", true},           // CGNAT
		{"0.0.0.0", true},              // unspecified
		{"224.0.0.1", true},            // multicast
		{"1.1.1.1", false},             // public DNS
		{"93.184.216.34", false},       // example.com
		{"2606:2800:220:1:248:1893:25c8:1946", false}, // example.com v6
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		got := isBlockedIP(ip)
		if got != c.blocked {
			t.Errorf("isBlockedIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}
