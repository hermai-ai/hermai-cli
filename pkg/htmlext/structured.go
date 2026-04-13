package htmlext

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
	"golang.org/x/net/html"
)

const maxStructuredBodyText = 2000

// ExtractStructured produces compact, agent-friendly output from HTML.
// It combines deterministic parsing (meta, OG, JSON-LD) with main content
// extraction, preferring JSON-LD as the richest structured data source.
func ExtractStructured(rawHTML string, baseURL string) map[string]any {
	page := Extract(rawHTML, baseURL)
	result := make(map[string]any)

	if page.Title != "" {
		result["title"] = page.Title
	}
	if page.Description != "" {
		result["description"] = page.Description
	}

	result["type"] = inferPageType(page)

	// __NEXT_DATA__ from Next.js SSR pages contains the richest structured
	// data — full page props, search results, metadata not in the DOM.
	// Prefer it over JSON-LD when available.
	if page.NextData != nil {
		nextDataResult := extractNextDataPayload(page.NextData)
		if len(nextDataResult) > 0 {
			for k, v := range nextDataResult {
				result[k] = v
			}
			surfacePagination(result, baseURL)
			return result
		}
	}

	// Embedded script patterns (ytInitialData, SIGI_STATE, __APOLLO_STATE__, etc.)
	// are SSR state blobs equivalent in richness to __NEXT_DATA__. When found,
	// include them directly — they contain the full page data.
	if len(page.EmbeddedScripts) > 0 {
		result["embedded_scripts"] = page.EmbeddedScripts
	}

	// JSON-LD is the next best structured data source. If present, merge it
	// directly into the result — it's already structured and standardized.
	if len(page.JSONLD) > 0 {
		mergeJSONLD(result, page.JSONLD)
	}

	// OG + meta enrich the result when no JSON-LD is present
	if len(page.JSONLD) == 0 && len(page.OpenGraph) > 0 {
		mergeOpenGraph(result, page.OpenGraph)
	}

	if page.Language != "" {
		result["language"] = page.Language
	}
	if page.Canonical != "" {
		result["url"] = page.Canonical
	}

	if page.BodyText != "" {
		bodyText := cleanBodyText(page.BodyText, maxStructuredBodyText)
		if bodyText != "" {
			result["body_text"] = bodyText
		}
	}

	return result
}

// ExtractionResult holds the extracted data and any missing required fields.
type ExtractionResult struct {
	Data           map[string]any
	MissingRequired []string // names of required fields that returned empty
}

// ApplyEntityExtraction applies entity field selectors to HTML and returns
// a compact map[string]any with the extracted data.
// Routes to the appropriate strategy (css_selector or table_rows).
func ApplyEntityExtraction(rawHTML string, rules *schema.ExtractionRules) map[string]any {
	result := ApplyEntityExtractionWithValidation(rawHTML, rules)
	return result.Data
}

// ApplyEntityExtractionWithValidation applies extraction rules and also
// reports which required fields are missing (for triggering re-discovery).
// Routes to the appropriate strategy based on rules.Strategy.
func ApplyEntityExtractionWithValidation(rawHTML string, rules *schema.ExtractionRules) ExtractionResult {
	// Route to table_rows strategy if configured
	if rules.Strategy == schema.StrategyTableRows && rules.TableRows != nil {
		return applyTableRowsWithValidation(rawHTML, rules)
	}

	return applyCSSEntityExtraction(rawHTML, rules)
}

// applyTableRowsWithValidation runs the table_rows extraction strategy.
func applyTableRowsWithValidation(rawHTML string, rules *schema.ExtractionRules) ExtractionResult {
	items := ApplyTableRowsExtraction(rawHTML, rules.TableRows)
	if len(items) == 0 {
		return ExtractionResult{}
	}

	data := make(map[string]any)

	// Add title if we have a title selector
	if rules.TitleSelector != "" {
		doc, err := html.Parse(strings.NewReader(rawHTML))
		if err == nil {
			if title := extractBySelector(doc, rules.TitleSelector); title != "" {
				data["title"] = title
			}
		}
	}

	if rules.PageType != "" {
		data["type"] = rules.PageType
	}

	// Use the content_selector name or "items" as the key
	listName := "items"
	if len(rules.EntityFields) > 0 {
		listName = rules.EntityFields[0].Name
	}
	data[listName] = items

	// Check for missing required fields in the first item
	var missingRequired []string
	if len(items) > 0 {
		for _, f := range rules.TableRows.Fields {
			if f.Required {
				if _, ok := items[0][f.Name]; !ok {
					missingRequired = append(missingRequired, f.Name)
				}
			}
		}
	}

	return ExtractionResult{
		Data:            data,
		MissingRequired: missingRequired,
	}
}

// applyCSSEntityExtraction is the original css_selector strategy.
func applyCSSEntityExtraction(rawHTML string, rules *schema.ExtractionRules) ExtractionResult {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return ExtractionResult{}
	}

	// Remove ignored elements first
	for _, sel := range rules.IgnoreSelectors {
		compiled, compileErr := cascadia.Compile(sel)
		if compileErr != nil {
			continue
		}
		removeNodes(doc, compiled)
	}

	data := make(map[string]any)

	// Extract title from title selector or fallback
	if rules.TitleSelector != "" {
		if title := extractBySelector(doc, rules.TitleSelector); title != "" {
			data["title"] = title
		}
	}
	if _, ok := data["title"]; !ok {
		if title := extractFallbackTitle(doc); title != "" {
			data["title"] = title
		}
	}

	// Apply page type
	if rules.PageType != "" {
		data["type"] = rules.PageType
	}

	// Extract entity fields and track missing required fields
	var missingRequired []string
	for _, field := range rules.EntityFields {
		val := extractEntityField(doc, field)
		if val != nil {
			data[field.Name] = val
		} else if field.Required {
			missingRequired = append(missingRequired, field.Name)
		}
	}

	return ExtractionResult{
		Data:            data,
		MissingRequired: missingRequired,
	}
}

// extractEntityField extracts a single entity field value from the DOM.
// Tries the primary Selector first, then each fallback in Selectors order.
func extractEntityField(doc *html.Node, field schema.EntityField) any {
	// Try primary selector
	if result := trySelector(doc, field.Selector, field); result != nil {
		return result
	}

	// Try fallback selectors in order
	for _, sel := range field.Selectors {
		if result := trySelector(doc, sel, field); result != nil {
			return result
		}
	}

	return nil
}

// trySelector compiles a selector and extracts the value based on field type.
func trySelector(doc *html.Node, selector string, field schema.EntityField) any {
	compiled, err := cascadia.Compile(selector)
	if err != nil {
		return nil
	}

	switch field.Type {
	case "list":
		return extractListField(doc, compiled, field)
	case "number":
		return extractNumberField(doc, compiled, field.Attribute)
	default: // "string" or empty
		return extractStringField(doc, compiled, field.Attribute)
	}
}

// extractStringField extracts text or attribute from the first matching node.
func extractStringField(doc *html.Node, sel cascadia.Selector, attribute string) any {
	node := cascadia.Query(doc, sel)
	if node == nil {
		return nil
	}
	if attribute != "" {
		val := attr(node, attribute)
		if val == "" {
			return nil
		}
		return val
	}
	text := strings.TrimSpace(textContent(node))
	if text == "" {
		return nil
	}
	return text
}

// extractNumberField extracts a numeric value from the first matching node.
func extractNumberField(doc *html.Node, sel cascadia.Selector, attribute string) any {
	node := cascadia.Query(doc, sel)
	if node == nil {
		return nil
	}
	var raw string
	if attribute != "" {
		raw = attr(node, attribute)
	} else {
		raw = strings.TrimSpace(textContent(node))
	}
	raw = cleanNumberString(raw)
	if raw == "" {
		return nil
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return f
	}
	return nil
}

// extractListField iterates over all matching nodes and extracts sub-fields.
func extractListField(doc *html.Node, sel cascadia.Selector, field schema.EntityField) any {
	nodes := cascadia.QueryAll(doc, sel)
	if len(nodes) == 0 {
		return nil
	}

	items := make([]any, 0, len(nodes))
	for _, node := range nodes {
		if len(field.ItemFields) > 0 {
			item := make(map[string]any)
			for _, sub := range field.ItemFields {
				val := extractEntityField(node, sub)
				if val != nil {
					item[sub.Name] = val
				}
			}
			if len(item) > 0 {
				items = append(items, item)
			}
		} else {
			// No sub-fields: extract text from each match
			text := strings.TrimSpace(textContent(node))
			if text != "" {
				items = append(items, text)
			}
		}
	}

	if len(items) == 0 {
		return nil
	}
	return items
}

// cleanNumberString strips common number formatting (commas, k/m suffixes,
// currency symbols) to produce a parseable numeric string.
func cleanNumberString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// Handle k/m suffixes (e.g., "1.2k" -> "1200")
	lower := strings.ToLower(s)
	if strings.HasSuffix(lower, "k") {
		numPart := strings.TrimSuffix(lower, "k")
		numPart = stripNonNumeric(numPart)
		if f, err := strconv.ParseFloat(numPart, 64); err == nil {
			return strconv.FormatInt(int64(f*1000), 10)
		}
	}
	if strings.HasSuffix(lower, "m") {
		numPart := strings.TrimSuffix(lower, "m")
		numPart = stripNonNumeric(numPart)
		if f, err := strconv.ParseFloat(numPart, 64); err == nil {
			return strconv.FormatInt(int64(f*1000000), 10)
		}
	}

	return stripNonNumeric(s)
}

// stripNonNumeric removes everything except digits, dots, and minus signs.
func stripNonNumeric(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func inferPageType(page PageContent) string {
	if ogType, ok := page.OpenGraph["type"]; ok {
		switch ogType {
		case "article":
			return "article"
		case "profile":
			return "profile"
		case "product":
			return "product"
		case "website":
			// "website" is the default OG type — need more signals
		}
	}

	if page.HasArticle {
		return "article"
	}

	for _, ld := range page.JSONLD {
		if m, ok := ld.(map[string]any); ok {
			if t, ok := m["@type"].(string); ok {
				return mapSchemaOrgType(t)
			}
		}
	}

	return "generic"
}

// mapSchemaOrgType maps Schema.org @type values to our page type categories.
func mapSchemaOrgType(schemaType string) string {
	lower := strings.ToLower(schemaType)
	switch {
	case strings.Contains(lower, "article") || strings.Contains(lower, "blogposting") || strings.Contains(lower, "newsarticle"):
		return "article"
	case strings.Contains(lower, "person") || strings.Contains(lower, "profilepage"):
		return "profile"
	case strings.Contains(lower, "product"):
		return "product"
	case lower == "itemlist" || lower == "searchresultspage":
		return "listing"
	case strings.Contains(lower, "technicalarticle") || strings.Contains(lower, "apireference"):
		return "documentation"
	default:
		return "generic"
	}
}

// mergeJSONLD merges JSON-LD structured data into the result map.
// It flattens the first JSON-LD object and adds selected fields.
func mergeJSONLD(result map[string]any, jsonLD []any) {
	if len(jsonLD) == 0 {
		return
	}

	// Use the first JSON-LD block as primary (often the most relevant)
	primary, ok := jsonLD[0].(map[string]any)
	if !ok {
		return
	}

	// Preserve select fields that are agent-useful
	preserveFields := []string{
		"@type", "name", "author", "datePublished", "dateModified",
		"description", "image", "url", "headline",
		"publisher", "mainEntityOfPage",
		"price", "priceCurrency", "brand", "sku", "availability",
		"aggregateRating", "offers",
		"jobTitle", "worksFor", "address",
	}

	for _, key := range preserveFields {
		if val, exists := primary[key]; exists {
			// Flatten author objects to strings when possible
			if key == "author" {
				if authorMap, isMap := val.(map[string]any); isMap {
					if name, hasName := authorMap["name"]; hasName {
						result["author"] = name
						continue
					}
				}
			}
			if key == "publisher" {
				if pubMap, isMap := val.(map[string]any); isMap {
					if name, hasName := pubMap["name"]; hasName {
						result["publisher"] = name
						continue
					}
				}
			}
			// Don't overwrite title/description if already set
			if key == "name" || key == "headline" {
				if _, hasTitle := result["title"]; hasTitle {
					continue
				}
			}
			if key == "description" {
				if _, hasDesc := result["description"]; hasDesc {
					continue
				}
			}
			result[key] = val
		}
	}

	// If multiple JSON-LD blocks, include additional @type info
	if len(jsonLD) > 1 {
		additional := make([]any, 0, len(jsonLD)-1)
		for _, ld := range jsonLD[1:] {
			if m, ok := ld.(map[string]any); ok {
				// Only include non-breadcrumb supplementary data
				if t, hasType := m["@type"].(string); hasType && t != "BreadcrumbList" {
					additional = append(additional, ld)
				}
			}
		}
		if len(additional) > 0 {
			result["additional_data"] = additional
		}
	}
}

// mergeOpenGraph merges OG metadata into the result, mapping to clean field names.
func mergeOpenGraph(result map[string]any, og map[string]string) {
	ogMapping := map[string]string{
		"site_name": "site_name",
		"image":     "image",
		"url":       "url",
	}

	for ogKey, resultKey := range ogMapping {
		if val, ok := og[ogKey]; ok {
			if _, exists := result[resultKey]; !exists {
				result[resultKey] = val
			}
		}
	}
}

// cleanBodyText collapses whitespace, removes duplicate lines,
// and truncates to maxLen. Produces readable text from raw extraction.
func cleanBodyText(raw string, maxLen int) string {
	// Collapse multiple newlines to single
	lines := strings.Split(raw, "\n")
	seen := make(map[string]bool)
	var cleaned []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Skip very short lines that are likely UI fragments
		if len(line) < 3 {
			continue
		}
		// Skip duplicate lines
		if seen[line] {
			continue
		}
		// Skip common noise phrases
		if isNoiseLine(line) {
			continue
		}
		seen[line] = true
		cleaned = append(cleaned, line)
	}

	result := strings.Join(cleaned, "\n")
	if len(result) > maxLen {
		result = result[:maxLen]
	}
	return strings.TrimSpace(result)
}

// isNoiseLine returns true for common non-content text patterns.
func isNoiseLine(line string) bool {
	lower := strings.ToLower(line)
	noisePatterns := []string{
		"sign in", "sign up", "log in", "log out",
		"cookie", "privacy policy", "terms of service",
		"accept all", "reject all", "manage cookies",
		"skip to content", "skip to main",
		"toggle navigation", "close menu",
		"block user", "report abuse",
		"learn more about blocking",
		"you must be logged in",
		"loading", "please wait",
	}
	for _, pattern := range noisePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// extractNextDataPayload extracts the meaningful content from a Next.js
// __NEXT_DATA__ blob. The standard structure is:
//
//	{props: {pageProps: { ...actual data... }}}
//
// We return pageProps directly — it contains the full SSR payload including
// search results, listings, metadata, and fields not present in the DOM.
func extractNextDataPayload(nextData any) map[string]any {
	root, ok := nextData.(map[string]any)
	if !ok {
		return nil
	}

	props, ok := root["props"].(map[string]any)
	if !ok {
		return nil
	}

	pageProps, ok := props["pageProps"].(map[string]any)
	if !ok {
		return nil
	}

	// pageProps must have meaningful content (not just error/redirect state)
	if len(pageProps) < 2 {
		return nil
	}

	// Zillow detail pages store rich property data in gdpClientCache,
	// a JSON string (not object) keyed by zpid. Parse and merge the
	// richest entry so agents get structured fields directly.
	if raw, ok := pageProps["gdpClientCache"]; ok {
		if jsonStr, isStr := raw.(string); isStr {
			var cache map[string]any
			if err := json.Unmarshal([]byte(jsonStr), &cache); err == nil {
				if richest := findRichestEntry(cache); richest != nil {
					merged := make(map[string]any, len(pageProps)+len(richest))
					for k, v := range pageProps {
						if k == "gdpClientCache" {
							continue
						}
						merged[k] = v
					}
					for k, v := range richest {
						merged[k] = v
					}
					return merged
				}
			}
		}
	}

	return pageProps
}

// findRichestEntry returns the map value with the most keys.
// Used for gdpClientCache where the key is a zpid and the value
// is the full property object.
func findRichestEntry(cache map[string]any) map[string]any {
	var best map[string]any
	bestCount := 0
	for _, v := range cache {
		if m, ok := v.(map[string]any); ok && len(m) > bestCount {
			best = m
			bestCount = len(m)
		}
	}
	return best
}

// surfacePagination detects pagination metadata in __NEXT_DATA__ responses
// and promotes it to a top-level `_pagination` key. Currently handles
// Zillow search results (searchPageState.cat1.searchList).
func surfacePagination(result map[string]any, baseURL string) {
	// Zillow path: searchPageState -> cat1 -> searchList
	searchPageState, _ := result["searchPageState"].(map[string]any)
	if searchPageState == nil {
		return
	}
	cat1, _ := searchPageState["cat1"].(map[string]any)
	if cat1 == nil {
		return
	}
	searchList, _ := cat1["searchList"].(map[string]any)
	if searchList == nil {
		return
	}

	pagination := map[string]any{}

	if total, ok := searchList["totalResultCount"].(float64); ok {
		pagination["total_results"] = int(total)
	}
	if pages, ok := searchList["totalPages"].(float64); ok {
		pagination["total_pages"] = int(pages)
	}

	// Extract nextUrl from pagination object
	if pag, ok := searchList["pagination"].(map[string]any); ok {
		if nextURL, ok := pag["nextUrl"].(string); ok && nextURL != "" {
			// Resolve relative URL
			if !strings.HasPrefix(nextURL, "http") {
				base, err := url.Parse(baseURL)
				if err == nil {
					ref, refErr := url.Parse(nextURL)
					if refErr == nil {
						nextURL = base.ResolveReference(ref).String()
					}
				}
			}
			pagination["next_url"] = nextURL
		}
	}

	if len(pagination) > 0 {
		result["_pagination"] = pagination
	}
}

// ApplyNextDataPaths extracts named sub-trees from a __NEXT_DATA__ pageProps
// map. Each key in paths is a human-readable alias, each value is a dot-path
// (e.g. ".initialZustandState.navigationData"). Returns a compact map keyed
// by alias. Paths that don't resolve are silently omitted. If ALL paths fail,
// returns nil so the caller can fall back to the full payload.
func ApplyNextDataPaths(pageProps map[string]any, paths map[string]string) map[string]any {
	result := make(map[string]any, len(paths))
	for alias, dotPath := range paths {
		if v := resolveDotPath(pageProps, dotPath); v != nil {
			result[alias] = v
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// resolveDotPath walks a nested map[string]any using a jq-style dot-path
// like ".foo.bar.baz" or ".items[0].name". Supports:
//   - object key traversal: .key1.key2
//   - array index: .items[0]
func resolveDotPath(data any, path string) any {
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return data
	}

	segments := strings.Split(path, ".")
	current := data

	for _, seg := range segments {
		if current == nil {
			return nil
		}

		// Check for array index: "key[N]"
		key, idx := seg, -1
		if bracket := strings.Index(seg, "["); bracket >= 0 {
			end := strings.Index(seg, "]")
			if end > bracket {
				if n, err := strconv.Atoi(seg[bracket+1 : end]); err == nil {
					idx = n
				}
				key = seg[:bracket]
			}
		}

		// Traverse into object
		if key != "" {
			m, ok := current.(map[string]any)
			if !ok {
				return nil
			}
			current = m[key]
		}

		// Traverse into array
		if idx >= 0 {
			arr, ok := current.([]any)
			if !ok || idx >= len(arr) {
				return nil
			}
			current = arr[idx]
		}
	}
	return current
}

// MarshalStructured converts a map[string]any to compact JSON bytes.
func MarshalStructured(data map[string]any) ([]byte, error) {
	return json.Marshal(data)
}
