package httpclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/httpclient"
)

// TestReExportSurface is a compile-time smoke test that the shim still
// exposes every symbol external consumers depend on. If the internal
// package renames or removes one, this file fails to build — loud,
// local failure instead of a surprise at an external call site.
func TestReExportSurface(t *testing.T) {
	_ = httpclient.Doer(nil)

	opts := httpclient.Options{Timeout: 1 * time.Second}
	plain := httpclient.New(opts)
	if plain == nil {
		t.Fatal("New returned nil")
	}

	if _, err := httpclient.NewStealth(opts); err != nil {
		t.Fatalf("NewStealth: %v", err)
	}
	_ = httpclient.MustNewStealth(opts)
	_ = httpclient.NewStealthOrFallback(opts)

	if _, err := httpclient.NewStealthWithRedirects(opts, 5); err != nil {
		t.Fatalf("NewStealthWithRedirects: %v", err)
	}
}

// TestPlainClientDoesHTTP confirms the plain client can still issue an
// HTTP call through the shim — catches a regression where the re-export
// accidentally loses the underlying transport wiring.
func TestPlainClientDoesHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	c := httpclient.New(httpclient.Options{Timeout: 5 * time.Second})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
