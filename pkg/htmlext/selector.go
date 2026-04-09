package htmlext

import (
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
	"golang.org/x/net/html"
)

// CleanContent is the structured output from CSS selector-based extraction.
type CleanContent struct {
	Title   string            `json:"title"`
	Author  string            `json:"author,omitempty"`
	Date    string            `json:"date,omitempty"`
	Content string            `json:"content"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// ErrNoContentMatch signals that the content selector matched nothing,
// indicating the cached rules are stale (site redesign).
var ErrNoContentMatch = strings.NewReader("") // sentinel, compared by identity

// ExtractWithRules applies cached CSS selectors to extract clean content.
// Returns nil CleanContent if the content_selector matches nothing.
func ExtractWithRules(rawHTML string, baseURL string, rules *schema.ExtractionRules) (*CleanContent, error) {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return nil, err
	}

	// Remove ignored elements from the tree
	for _, sel := range rules.IgnoreSelectors {
		compiled, compileErr := cascadia.Compile(sel)
		if compileErr != nil {
			continue
		}
		removeNodes(doc, compiled)
	}

	// Extract main content
	content := extractBySelector(doc, rules.ContentSelector)
	if content == "" {
		return nil, nil // signals stale rules
	}

	result := &CleanContent{
		Content: collapseWhitespace(content),
	}

	// Extract optional structured fields
	if rules.TitleSelector != "" {
		result.Title = extractBySelector(doc, rules.TitleSelector)
	}
	if result.Title == "" {
		result.Title = extractFallbackTitle(doc)
	}

	if rules.AuthorSelector != "" {
		result.Author = extractBySelector(doc, rules.AuthorSelector)
	}
	if rules.DateSelector != "" {
		result.Date = extractBySelector(doc, rules.DateSelector)
	}

	if len(rules.Fields) > 0 {
		result.Fields = make(map[string]string, len(rules.Fields))
		for _, f := range rules.Fields {
			val := extractFieldBySelector(doc, f.Selector, f.Attribute)
			if val != "" {
				result.Fields[f.Name] = val
			}
		}
	}

	return result, nil
}

// extractBySelector compiles a CSS selector and returns the text content
// of the first matching node.
func extractBySelector(doc *html.Node, selector string) string {
	compiled, err := cascadia.Compile(selector)
	if err != nil {
		return ""
	}
	node := cascadia.Query(doc, compiled)
	if node == nil {
		return ""
	}
	return strings.TrimSpace(textContent(node))
}

// extractFieldBySelector extracts either text content or an attribute value.
func extractFieldBySelector(doc *html.Node, selector string, attribute string) string {
	compiled, err := cascadia.Compile(selector)
	if err != nil {
		return ""
	}
	node := cascadia.Query(doc, compiled)
	if node == nil {
		return ""
	}
	if attribute != "" {
		return attr(node, attribute)
	}
	return strings.TrimSpace(textContent(node))
}

// extractFallbackTitle gets the <title> tag content as fallback.
func extractFallbackTitle(doc *html.Node) string {
	sel, err := cascadia.Compile("title")
	if err != nil {
		return ""
	}
	node := cascadia.Query(doc, sel)
	if node == nil {
		return ""
	}
	return strings.TrimSpace(textContent(node))
}

// removeNodes finds all nodes matching the selector and removes them from the tree.
func removeNodes(doc *html.Node, sel cascadia.Selector) {
	matches := cascadia.QueryAll(doc, sel)
	for _, m := range matches {
		if m.Parent != nil {
			m.Parent.RemoveChild(m)
		}
	}
}

// ValidateSelectors checks that all selectors in the rules compile with cascadia.
func ValidateSelectors(rules *schema.ExtractionRules) error {
	if _, err := cascadia.Compile(rules.ContentSelector); err != nil {
		return err
	}
	selectors := []string{rules.TitleSelector, rules.AuthorSelector, rules.DateSelector}
	for _, s := range selectors {
		if s != "" {
			if _, err := cascadia.Compile(s); err != nil {
				return err
			}
		}
	}
	for _, s := range rules.IgnoreSelectors {
		if _, err := cascadia.Compile(s); err != nil {
			return err
		}
	}
	for _, f := range rules.Fields {
		if _, err := cascadia.Compile(f.Selector); err != nil {
			return err
		}
	}
	for _, ef := range rules.EntityFields {
		if err := validateEntityField(ef); err != nil {
			return err
		}
	}
	return nil
}

// validateEntityField recursively validates entity field selectors,
// including the ordered fallback Selectors list.
func validateEntityField(ef schema.EntityField) error {
	if _, err := cascadia.Compile(ef.Selector); err != nil {
		return err
	}
	for _, sel := range ef.Selectors {
		if _, err := cascadia.Compile(sel); err != nil {
			return err
		}
	}
	for _, sub := range ef.ItemFields {
		if err := validateEntityField(sub); err != nil {
			return err
		}
	}
	return nil
}
