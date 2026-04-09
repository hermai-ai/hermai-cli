package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShopifyDetection_ValidStore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/products.json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"products": []any{
					map[string]any{"id": 1, "title": "Test Product", "handle": "test-product"},
				},
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/products/test-product", Options{
		Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("expected schema, got nil")
	}
	if result.Strategy != "shopify" {
		t.Errorf("expected strategy 'shopify', got %q", result.Strategy)
	}
	if len(result.Schema.Endpoints) != 2 {
		t.Errorf("expected 2 endpoints, got %d", len(result.Schema.Endpoints))
	}
	if len(result.Schema.Actions) != 4 {
		t.Errorf("expected 4 actions, got %d", len(result.Schema.Actions))
	}

	// Verify action names
	actionNames := make(map[string]bool)
	for _, a := range result.Schema.Actions {
		actionNames[a.Name] = true
	}
	for _, expected := range []string{"add_to_cart", "get_cart", "update_cart", "change_cart_item"} {
		if !actionNames[expected] {
			t.Errorf("missing action %q", expected)
		}
	}
}

func TestShopifyDetection_NotShopify(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/page", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Strategy == "shopify" {
		t.Error("should not detect as Shopify")
	}
}

func TestShopifyDetection_InvalidShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/products.json" {
			w.Header().Set("Content-Type", "application/json")
			// Valid JSON but wrong shape (no "products" key)
			json.NewEncoder(w).Encode(map[string]any{
				"items": []any{"a", "b"},
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	result, err := Probe(context.Background(), srv.URL+"/page", Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Strategy == "shopify" {
		t.Error("should not detect as Shopify for wrong JSON shape")
	}
}
