package schema

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGenerateID_SameDomainAndPath_SameID(t *testing.T) {
	id1 := GenerateID("example.com", "/api/products/123")
	id2 := GenerateID("example.com", "/api/products/456")

	if id1 != id2 {
		t.Errorf("expected same ID for same domain+path structure, got %s and %s", id1, id2)
	}
}

func TestGenerateID_DifferentDomains_DifferentIDs(t *testing.T) {
	id1 := GenerateID("example.com", "/api/products/123")
	id2 := GenerateID("other.com", "/api/products/123")

	if id1 == id2 {
		t.Errorf("expected different IDs for different domains, both got %s", id1)
	}
}

func TestGenerateID_DifferentPaths_DifferentIDs(t *testing.T) {
	id1 := GenerateID("example.com", "/api/products/123")
	id2 := GenerateID("example.com", "/api/users/123")

	if id1 == id2 {
		t.Errorf("expected different IDs for different path structures, both got %s", id1)
	}
}

func TestGenerateID_Length(t *testing.T) {
	id := GenerateID("example.com", "/api/products/123")

	// 8 bytes hex-encoded = 16 characters
	if len(id) != 16 {
		t.Errorf("expected ID length 16, got %d (%s)", len(id), id)
	}
}

func TestNormalizePathStructure_NumericSegments(t *testing.T) {
	result := NormalizePathStructure("/api/products/12345")
	expected := "/api/products/{}"

	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestNormalizePathStructure_UUIDSegments(t *testing.T) {
	result := NormalizePathStructure("/api/users/550e8400-e29b-41d4-a716-446655440000")
	expected := "/api/users/{}"

	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestNormalizePathStructure_LongAlphanumericSegments(t *testing.T) {
	result := NormalizePathStructure("/api/items/abc123def456ghi789jkl")
	expected := "/api/items/{}"

	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestNormalizePathStructure_PreservesStaticSegments(t *testing.T) {
	result := NormalizePathStructure("/api/v2/products/list")
	expected := "/api/v2/products/list"

	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestNormalizePathStructure_StripsQueryStrings(t *testing.T) {
	result := NormalizePathStructure("/api/search?q=test&page=1")
	expected := "/api/search"

	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestNormalizePathStructure_MultipleWildcardSegments(t *testing.T) {
	result := NormalizePathStructure("/api/users/12345/orders/67890")
	expected := "/api/users/{}/orders/{}"

	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestNormalizePathStructure_EmptyPath(t *testing.T) {
	result := NormalizePathStructure("/")
	expected := "/"

	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestExtractDomain_ValidURL(t *testing.T) {
	domain, err := ExtractDomain("https://www.example.com/api/products")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if domain != "www.example.com" {
		t.Errorf("expected www.example.com, got %s", domain)
	}
}

func TestExtractDomain_WithPort(t *testing.T) {
	domain, err := ExtractDomain("http://localhost:8080/api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if domain != "localhost:8080" {
		t.Errorf("expected localhost:8080, got %s", domain)
	}
}

func TestExtractDomain_InvalidURL(t *testing.T) {
	_, err := ExtractDomain("not-a-valid-url")
	if err == nil {
		t.Error("expected error for invalid URL, got nil")
	}
}

func TestSchema_JSONRoundTrip(t *testing.T) {
	original := Schema{
		ID:             "abcdef0123456789",
		Domain:         "example.com",
		URLPattern:     "/api/products/{}",
		Version:        1,
		CreatedAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		DiscoveredFrom: "https://example.com/products",
		Endpoints: []Endpoint{
			{
				Name:        "GetProduct",
				Method:      "GET",
				URLTemplate: "https://example.com/api/products/${id}",
				Headers: map[string]string{
					"Accept": "application/json",
				},
				QueryParams: []Param{
					{Key: "format", Value: "json", Required: false},
				},
				Variables: []Variable{
					{Name: "id", Source: "path", Pattern: `\d+`},
				},
				IsPrimary: true,
				ResponseMapping: map[string]string{
					"title": "data.product.title",
				},
			},
		},
	}

	data, err := original.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	restored, err := FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON failed: %v", err)
	}

	// Re-serialize to compare
	restoredData, err := restored.ToJSON()
	if err != nil {
		t.Fatalf("re-serialize failed: %v", err)
	}

	if string(data) != string(restoredData) {
		t.Errorf("JSON round-trip mismatch:\noriginal:  %s\nrestored: %s", string(data), string(restoredData))
	}
}

func TestSchema_ToJSON_ValidJSON(t *testing.T) {
	s := Schema{
		ID:         "test123456789012",
		Domain:     "example.com",
		URLPattern: "/api/test",
		Version:    1,
		Endpoints:  []Endpoint{},
	}

	data, err := s.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Errorf("ToJSON produced invalid JSON: %v", err)
	}
}

func TestFromJSON_InvalidJSON(t *testing.T) {
	_, err := FromJSON([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}
