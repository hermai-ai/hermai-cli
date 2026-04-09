package schema

import (
	"testing"
)

func TestMatchURL_ProductPattern(t *testing.T) {
	schemas := []Schema{
		{ID: "prod1", URLPattern: "/api/products/{}"},
		{ID: "search1", URLPattern: "/api/search"},
	}

	result := MatchURL("https://example.com/api/products/12345", schemas)
	if result == nil {
		t.Fatal("expected a match, got nil")
	}
	if result.ID != "prod1" {
		t.Errorf("expected prod1, got %s", result.ID)
	}
}

func TestMatchURL_SearchPattern(t *testing.T) {
	schemas := []Schema{
		{ID: "prod1", URLPattern: "/api/products/{}"},
		{ID: "search1", URLPattern: "/api/search"},
	}

	result := MatchURL("https://example.com/api/search?q=test", schemas)
	if result == nil {
		t.Fatal("expected a match, got nil")
	}
	if result.ID != "search1" {
		t.Errorf("expected search1, got %s", result.ID)
	}
}

func TestMatchURL_NoMatch(t *testing.T) {
	schemas := []Schema{
		{ID: "prod1", URLPattern: "/api/products/{}"},
		{ID: "search1", URLPattern: "/api/search"},
	}

	result := MatchURL("https://example.com/api/users/123", schemas)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestMatchURL_MostSpecificWins(t *testing.T) {
	schemas := []Schema{
		{ID: "generic", URLPattern: "/{}/{}"},
		{ID: "specific", URLPattern: "/product/{}"},
	}

	result := MatchURL("https://example.com/product/12345", schemas)
	if result == nil {
		t.Fatal("expected a match, got nil")
	}
	if result.ID != "specific" {
		t.Errorf("expected specific, got %s", result.ID)
	}
}

func TestMatchURL_EmptySchemas(t *testing.T) {
	result := MatchURL("https://example.com/api/products/123", nil)
	if result != nil {
		t.Errorf("expected nil for empty schemas, got %v", result)
	}
}

func TestPatternMatches_ExactMatch(t *testing.T) {
	if !patternMatches("/api/search", "/api/search") {
		t.Error("expected exact match to succeed")
	}
}

func TestPatternMatches_WildcardMatch(t *testing.T) {
	if !patternMatches("/api/products/{}", "/api/products/{}") {
		t.Error("expected wildcard pattern to match normalized path with wildcard")
	}
}

func TestPatternMatches_WildcardMatchesAnything(t *testing.T) {
	if !patternMatches("/api/products/shoes", "/api/products/{}") {
		t.Error("expected wildcard to match any segment")
	}
}

func TestPatternMatches_NoMatch(t *testing.T) {
	if patternMatches("/api/users/{}", "/api/products/{}") {
		t.Error("expected different static segments to not match")
	}
}

func TestPatternMatches_DifferentLengths(t *testing.T) {
	if patternMatches("/api/products", "/api/products/{}") {
		t.Error("expected different segment counts to not match")
	}
}

func TestSpecificity_AllStatic(t *testing.T) {
	score := specificity("/api/v2/products")
	// 3 static segments * 10 = 30
	if score != 30 {
		t.Errorf("expected 30, got %d", score)
	}
}

func TestSpecificity_MixedSegments(t *testing.T) {
	score := specificity("/api/products/{}")
	// 2 static * 10 + 1 wildcard * 1 = 21
	if score != 21 {
		t.Errorf("expected 21, got %d", score)
	}
}

func TestSpecificity_AllWildcard(t *testing.T) {
	score := specificity("/{}/{}")
	// 2 wildcards * 1 = 2
	if score != 2 {
		t.Errorf("expected 2, got %d", score)
	}
}

func TestSpecificity_SingleSegment(t *testing.T) {
	score := specificity("/api")
	if score != 10 {
		t.Errorf("expected 10, got %d", score)
	}
}
