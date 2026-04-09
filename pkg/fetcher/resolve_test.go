package fetcher

import (
	"testing"

	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

func TestResolveURL(t *testing.T) {
	tests := []struct {
		name      string
		template  string
		targetURL string
		variables []schema.Variable
		expected  string
		wantErr   bool
	}{
		{
			name:      "path variable by segment index",
			template:  "https://api.example.com/products/{productId}",
			targetURL: "https://example.com/shop/products/42",
			variables: []schema.Variable{
				{Name: "productId", Source: "path", Pattern: "2"},
			},
			expected: "https://api.example.com/products/42",
		},
		{
			name:      "path variable first segment",
			template:  "https://api.example.com/users/{userId}/profile",
			targetURL: "https://example.com/users/john-doe/settings",
			variables: []schema.Variable{
				{Name: "userId", Source: "path", Pattern: "1"},
			},
			expected: "https://api.example.com/users/john-doe/profile",
		},
		{
			name:      "query variable",
			template:  "https://api.example.com/search?q={searchTerm}",
			targetURL: "https://example.com/search?q=golang&page=2",
			variables: []schema.Variable{
				{Name: "searchTerm", Source: "query", Pattern: "q"},
			},
			expected: "https://api.example.com/search?q=golang",
		},
		{
			name:      "fixed variable",
			template:  "https://api.example.com/data?key={apiKey}",
			targetURL: "https://example.com/anything",
			variables: []schema.Variable{
				{Name: "apiKey", Source: "fixed", Pattern: "my-secret-key"},
			},
			expected: "https://api.example.com/data?key=my-secret-key",
		},
		{
			name:      "multiple variables mixed sources",
			template:  "https://api.example.com/users/{userId}/posts?category={cat}&token={token}",
			targetURL: "https://example.com/users/99/posts?cat=tech&sort=new",
			variables: []schema.Variable{
				{Name: "userId", Source: "path", Pattern: "1"},
				{Name: "cat", Source: "query", Pattern: "cat"},
				{Name: "token", Source: "fixed", Pattern: "abc123"},
			},
			expected: "https://api.example.com/users/99/posts?category=tech&token=abc123",
		},
		{
			name:      "path variable out of bounds",
			template:  "https://api.example.com/{val}",
			targetURL: "https://example.com/one",
			variables: []schema.Variable{
				{Name: "val", Source: "path", Pattern: "5"},
			},
			wantErr: true,
		},
		{
			name:      "path variable non-numeric pattern",
			template:  "https://api.example.com/{val}",
			targetURL: "https://example.com/one",
			variables: []schema.Variable{
				{Name: "val", Source: "path", Pattern: "notanumber"},
			},
			wantErr: true,
		},
		{
			name:      "no variables",
			template:  "https://api.example.com/static/endpoint",
			targetURL: "https://example.com/anything",
			variables: nil,
			expected:  "https://api.example.com/static/endpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveURL(tt.template, tt.targetURL, tt.variables)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestResolveVariable_URLSource(t *testing.T) {
	v := schema.Variable{Name: "id", Source: "url", Pattern: `\d+`}
	result, err := resolveVariable(v, "https://example.com/api/v2/pokemon/25")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "25" {
		t.Errorf("expected '25', got %q", result)
	}
}

func TestResolveURL_PathRest(t *testing.T) {
	tests := []struct {
		name      string
		template  string
		targetURL string
		variables []schema.Variable
		expected  string
		wantErr   bool
	}{
		{
			name:      "github tree multi-segment path",
			template:  "https://api.github.com/repos/{owner}/{repo}/contents/{path}?ref={branch}",
			targetURL: "https://github.com/hermai-ai/hermai-api/tree/main/pkg/probe/known_sites.go",
			variables: []schema.Variable{
				{Name: "owner", Source: "path", Pattern: "0"},
				{Name: "repo", Source: "path", Pattern: "1"},
				{Name: "branch", Source: "path", Pattern: "3"},
				{Name: "path", Source: "path_rest", Pattern: "4"},
			},
			expected: "https://api.github.com/repos/hermai-ai/hermai-api/contents/pkg/probe/known_sites.go?ref=main",
		},
		{
			name:      "github blob single file",
			template:  "https://api.github.com/repos/{owner}/{repo}/contents/{path}?ref={branch}",
			targetURL: "https://github.com/golang/go/blob/master/README.md",
			variables: []schema.Variable{
				{Name: "owner", Source: "path", Pattern: "0"},
				{Name: "repo", Source: "path", Pattern: "1"},
				{Name: "branch", Source: "path", Pattern: "3"},
				{Name: "path", Source: "path_rest", Pattern: "4"},
			},
			expected: "https://api.github.com/repos/golang/go/contents/README.md?ref=master",
		},
		{
			name:      "path_rest out of bounds",
			template:  "https://api.example.com/{rest}",
			targetURL: "https://example.com/one",
			variables: []schema.Variable{
				{Name: "rest", Source: "path_rest", Pattern: "5"},
			},
			wantErr: true,
		},
		{
			name:      "path_rest non-numeric pattern",
			template:  "https://api.example.com/{rest}",
			targetURL: "https://example.com/one/two",
			variables: []schema.Variable{
				{Name: "rest", Source: "path_rest", Pattern: "notanumber"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveURL(tt.template, tt.targetURL, tt.variables)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestResolveVariable_UnknownSource(t *testing.T) {
	v := schema.Variable{Name: "id", Source: "magic", Pattern: "test"}
	_, err := resolveVariable(v, "https://example.com/test")
	if err == nil {
		t.Error("expected error for unknown source")
	}
}
