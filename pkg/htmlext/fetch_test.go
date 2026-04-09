package htmlext

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchHTML_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "" || !strings.Contains(r.Header.Get("Accept"), "text/html") {
			t.Error("expected Accept header with text/html")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>Hello</body></html>"))
	}))
	defer server.Close()

	html, err := FetchHTML(context.Background(), server.URL, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(html, "Hello") {
		t.Errorf("expected body to contain 'Hello', got: %q", html)
	}
}

func TestFetchHTML_NonOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer server.Close()

	_, err := FetchHTML(context.Background(), server.URL, "", false)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention status code: %v", err)
	}
}

func TestFetchHTML_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>ok</html>"))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := FetchHTML(ctx, server.URL, "", false)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestFetchHTML_SizeLimit(t *testing.T) {
	// Serve 6MB of content — should be truncated to 5MB
	bigBody := strings.Repeat("a", 6*1024*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(bigBody))
	}))
	defer server.Close()

	html, err := FetchHTML(context.Background(), server.URL, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(html) > maxHTMLSize {
		t.Errorf("response exceeds max size: %d > %d", len(html), maxHTMLSize)
	}
}
