package htmlext

import (
	"strconv"
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
	"golang.org/x/net/html"
)

// ApplyTableRowsExtraction extracts structured data from table-based layouts
// where one logical item spans multiple consecutive <tr> rows.
//
// For example, Hacker News uses 3 <tr> rows per story:
//   - Row 0: rank, title, URL
//   - Row 1: score, author, time, comments
//   - Row 2: spacer
//
// The config specifies group_size=3 and maps each field to its row index.
func ApplyTableRowsExtraction(rawHTML string, cfg *schema.TableRowsConfig) []map[string]any {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return nil
	}

	// Find the container element
	containerSel, err := cascadia.Compile(cfg.Container)
	if err != nil {
		return nil
	}
	container := cascadia.Query(doc, containerSel)
	if container == nil {
		return nil
	}

	// Collect all direct <tr> children of the container (or its <tbody>)
	rows := collectTRs(container)
	if len(rows) < cfg.GroupSize {
		return nil
	}

	// Group rows and extract fields from each group
	var items []map[string]any
	for i := 0; i+cfg.GroupSize <= len(rows); i += cfg.GroupSize {
		group := rows[i : i+cfg.GroupSize]
		item := extractFromRowGroup(group, cfg.Fields)
		if len(item) > 0 {
			items = append(items, item)
		}
	}

	return items
}

// collectTRs finds all <tr> elements that are direct children of the node
// or its first <tbody> child.
func collectTRs(container *html.Node) []*html.Node {
	// Check if there's a tbody first
	target := container
	for c := container.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "tbody" {
			target = c
			break
		}
	}

	var rows []*html.Node
	for c := target.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "tr" {
			rows = append(rows, c)
		}
	}
	return rows
}

// extractFromRowGroup extracts all fields from a group of <tr> rows.
func extractFromRowGroup(rows []*html.Node, fields []schema.TableRowField) map[string]any {
	item := make(map[string]any)

	for _, f := range fields {
		if f.Row < 0 || f.Row >= len(rows) {
			continue
		}
		row := rows[f.Row]

		val := extractFieldFromNode(row, f.Selector, f.Attribute)
		if val == "" {
			continue
		}

		switch f.Type {
		case "number":
			cleaned := cleanNumberString(val)
			if n, err := strconv.ParseInt(cleaned, 10, 64); err == nil {
				item[f.Name] = n
			} else if fv, err := strconv.ParseFloat(cleaned, 64); err == nil {
				item[f.Name] = fv
			}
		default:
			item[f.Name] = val
		}
	}

	return item
}

// extractFieldFromNode applies a CSS selector within a specific DOM node
// and returns the text content or attribute value.
func extractFieldFromNode(node *html.Node, selector, attribute string) string {
	if selector == "" {
		// No selector — extract from the node itself
		if attribute != "" {
			return attr(node, attribute)
		}
		return strings.TrimSpace(textContent(node))
	}

	compiled, err := cascadia.Compile(selector)
	if err != nil {
		return ""
	}
	match := cascadia.Query(node, compiled)
	if match == nil {
		return ""
	}
	if attribute != "" {
		return attr(match, attribute)
	}
	return strings.TrimSpace(textContent(match))
}
