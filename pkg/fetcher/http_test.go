package fetcher

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/retry"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// fastRetry returns a retry config with minimal delays for testing.
func fastRetry() retry.Config {
	return retry.Config{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
	}
}

// newTestFetcher creates a fetcher with fast retry for tests.
func newTestFetcher() *HTTPFetcher {
	return NewHTTPFetcher().WithRetry(fastRetry())
}

func TestFetch_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"title": "Test Product",
			"price": 29.99,
		})
	}))
	defer server.Close()

	s := &schema.Schema{
		ID:      "test-schema-1",
		Version: 1,
		Endpoints: []schema.Endpoint{
			{
				Name:        "get-product",
				Method:      "GET",
				URLTemplate: server.URL + "/api/products/{productId}",
				Variables: []schema.Variable{
					{Name: "productId", Source: "fixed", Pattern: "123"},
				},
				IsPrimary: true,
			},
		},
	}

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(context.Background(), s, "https://example.com/products/123", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.DataMap()["title"] != "Test Product" {
		t.Errorf("expected title 'Test Product', got %v", result.DataMap()["title"])
	}
	if result.DataMap()["price"] != 29.99 {
		t.Errorf("expected price 29.99, got %v", result.DataMap()["price"])
	}
	if result.Metadata.SchemaID != "test-schema-1" {
		t.Errorf("expected schema ID 'test-schema-1', got %s", result.Metadata.SchemaID)
	}
	if result.Metadata.SchemaVersion != 1 {
		t.Errorf("expected schema version 1, got %d", result.Metadata.SchemaVersion)
	}
	if result.Metadata.EndpointsCalled != 1 {
		t.Errorf("expected 1 endpoint called, got %d", result.Metadata.EndpointsCalled)
	}
	if result.Metadata.TotalLatencyMs < 0 {
		t.Errorf("expected non-negative latency, got %d", result.Metadata.TotalLatencyMs)
	}
	if result.Metadata.Source != "http_fetcher" {
		t.Errorf("expected source 'http_fetcher', got %s", result.Metadata.Source)
	}
}

func TestFetch_RawMode(t *testing.T) {
	responseBody := map[string]any{"id": "abc", "status": "active"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "test-value")
		json.NewEncoder(w).Encode(responseBody)
	}))
	defer server.Close()

	s := &schema.Schema{
		ID:      "raw-test",
		Version: 2,
		Endpoints: []schema.Endpoint{
			{
				Name:        "get-item",
				Method:      "GET",
				URLTemplate: server.URL + "/api/item",
				IsPrimary:   true,
			},
		},
	}

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(context.Background(), s, "https://example.com/item", FetchOpts{Raw: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Raw) != 1 {
		t.Fatalf("expected 1 raw response, got %d", len(result.Raw))
	}

	raw := result.Raw[0]
	if raw.EndpointName != "get-item" {
		t.Errorf("expected endpoint name 'get-item', got %s", raw.EndpointName)
	}
	if raw.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", raw.StatusCode)
	}
	if raw.Headers["Content-Type"] != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %s", raw.Headers["Content-Type"])
	}
	if raw.Headers["X-Custom"] != "test-value" {
		t.Errorf("expected X-Custom 'test-value', got %s", raw.Headers["X-Custom"])
	}
	if raw.Body == nil {
		t.Error("expected non-nil body")
	}
}

func TestFetch_HeaderOverrides(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	s := &schema.Schema{
		ID:      "header-test",
		Version: 1,
		Endpoints: []schema.Endpoint{
			{
				Name:        "get-data",
				Method:      "GET",
				URLTemplate: server.URL + "/api/data",
				Headers: map[string]string{
					"Authorization": "Bearer original-token",
					"Accept":        "application/json",
				},
				IsPrimary: true,
			},
		},
	}

	fetcher := newTestFetcher()
	_, err := fetcher.Fetch(context.Background(), s, "https://example.com/data", FetchOpts{
		HeaderOverrides: map[string]string{
			"Authorization": "Bearer override-token",
			"X-Extra":       "extra-value",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedHeaders.Get("Authorization") != "Bearer override-token" {
		t.Errorf("expected overridden Authorization header, got %s", receivedHeaders.Get("Authorization"))
	}
	if receivedHeaders.Get("Accept") != "application/json" {
		t.Errorf("expected Accept header preserved, got %s", receivedHeaders.Get("Accept"))
	}
	if receivedHeaders.Get("X-Extra") != "extra-value" {
		t.Errorf("expected X-Extra header, got %s", receivedHeaders.Get("X-Extra"))
	}
}

func TestFetch_NonTwoXX_ReturnsErrSchemaBroken(t *testing.T) {
	statusCodes := []int{400, 401, 403, 404, 500}

	for _, code := range statusCodes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				w.Write([]byte(`{"error":"forbidden"}`))
			}))
			defer server.Close()

			s := &schema.Schema{
				ID:      "broken-test",
				Version: 1,
				Endpoints: []schema.Endpoint{
					{
						Name:        "get-protected",
						Method:      "GET",
						URLTemplate: server.URL + "/api/protected",
						IsPrimary:   true,
					},
				},
			}

			fetcher := newTestFetcher()
			_, err := fetcher.Fetch(context.Background(), s, "https://example.com/protected", FetchOpts{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrSchemaBroken) {
				t.Errorf("expected error to wrap ErrSchemaBroken, got: %v", err)
			}
		})
	}
}

func TestFetch_TransientStatusCodes_ReturnsErrTransient(t *testing.T) {
	transientCodes := []int{429, 502, 503, 504}

	for _, code := range transientCodes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer server.Close()

			s := &schema.Schema{
				ID:      "transient-test",
				Version: 1,
				Endpoints: []schema.Endpoint{
					{
						Name:        "api",
						Method:      "GET",
						URLTemplate: server.URL + "/api/data",
						IsPrimary:   true,
					},
				},
			}

			fetcher := newTestFetcher()
			_, err := fetcher.Fetch(context.Background(), s, "https://example.com/data", FetchOpts{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrTransient) {
				t.Errorf("expected error to wrap ErrTransient for %d, got: %v", code, err)
			}
		})
	}
}

func TestFetch_MultipleEndpoints(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/product":
			json.NewEncoder(w).Encode(map[string]any{
				"name":  "Widget",
				"price": 10.50,
			})
		case "/api/reviews":
			json.NewEncoder(w).Encode(map[string]any{
				"count":   42,
				"average": 4.5,
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()

	s := &schema.Schema{
		ID:      "multi-endpoint",
		Version: 3,
		Endpoints: []schema.Endpoint{
			{
				Name:        "get-product",
				Method:      "GET",
				URLTemplate: server.URL + "/api/product",
				IsPrimary:   true,
			},
			{
				Name:        "get-reviews",
				Method:      "GET",
				URLTemplate: server.URL + "/api/reviews",
				IsPrimary:   false,
			},
		},
	}

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(context.Background(), s, "https://example.com/product/1", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if callCount.Load() != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount.Load())
	}
	if result.Metadata.EndpointsCalled != 2 {
		t.Errorf("expected 2 endpoints called in metadata, got %d", result.Metadata.EndpointsCalled)
	}

	// Multi-endpoint: data is nested by endpoint name
	dataMap := result.DataMap()
	productData, ok := dataMap["get-product"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'get-product' nested in data, got %v", dataMap)
	}
	if productData["name"] != "Widget" {
		t.Errorf("expected 'Widget' in get-product data, got %v", productData["name"])
	}
	if productData["price"] != 10.50 {
		t.Errorf("expected 10.50 in get-product data, got %v", productData["price"])
	}

	// Non-primary endpoint data should also be present under its name
	reviewsData, ok := dataMap["get-reviews"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'get-reviews' nested in data, got %v", dataMap)
	}
	if reviewsData["count"] != float64(42) {
		t.Errorf("expected 42 in get-reviews count, got %v", reviewsData["count"])
	}
}

func TestFetch_PostWithBody(t *testing.T) {
	var receivedBody map[string]any
	var receivedContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"result": "created"})
	}))
	defer server.Close()

	s := &schema.Schema{
		ID:      "post-test",
		Version: 1,
		Endpoints: []schema.Endpoint{
			{
				Name:        "create-item",
				Method:      "POST",
				URLTemplate: server.URL + "/api/items",
				Body: &schema.BodyTemplate{
					ContentType: "application/json",
					Template:    `{"query": "{searchTerm}", "limit": 10}`,
				},
				Variables: []schema.Variable{
					{Name: "searchTerm", Source: "query", Pattern: "q"},
				},
				IsPrimary: true,
			},
		},
	}

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(context.Background(), s, "https://example.com/search?q=hello", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedContentType != "application/json" {
		t.Errorf("expected content type 'application/json', got %s", receivedContentType)
	}
	if receivedBody["query"] != "hello" {
		t.Errorf("expected query 'hello', got %v", receivedBody["query"])
	}
	if result.DataMap()["result"] != "created" {
		t.Errorf("expected result 'created', got %v", result.DataMap()["result"])
	}
}

func TestFetch_ArrayResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "title": "First"},
			{"id": 2, "title": "Second"},
		})
	}))
	defer server.Close()

	s := &schema.Schema{
		ID:      "array-test",
		Version: 1,
		Endpoints: []schema.Endpoint{
			{
				Name:        "list-items",
				Method:      "GET",
				URLTemplate: server.URL + "/api/items",
				IsPrimary:   true,
			},
		},
	}

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(context.Background(), s, "https://example.com/items", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	arr, ok := result.Data.([]any)
	if !ok {
		t.Fatalf("expected Data to be []any, got %T", result.Data)
	}
	if len(arr) != 2 {
		t.Errorf("expected 2 items, got %d", len(arr))
	}
	if result.DataMap() != nil {
		t.Error("expected DataMap() to return nil for array response")
	}
}

func TestFetch_ArrayResponse_Concurrent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/items":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "title": "First"},
			})
		case "/api/meta":
			json.NewEncoder(w).Encode(map[string]any{"total": 1})
		}
	}))
	defer server.Close()

	s := &schema.Schema{
		ID:      "array-concurrent-test",
		Version: 1,
		Endpoints: []schema.Endpoint{
			{
				Name:        "list-items",
				Method:      "GET",
				URLTemplate: server.URL + "/api/items",
				IsPrimary:   true,
			},
			{
				Name:        "get-meta",
				Method:      "GET",
				URLTemplate: server.URL + "/api/meta",
				IsPrimary:   false,
			},
		},
	}

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(context.Background(), s, "https://example.com/items", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Multi-endpoint: data is nested by name
	dataMap := result.DataMap()
	if dataMap == nil {
		t.Fatalf("expected Data to be map[string]any, got %T", result.Data)
	}
	listItems, ok := dataMap["list-items"].([]any)
	if !ok {
		t.Fatalf("expected 'list-items' to be []any, got %T", dataMap["list-items"])
	}
	if len(listItems) != 1 {
		t.Errorf("expected 1 item, got %d", len(listItems))
	}
	meta, ok := dataMap["get-meta"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'get-meta' to be map, got %T", dataMap["get-meta"])
	}
	if meta["total"] != float64(1) {
		t.Errorf("expected total=1, got %v", meta["total"])
	}
}

func TestFetch_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	s := &schema.Schema{
		ID:      "ctx-test",
		Version: 1,
		Endpoints: []schema.Endpoint{
			{
				Name:        "get-data",
				Method:      "GET",
				URLTemplate: server.URL + "/api/data",
				IsPrimary:   true,
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	fetcher := newTestFetcher()
	_, err := fetcher.Fetch(ctx, s, "https://example.com/data", FetchOpts{})
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}
