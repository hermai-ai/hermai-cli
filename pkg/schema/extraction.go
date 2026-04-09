package schema

import (
	"net/url"
	"strings"
	"time"
)

// Extraction strategy constants.
const (
	StrategyCSS       = "css_selector" // default: all fields in one container
	StrategyTableRows = "table_rows"   // table layout: one item spans N <tr> rows
	StrategyNextData  = "next_data"    // Next.js __NEXT_DATA__ SSR payload
)

// ExtractionRules defines CSS selectors for extracting structured content
// from HTML pages. These rules are discovered by the LLM on first visit
// and cached so subsequent visits skip the LLM entirely.
type ExtractionRules struct {
	Strategy        string            `json:"strategy,omitempty"`          // "css_selector" (default) or "table_rows"
	PageType        string            `json:"page_type"`                   // "article", "profile", "product", "listing", "documentation", "generic"
	ContentSelector string            `json:"content_selector"`
	TitleSelector   string            `json:"title_selector,omitempty"`
	AuthorSelector  string            `json:"author_selector,omitempty"`
	DateSelector    string            `json:"date_selector,omitempty"`
	IgnoreSelectors []string          `json:"ignore_selectors,omitempty"`
	Fields          []ExtractionField `json:"fields,omitempty"`
	EntityFields    []EntityField     `json:"entity_fields,omitempty"`     // for css_selector strategy
	TableRows       *TableRowsConfig  `json:"table_rows,omitempty"`        // for table_rows strategy
	NextDataPaths   map[string]string `json:"next_data_paths,omitempty"`   // named jq-style paths into __NEXT_DATA__ pageProps
}

// TableRowsConfig defines extraction for table-based layouts where one
// logical item spans multiple consecutive <tr> rows.
type TableRowsConfig struct {
	Container string          `json:"container"`  // CSS selector for the <table> or <tbody>
	GroupSize int             `json:"group_size"`  // number of <tr> rows per logical item
	Fields    []TableRowField `json:"fields"`      // which row + selector for each field
}

// TableRowField extracts a value from a specific row within a row group.
type TableRowField struct {
	Name      string `json:"name"`
	Row       int    `json:"row"`                    // 0-indexed row within the group
	Selector  string `json:"selector"`
	Attribute string `json:"attribute,omitempty"`     // empty = text content
	Type      string `json:"type,omitempty"`          // "string" (default), "number"
	Required  bool   `json:"required,omitempty"`
}

// ExtractionField represents a named piece of data to extract using a CSS selector.
type ExtractionField struct {
	Name      string `json:"name"`
	Selector  string `json:"selector"`
	Attribute string `json:"attribute,omitempty"` // empty = text content
}

// EntityField extracts a named piece of data with type information.
type EntityField struct {
	Name       string        `json:"name"`
	Selector   string        `json:"selector"`
	Selectors  []string      `json:"selectors,omitempty"`   // ordered fallback selectors
	Attribute  string        `json:"attribute,omitempty"`   // empty = text content
	Type       string        `json:"type"`                  // "string", "number", "list"
	Required   bool          `json:"required,omitempty"`    // triggers re-discovery if empty
	ItemFields []EntityField `json:"item_fields,omitempty"` // for list items
}

// IsHTMLExtraction returns true if this schema uses HTML content
// extraction (CSS selectors, table rows, or __NEXT_DATA__) rather
// than API endpoints.
func (s Schema) IsHTMLExtraction() bool {
	return s.ExtractionRules != nil
}

// IsNextDataSchema returns true if this schema uses __NEXT_DATA__
// extraction from Next.js SSR pages.
func (s Schema) IsNextDataSchema() bool {
	return s.ExtractionRules != nil && s.ExtractionRules.Strategy == StrategyNextData
}

// FromNextData creates a Schema recording that a URL uses Next.js __NEXT_DATA__
// for structured data. The extraction is deterministic — no CSS selectors or
// LLM analysis needed — so only the strategy is stored.
func FromNextData(targetURL string) *Schema {
	return FromNextDataWithPaths(targetURL, nil)
}

// FromNextDataWithPaths creates a __NEXT_DATA__ schema with named extraction
// paths. Each key in paths is a human-readable alias (e.g. "products"), and
// each value is a dot-path into pageProps (e.g. ".ssrQuery.hits").
// When paths are present, extraction returns only the targeted sub-trees
// instead of the full pageProps blob.
//
// URL pattern: Next.js pages under the same section share identical
// __NEXT_DATA__ key structures, so we wildcard all segments after the
// first one. E.g. /collections/all-products/mens → /collections/{}/{}.
// This lets one cached schema serve /collections/shorts/mens,
// /collections/all/womens, etc.
func FromNextDataWithPaths(targetURL string, paths map[string]string) *Schema {
	domain, _ := ExtractDomain(targetURL)

	path := "/"
	if parsed, err := url.Parse(targetURL); err == nil {
		path = parsed.Path
	}

	pattern := normalizeNextDataPattern(path)

	rules := &ExtractionRules{
		Strategy: StrategyNextData,
		PageType: "next_data",
	}
	if len(paths) > 0 {
		rules.NextDataPaths = paths
	}

	return &Schema{
		ID:              GenerateID(domain, pattern),
		Domain:          domain,
		URLPattern:      pattern,
		SchemaType:      SchemaTypeCSS,
		Version:         1,
		CreatedAt:       time.Now(),
		DiscoveredFrom:  targetURL,
		ExtractionRules: rules,
	}
}

// normalizeNextDataPattern creates a broad URL pattern for __NEXT_DATA__
// schemas. Keeps the first path segment (section type) static and wildcards
// the rest, since Next.js pages in the same section share the same
// pageProps key structure.
//
//	/collections/all-products/mens  → /collections/{}/{}
//	/product/abc123                 → /product/{}
//	/                               → /
func normalizeNextDataPattern(path string) string {
	path = strings.SplitN(path, "?", 2)[0]
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) <= 1 {
		return NormalizePathStructure(path)
	}
	// Keep first segment, wildcard the rest
	result := make([]string, len(segments))
	result[0] = segments[0]
	for i := 1; i < len(segments); i++ {
		result[i] = "{}"
	}
	return "/" + strings.Join(result, "/")
}

// HasNextDataPaths returns true if this extraction has named paths
// for targeted __NEXT_DATA__ sub-tree extraction.
func (r *ExtractionRules) HasNextDataPaths() bool {
	return len(r.NextDataPaths) > 0
}

// FromExtractionRules creates a Schema from extraction rules and a target URL.
// The schema ID is generated from the domain and URL path pattern.
func FromExtractionRules(targetURL string, rules *ExtractionRules) *Schema {
	domain, _ := ExtractDomain(targetURL)

	path := "/"
	if parsed, err := url.Parse(targetURL); err == nil {
		path = parsed.Path
	}

	return &Schema{
		ID:              GenerateID(domain, path),
		Domain:          domain,
		URLPattern:      NormalizePathStructure(path),
		SchemaType:      SchemaTypeCSS,
		Version:         1,
		CreatedAt:       time.Now(),
		DiscoveredFrom:  targetURL,
		ExtractionRules: rules,
	}
}
