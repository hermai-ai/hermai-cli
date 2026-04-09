//go:build integration

package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRodBrowser_CaptureIntegration(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
	<h1>Product Page</h1>
	<div id="content">Loading...</div>
	<script>
		fetch('/api/data')
			.then(r => r.json())
			.then(d => {
				document.getElementById('content').textContent = d.name;
			});
	</script>
</body>
</html>`)
	})

	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name": "TestProduct", "price": 29.99}`)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	rb, err := NewRodBrowser("")
	if err != nil {
		t.Fatalf("failed to create RodBrowser: %v", err)
	}
	defer rb.Close()

	ctx := context.Background()
	result, err := rb.Capture(ctx, server.URL, CaptureOpts{
		Timeout:       15 * time.Second,
		WaitAfterLoad: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Capture failed: %v", err)
	}

	if result.HAR == nil {
		t.Fatal("HAR should not be nil")
	}

	if len(result.HAR.Entries) == 0 {
		t.Fatal("HAR should contain at least one entry")
	}

	foundAPICall := false
	for _, entry := range result.HAR.Entries {
		if strings.Contains(entry.Request.URL, "/api/data") {
			foundAPICall = true
			if entry.Response.Status != 200 {
				t.Errorf("expected 200 status for /api/data, got %d", entry.Response.Status)
			}
			if !strings.Contains(entry.Response.ContentType, "json") {
				t.Errorf("expected JSON content type for /api/data, got %q", entry.Response.ContentType)
			}
			if !strings.Contains(entry.Response.Body, "TestProduct") {
				t.Errorf("expected response body to contain TestProduct, got %q", entry.Response.Body)
			}
			break
		}
	}
	if !foundAPICall {
		t.Error("HAR should contain the /api/data request")
	}

	if result.DOMSnapshot == "" {
		t.Error("DOMSnapshot should not be empty")
	}

	if !strings.Contains(result.DOMSnapshot, "Product Page") {
		t.Errorf("DOMSnapshot should contain 'Product Page', got %q", result.DOMSnapshot)
	}
}

func TestRodBrowser_CaptureAuthDetection(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	})

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<body>
	<h1>Sign In</h1>
	<form>
		<input type="password" name="password">
		<button type="submit">Log In</button>
	</form>
</body>
</html>`)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	rb, err := NewRodBrowser("")
	if err != nil {
		t.Fatalf("failed to create RodBrowser: %v", err)
	}
	defer rb.Close()

	ctx := context.Background()
	_, err = rb.Capture(ctx, server.URL, CaptureOpts{
		Timeout:       15 * time.Second,
		WaitAfterLoad: 2 * time.Second,
	})

	if err == nil {
		t.Fatal("expected auth wall error, got nil")
	}

	if !strings.Contains(err.Error(), "authentication") {
		t.Errorf("expected auth-related error message, got: %v", err)
	}
}
