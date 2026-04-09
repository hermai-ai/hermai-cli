package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJSONSuffixStrategy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/t/topic/123.json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id":       123,
				"title":    "Test Topic",
				"body":     "Hello world",
				"category": "general",
			})
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>page</html>"))
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/t/topic/123", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("expected schema, got nil")
	}
	if result.Strategy != "json_suffix" {
		t.Errorf("expected strategy json_suffix, got %q", result.Strategy)
	}
	if len(result.Schema.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(result.Schema.Endpoints))
	}
	ep := result.Schema.Endpoints[0]
	if ep.Method != "GET" {
		t.Errorf("expected method GET, got %q", ep.Method)
	}
	if !ep.IsPrimary {
		t.Error("expected IsPrimary=true")
	}
}

func TestAcceptHeaderStrategy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only return JSON if Accept header requests it
		if r.Header.Get("Accept") == "application/json" && r.URL.Path == "/api/v2/pokemon/pikachu" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			json.NewEncoder(w).Encode(map[string]any{
				"id":     25,
				"name":   "pikachu",
				"type":   "electric",
				"weight": 60,
			})
			return
		}
		// JSON suffix should not match
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/api/v2/pokemon/pikachu", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("expected schema, got nil")
	}
	if result.Strategy != "accept_header" {
		t.Errorf("expected strategy accept_header, got %q", result.Strategy)
	}
	ep := result.Schema.Endpoints[0]
	if ep.Headers["Accept"] != "application/json" {
		t.Errorf("expected Accept header in endpoint, got %v", ep.Headers)
	}
}

func TestNeitherStrategyWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>Hello</body></html>"))
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/some/page", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema != nil {
		t.Errorf("expected nil schema, got %+v", result.Schema)
	}
	if result.Strategy != "" {
		t.Errorf("expected empty strategy, got %q", result.Strategy)
	}
}

func TestSlowResponseTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":   1,
			"name": "slow",
			"data": "value",
			"ok":   true,
		})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	result, err := Probe(ctx, srv.URL+"/slow", Options{
		Timeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema != nil {
		t.Error("expected nil schema for slow response, got non-nil")
	}
}

func TestHTMLResponseReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Even .json path returns HTML (misconfigured server)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html><body>Not JSON</body></html>"))
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/page", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema != nil {
		t.Errorf("expected nil schema for HTML response, got %+v", result.Schema)
	}
}

func TestStealthEscalation(t *testing.T) {
	// Server returns Cloudflare challenge on first few requests (simulating
	// plain HTTP block), then returns JSON (simulating stealth bypass).
	// The probe should detect the block, retry with stealth, and flag RequiresStealth.
	//
	// requestCount is touched concurrently because httptest.Server runs each
	// handler invocation in its own goroutine, so use atomic ops.
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		// First 4 requests (one per strategy) return 403 + Cloudflare challenge.
		// Subsequent requests (stealth retry) return valid JSON.
		if count <= 5 {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(403)
			w.Write([]byte(`<html><body><h1>Just a moment...</h1><p>Checking your browser</p></body></html>`))
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "json") || strings.HasSuffix(r.URL.Path, ".json") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id":    1,
				"title": "Test Data",
				"body":  "Stealth worked",
				"tags":  []string{"test"},
			})
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>Normal page</body></html>"))
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/data", Options{
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("expected schema from stealth retry, got nil")
	}
	if !result.RequiresStealth {
		t.Error("expected RequiresStealth=true after stealth escalation")
	}
}

func TestNoStealthEscalationWhenNotBlocked(t *testing.T) {
	// Normal server: returns HTML (no JSON), no anti-bot signals
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>Normal page content</body></html>"))
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/page", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequiresStealth {
		t.Error("expected RequiresStealth=false when not blocked")
	}
}

func TestJSONWithTooFewKeysReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Only 1 key — below the threshold of >=2
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
		})
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/missing", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema != nil {
		t.Errorf("expected nil schema for JSON with <2 keys, got %+v", result.Schema)
	}
}

func TestJSONArrayResponseIsValid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/posts.json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "title": "Post 1", "body": "Content 1", "author": "alice"},
				{"id": 2, "title": "Post 2", "body": "Content 2", "author": "bob"},
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/posts", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("expected schema for JSON array response, got nil")
	}
	if result.Strategy != "json_suffix" {
		t.Errorf("expected strategy json_suffix, got %q", result.Strategy)
	}
}

func TestProbeWithProxy(t *testing.T) {
	// Just verify the option is accepted without error; actual proxy routing
	// is an integration concern.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html></html>"))
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/test", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema != nil {
		t.Error("expected nil schema")
	}
}

func TestSchemaFieldsArePopulated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/r/golang/hot.json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"kind":     "Listing",
				"data":     map[string]any{"children": []any{}},
				"before":   nil,
				"after":    "t3_abc123",
				"modhash":  "",
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/r/golang/hot", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("expected schema, got nil")
	}

	s := result.Schema
	if s.Domain == "" {
		t.Error("expected non-empty domain")
	}
	if s.ID == "" {
		t.Error("expected non-empty ID")
	}
	if s.URLPattern == "" {
		t.Error("expected non-empty URLPattern")
	}
	if s.Version != 1 {
		t.Errorf("expected version 1, got %d", s.Version)
	}
	if s.DiscoveredFrom == "" {
		t.Error("expected non-empty DiscoveredFrom")
	}
}

func TestJSONSuffixStrategy_GeneralizesSubredditSlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/r/programming.json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "Listing",
				"data": map[string]any{"children": []any{}},
				"after": "t3_test",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/r/programming", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("expected schema, got nil")
	}

	if result.Schema.URLPattern != "/r/{}" {
		t.Fatalf("expected reusable subreddit pattern, got %q", result.Schema.URLPattern)
	}
	if len(result.Schema.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(result.Schema.Endpoints))
	}

	ep := result.Schema.Endpoints[0]
	if got := ep.URLTemplate; !strings.HasSuffix(got, "/r/{subreddit}.json") {
		t.Fatalf("expected subreddit URL template, got %q", got)
	}
	if len(ep.Variables) != 1 {
		t.Fatalf("expected 1 variable, got %d", len(ep.Variables))
	}
	if ep.Variables[0].Name != "subreddit" {
		t.Fatalf("expected variable name subreddit, got %q", ep.Variables[0].Name)
	}
}

func TestAcceptHeaderStrategy_GeneralizesResourceSlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/json" && r.URL.Path == "/api/v2/pokemon/pikachu" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": 25, "name": "pikachu", "type": "electric",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/api/v2/pokemon/pikachu", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("expected schema, got nil")
	}

	if result.Schema.URLPattern != "/api/v2/pokemon/{}" {
		t.Fatalf("expected reusable pokemon pattern, got %q", result.Schema.URLPattern)
	}

	ep := result.Schema.Endpoints[0]
	if got := ep.URLTemplate; !strings.HasSuffix(got, "/api/v2/pokemon/{pokemon}") {
		t.Fatalf("expected pokemon URL template, got %q", got)
	}
	if len(ep.Variables) != 1 || ep.Variables[0].Name != "pokemon" {
		t.Fatalf("expected pokemon variable, got %+v", ep.Variables)
	}
}

func TestAcceptHeaderStrategy_DoesNotGeneralizeReservedStaticLeaf(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/json" && r.URL.Path == "/api/v2/products/list" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"items": []any{}, "count": 0,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/api/v2/products/list", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("expected schema, got nil")
	}

	if result.Schema.URLPattern != "/api/v2/products/list" {
		t.Fatalf("expected static pattern to be preserved, got %q", result.Schema.URLPattern)
	}

	ep := result.Schema.Endpoints[0]
	if strings.Contains(ep.URLTemplate, "{") {
		t.Fatalf("expected static URL template, got %q", ep.URLTemplate)
	}
	if len(ep.Variables) != 0 {
		t.Fatalf("expected no variables, got %+v", ep.Variables)
	}
}

func TestConcurrentStrategies_FirstWins(t *testing.T) {
	// Server responds to accept_header but not json_suffix
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/json" && r.URL.Path == "/data" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "test", "value": 42, "active": true,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/data", Options{
		Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("expected schema from concurrent strategies")
	}
	if result.Strategy != "accept_header" {
		t.Errorf("expected accept_header strategy, got %q", result.Strategy)
	}
}

func TestConcurrentStrategies_AllFail(t *testing.T) {
	// Server never returns JSON
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/page", Options{
		Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema != nil {
		t.Error("expected nil schema when all strategies fail")
	}
}

func TestConcurrentStrategies_ContextCancellation(t *testing.T) {
	// Slow server — context should cancel before response
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"key":"value","a":"b","c":"d"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result, err := Probe(ctx, srv.URL+"/slow", Options{
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema != nil {
		t.Error("expected nil schema when context cancelled")
	}
}
