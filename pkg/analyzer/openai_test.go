package analyzer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hermai-ai/hermai-cli/pkg/browser"
)

func makeTestHAR() *browser.HARLog {
	return &browser.HARLog{
		Entries: []browser.HAREntry{
			{
				Request: browser.HARRequest{
					Method:  "GET",
					URL:     "https://api.example.com/v1/products/123",
					Headers: map[string]string{"Authorization": "Bearer token"},
				},
				Response: browser.HARResponse{
					Status:      200,
					ContentType: "application/json",
					Body:        `{"id": 123, "name": "Widget"}`,
				},
			},
		},
	}
}

func makeLLMResponse(endpoints string) string {
	return `{
		"id": "chatcmpl-test",
		"choices": [{
			"message": {
				"content": ` + endpoints + `
			}
		}]
	}`
}

func TestOpenAIAnalyzer_HappyPath(t *testing.T) {
	endpointsJSON := `"{\"endpoints\": [{\"name\": \"Get Product\", \"method\": \"GET\", \"url_template\": \"https://api.example.com/v1/products/{id}\", \"headers\": {\"Authorization\": \"Bearer {token}\"}, \"variables\": [{\"name\": \"id\", \"source\": \"url\", \"pattern\": \"\\\\d+\"}], \"is_primary\": true}]}"`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(makeLLMResponse(endpointsJSON)))
	}))
	defer server.Close()

	analyzer := NewOpenAIAnalyzer(OpenAIConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Model:   "gpt-4",
	})

	result, err := analyzer.Analyze(context.Background(), makeTestHAR(), "<html>Widget</html>", "https://example.com/products/123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Domain != "example.com" {
		t.Errorf("expected domain 'example.com', got '%s'", result.Domain)
	}
	if result.URLPattern != "/products/{}" {
		t.Errorf("expected URL pattern '/products/{}', got '%s'", result.URLPattern)
	}
	if result.Version != 1 {
		t.Errorf("expected version 1, got %d", result.Version)
	}
	if result.DiscoveredFrom != "https://example.com/products/123" {
		t.Errorf("expected discovered from URL, got '%s'", result.DiscoveredFrom)
	}
	if len(result.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(result.Endpoints))
	}
	if result.Endpoints[0].Name != "Get Product" {
		t.Errorf("expected endpoint name 'Get Product', got '%s'", result.Endpoints[0].Name)
	}
	if result.Endpoints[0].Method != "GET" {
		t.Errorf("expected method GET, got '%s'", result.Endpoints[0].Method)
	}
	if !result.Endpoints[0].IsPrimary {
		t.Error("expected endpoint to be primary")
	}
	if result.ID == "" {
		t.Error("expected non-empty ID")
	}
	if result.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestOpenAIAnalyzer_RequestStructure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected path /chat/completions, got %s", r.URL.Path)
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer test-api-key" {
			t.Errorf("expected 'Bearer test-api-key', got '%s'", authHeader)
		}

		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}
		if req["model"] != "test-model" {
			t.Errorf("expected model 'test-model', got '%v'", req["model"])
		}
		messages, ok := req["messages"].([]any)
		if !ok || len(messages) != 2 {
			t.Fatalf("expected 2 messages, got %v", req["messages"])
		}

		endpointsJSON := `"{\"endpoints\": [{\"name\": \"Test\", \"method\": \"GET\", \"url_template\": \"https://api.example.com/test\", \"headers\": {}, \"variables\": [], \"is_primary\": true}]}"`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(makeLLMResponse(endpointsJSON)))
	}))
	defer server.Close()

	analyzer := NewOpenAIAnalyzer(OpenAIConfig{
		BaseURL: server.URL,
		APIKey:  "test-api-key",
		Model:   "test-model",
	})

	_, err := analyzer.Analyze(context.Background(), makeTestHAR(), "<html></html>", "https://example.com/page")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenAIAnalyzer_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "internal server error"}`))
	}))
	defer server.Close()

	analyzer := NewOpenAIAnalyzer(OpenAIConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Model:   "gpt-4",
	})

	_, err := analyzer.Analyze(context.Background(), makeTestHAR(), "<html></html>", "https://example.com")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to contain '500', got '%s'", err.Error())
	}
}

func TestOpenAIAnalyzer_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer server.Close()

	analyzer := NewOpenAIAnalyzer(OpenAIConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Model:   "gpt-4",
	})

	_, err := analyzer.Analyze(context.Background(), makeTestHAR(), "<html></html>", "https://example.com")
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestOpenAIAnalyzer_EmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "test", "choices": []}`))
	}))
	defer server.Close()

	analyzer := NewOpenAIAnalyzer(OpenAIConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Model:   "gpt-4",
	})

	_, err := analyzer.Analyze(context.Background(), makeTestHAR(), "<html></html>", "https://example.com")
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	if !strings.Contains(err.Error(), "choices") {
		t.Errorf("expected error about choices, got '%s'", err.Error())
	}
}
