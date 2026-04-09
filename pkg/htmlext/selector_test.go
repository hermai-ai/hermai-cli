package htmlext

import (
	"testing"

	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

const testHTML = `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
	<nav><a href="/">Home</a><a href="/about">About</a></nav>
	<main>
		<article class="post">
			<h1 class="post-title">Hello World</h1>
			<span class="author">John Doe</span>
			<time class="date">2026-01-15</time>
			<div class="post-body">
				<p>This is the main content of the article.</p>
				<p>It has multiple paragraphs with useful information.</p>
			</div>
			<span class="price" data-amount="29.99">$29.99</span>
		</article>
	</main>
	<footer>Copyright 2026</footer>
</body>
</html>`

func TestExtractWithRules_Basic(t *testing.T) {
	rules := &schema.ExtractionRules{
		ContentSelector: "article.post",
		TitleSelector:   "h1.post-title",
		AuthorSelector:  ".author",
		DateSelector:    "time.date",
		IgnoreSelectors: []string{"nav", "footer"},
	}

	result, err := ExtractWithRules(testHTML, "https://example.com", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Title != "Hello World" {
		t.Errorf("title: got %q, want %q", result.Title, "Hello World")
	}
	if result.Author != "John Doe" {
		t.Errorf("author: got %q, want %q", result.Author, "John Doe")
	}
	if result.Date != "2026-01-15" {
		t.Errorf("date: got %q, want %q", result.Date, "2026-01-15")
	}
	if result.Content == "" {
		t.Error("content should not be empty")
	}
	if !contains(result.Content, "main content of the article") {
		t.Errorf("content missing article text: %q", result.Content)
	}
	// Nav and footer should be removed
	if contains(result.Content, "Home") {
		t.Error("content should not contain nav text")
	}
	if contains(result.Content, "Copyright") {
		t.Error("content should not contain footer text")
	}
}

func TestExtractWithRules_CustomFields(t *testing.T) {
	rules := &schema.ExtractionRules{
		ContentSelector: "article.post",
		Fields: []schema.ExtractionField{
			{Name: "price_text", Selector: ".price"},
			{Name: "price_amount", Selector: ".price", Attribute: "data-amount"},
		},
	}

	result, err := ExtractWithRules(testHTML, "https://example.com", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Fields["price_text"] != "$29.99" {
		t.Errorf("price_text: got %q", result.Fields["price_text"])
	}
	if result.Fields["price_amount"] != "29.99" {
		t.Errorf("price_amount: got %q", result.Fields["price_amount"])
	}
}

func TestExtractWithRules_NoMatch(t *testing.T) {
	rules := &schema.ExtractionRules{
		ContentSelector: "div.nonexistent-class",
	}

	result, err := ExtractWithRules(testHTML, "https://example.com", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result when content selector matches nothing")
	}
}

func TestExtractWithRules_FallbackTitle(t *testing.T) {
	rules := &schema.ExtractionRules{
		ContentSelector: "article.post",
		TitleSelector:   "h2.nonexistent", // won't match
	}

	result, err := ExtractWithRules(testHTML, "https://example.com", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Title != "Test Page" {
		t.Errorf("fallback title: got %q, want %q", result.Title, "Test Page")
	}
}

func TestExtractWithRules_InvalidSelector(t *testing.T) {
	rules := &schema.ExtractionRules{
		ContentSelector: "[[[invalid",
	}

	result, err := ExtractWithRules(testHTML, "https://example.com", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result for invalid selector")
	}
}

func TestValidateSelectors(t *testing.T) {
	valid := &schema.ExtractionRules{
		ContentSelector: "article.post",
		TitleSelector:   "h1",
		IgnoreSelectors: []string{"nav", "footer"},
	}
	if err := ValidateSelectors(valid); err != nil {
		t.Errorf("expected valid, got error: %v", err)
	}

	invalid := &schema.ExtractionRules{
		ContentSelector: "[[[bad",
	}
	if err := ValidateSelectors(invalid); err == nil {
		t.Error("expected error for invalid selector")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && stringContains(s, substr)
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
