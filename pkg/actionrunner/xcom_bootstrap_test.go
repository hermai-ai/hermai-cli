package actionrunner

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hermai-ai/hermai-cli/pkg/signer"
)

// TestXcomBootstrapJS_MatchesGoReference runs the checked-in
// schemas/xcom/bootstrap.js through our real goja + cascadia sandbox
// against the same fixtures the Go bootstrap uses, and confirms the
// produced animation_key matches the pinned Go output.
//
// The Go reference value comes from pkg/sessions/xcom's integration
// test running the Go bootstrap against the same fixtures. If the JS
// port drifts from Go, this test is the tripwire.
func TestXcomBootstrapJS_MatchesGoReference(t *testing.T) {
	homeHTML := mustReadFixture(t, "homepage.html")
	ondemandJS := mustReadFixture(t, "ondemand.js")
	bootstrapJS := mustReadFile(t, filepath.Join("testdata", "xcom", "bootstrap.js"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.Host, "x.com"), r.URL.Path == "/":
			_, _ = io.WriteString(w, homeHTML)
		case strings.Contains(r.URL.Path, "ondemand.s"):
			_, _ = io.WriteString(w, ondemandJS)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// The bootstrap.js hits "https://x.com" and "https://abs.twimg.com/..."
	// literally. Rewrite those to hit our test server by passing a
	// custom transport that redirects them.
	tr := &rewritingTransport{to: srv.URL, inner: srv.Client().Transport}
	client := &http.Client{Transport: tr}

	b, err := signer.NewJSBootstrap(signer.BootstrapConfig{
		Source:       bootstrapJS,
		AllowedHosts: []string{"x.com", "abs.twimg.com"},
		HTTPClient:   client,
	})
	if err != nil {
		t.Fatalf("NewJSBootstrap: %v", err)
	}
	// httptest binds loopback; override SSRF resolver for this test.
	overrideResolverToPublic(b)

	state, err := b.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	const expectedAnimationKey = "ff000ee147ae147ae1805eb851eb851eb805eb851eb851eb80ee147ae147ae1800"
	if state["animation_key"] != expectedAnimationKey {
		t.Errorf("animation_key drift:\n  got  %q\n  want %q", state["animation_key"], expectedAnimationKey)
	}
	if state["additional_random_number"] != "3" {
		t.Errorf("additional_random_number = %q, want 3", state["additional_random_number"])
	}
	if state["key_b64"] == "" {
		t.Error("key_b64 is empty")
	}
	t.Logf("bootstrap.js produced: key_b64=%q animation_key=%q", state["key_b64"], state["animation_key"])
}

// --- test helpers ------------------------------------------------------------

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func mustReadFixture(t *testing.T, name string) string {
	t.Helper()
	return mustReadFile(t, filepath.Join("testdata", "xcom", name))
}

// rewritingTransport redirects requests to x.com / abs.twimg.com to a
// local httptest.Server so the bootstrap JS can be exercised without
// live network.
type rewritingTransport struct {
	to    string // "http://127.0.0.1:PORT"
	inner http.RoundTripper
}

func (t *rewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	originalURL := req.URL
	targetURL := t.to
	if req.URL.Path != "" {
		targetURL += req.URL.Path
	}
	if req.URL.RawQuery != "" {
		targetURL += "?" + req.URL.RawQuery
	}
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, targetURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header.Clone()
	resp, err := t.inner.RoundTrip(newReq)
	if err != nil {
		return nil, err
	}
	// Preserve the original (allowlisted) URL on the response so the
	// post-redirect policy re-check sees x.com, not 127.0.0.1.
	if resp.Request != nil {
		resp.Request.URL = originalURL
	}
	return resp, nil
}

// overrideResolverToPublic swaps JSBootstrap's resolver to pretend the
// target hosts resolve to a public IP — the SSRF guard would otherwise
// block loopback where httptest binds. Production code never uses this.
func overrideResolverToPublic(b *signer.JSBootstrap) {
	// The field is unexported; cross-package tests can't touch it
	// directly. We expose a test hook here via reflection or via a
	// helper in the signer package.
	signer.OverrideResolverForTest(b, func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil // example.com
	})
}
