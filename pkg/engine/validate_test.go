package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

func TestValidateAllEndpoints_KeepsValid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": "value", "count": 42})
		case "/bad":
			w.WriteHeader(http.StatusNotFound)
		case "/html":
			w.Write([]byte(`<html>not json</html>`))
		}
	}))
	defer srv.Close()

	s := &schema.Schema{
		Endpoints: []schema.Endpoint{
			{Name: "good", Method: "GET", URLTemplate: srv.URL + "/good"},
			{Name: "bad", Method: "GET", URLTemplate: srv.URL + "/bad"},
			{Name: "html", Method: "GET", URLTemplate: srv.URL + "/html"},
		},
	}

	validated := validateAllEndpoints(context.Background(), s, validationOptions{})
	if len(validated) != 1 {
		t.Fatalf("expected 1 validated endpoint, got %d", len(validated))
	}
	if validated[0].Name != "good" {
		t.Errorf("expected 'good' endpoint, got %s", validated[0].Name)
	}
}

func TestValidateAllEndpoints_SortsByResponseSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/small":
			w.Write([]byte(`{"a":1}`))
		case "/large":
			large := map[string]any{"items": make([]int, 100)}
			json.NewEncoder(w).Encode(large)
		}
	}))
	defer srv.Close()

	s := &schema.Schema{
		Endpoints: []schema.Endpoint{
			{Name: "small", Method: "GET", URLTemplate: srv.URL + "/small"},
			{Name: "large", Method: "GET", URLTemplate: srv.URL + "/large"},
		},
	}

	validated := validateAllEndpoints(context.Background(), s, validationOptions{})
	if len(validated) != 2 {
		t.Fatalf("expected 2 validated endpoints, got %d", len(validated))
	}
	if validated[0].Name != "large" {
		t.Errorf("expected largest response first, got %s", validated[0].Name)
	}
}

func TestValidateAllEndpoints_Empty(t *testing.T) {
	s := &schema.Schema{Endpoints: nil}
	validated := validateAllEndpoints(context.Background(), s, validationOptions{})
	if len(validated) != 0 {
		t.Errorf("expected 0 validated endpoints, got %d", len(validated))
	}
}

func TestValidateAllEndpoints_InfersResponseSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"products": []any{
				map[string]any{"name": "Widget", "price": 9.99},
			},
			"total": 1,
		})
	}))
	defer srv.Close()

	s := &schema.Schema{
		Endpoints: []schema.Endpoint{
			{Name: "products", Method: "GET", URLTemplate: srv.URL + "/api"},
		},
	}

	validated := validateAllEndpoints(context.Background(), s, validationOptions{})
	if len(validated) != 1 {
		t.Fatalf("expected 1 validated endpoint, got %d", len(validated))
	}
	ep := validated[0]
	if ep.ResponseSchema == nil {
		t.Fatal("expected ResponseSchema to be inferred")
	}
	if ep.ResponseSchema.Type != "object" {
		t.Errorf("expected type=object, got %s", ep.ResponseSchema.Type)
	}
	if len(ep.ResponseSchema.Fields) != 2 {
		t.Errorf("expected 2 fields, got %d", len(ep.ResponseSchema.Fields))
	}
}

func TestValidateAllEndpoints_RequiresSemanticMatch(t *testing.T) {
	var baseURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"slug": "some-other-post",
				"link": baseURL + "/posts/some-other-post",
			},
		})
	}))
	defer srv.Close()
	baseURL = srv.URL

	s := &schema.Schema{
		DiscoveredFrom: baseURL + "/posts/target-post",
		Endpoints: []schema.Endpoint{
			{Name: "generic_feed", Method: "GET", URLTemplate: baseURL + "/wp-json/wp/v2/posts?per_page=1"},
		},
	}

	validated := validateAllEndpoints(context.Background(), s, validationOptions{requireSemanticMatch: true})
	if len(validated) != 0 {
		t.Fatalf("expected semantic mismatch to reject endpoint, got %d validated endpoints", len(validated))
	}
}

func TestResolveEndpointURL_RegexPattern(t *testing.T) {
	tests := []struct {
		name      string
		template  string
		targetURL string
		variables []schema.Variable
		want      string
	}{
		{
			name:      "regex pattern matches path segment",
			template:  "https://api.example.com/cases/{caseId}",
			targetURL: "https://example.com/opad/case/47BCV-25-249",
			variables: []schema.Variable{
				{Name: "caseId", Source: "path", Pattern: "[A-Z0-9]+-[0-9]+-[0-9]+"},
			},
			want: "https://api.example.com/cases/47BCV-25-249",
		},
		{
			name:      "numeric index pattern still works",
			template:  "https://api.example.com/repos/{owner}/{repo}",
			targetURL: "https://example.com/golang/go",
			variables: []schema.Variable{
				{Name: "owner", Source: "path", Pattern: "0"},
				{Name: "repo", Source: "path", Pattern: "1"},
			},
			want: "https://api.example.com/repos/golang/go",
		},
		{
			name:      "regex matches last segment first",
			template:  "https://api.example.com/item/{id}",
			targetURL: "https://example.com/items/category/12345",
			variables: []schema.Variable{
				{Name: "id", Source: "path", Pattern: "\\d+"},
			},
			want: "https://api.example.com/item/12345",
		},
		{
			name:      "query variable still works",
			template:  "https://api.example.com/item/{id}.json",
			targetURL: "https://example.com/item?id=42",
			variables: []schema.Variable{
				{Name: "id", Source: "query", Pattern: "id"},
			},
			want: "https://api.example.com/item/42.json",
		},
		{
			name:      "mixed regex and index patterns",
			template:  "https://api.example.com/{org}/cases/{caseId}",
			targetURL: "https://example.com/court/docket/ABC-123",
			variables: []schema.Variable{
				{Name: "org", Source: "path", Pattern: "0"},
				{Name: "caseId", Source: "path", Pattern: "[A-Z]+-\\d+"},
			},
			want: "https://api.example.com/court/cases/ABC-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveEndpointURL(tt.template, tt.targetURL, tt.variables)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindPrimaryEndpoint(t *testing.T) {
	s := &schema.Schema{
		Endpoints: []schema.Endpoint{
			{Name: "secondary", IsPrimary: false},
			{Name: "primary", IsPrimary: true},
		},
	}

	ep := findPrimaryEndpoint(s)
	if ep.Name != "primary" {
		t.Errorf("expected primary endpoint, got %s", ep.Name)
	}
}
