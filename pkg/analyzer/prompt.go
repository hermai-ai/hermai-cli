package analyzer

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hermai-ai/hermai-cli/pkg/browser"
)

const maxResponseValueLen = 80
const maxDOMLen = 3000
const maxResponseBodyLen = 2000 // cap truncated response body per entry
const maxHAREntries = 25        // cap entries sent to LLM

// SystemPrompt returns the LLM system prompt that instructs the model
// to analyze captured HAR traffic and identify API endpoints.
func SystemPrompt() string {
	return `You are an API traffic classifier. You analyze captured HTTP traffic (HAR entries) from a web page and classify each entry as either NOISE or CANDIDATE.

Your task is NOT to pick the best endpoint. Your task is to CLASSIFY:
- NOISE: analytics, tracking, ads, config, health checks, logging, telemetry, error reporting
- CANDIDATE: anything that returns actual page data (content, listings, scores, articles, products)

List ALL candidates. Do not exclude entries you are unsure about — include them. The system will validate which ones actually work. It is better to include too many than to miss one.

Respond with a JSON object:
{
  "endpoints": [
    {
      "name": "descriptive name of what this endpoint returns",
      "method": "GET|POST",
      "url_template": "https://api.example.com/path/{variable}",
      "headers": {"Header-Name": "value"},
      "query_params": [{"key": "param", "value": "value", "required": true}],
      "body": {"content_type": "application/json", "template": "..."},
      "variables": [{"name": "variable", "source": "url|path|query", "pattern": "regex"}],
      "is_primary": false,
      "response_mapping": {}
    }
  ],
  "session": {
    "bootstrap_url": "https://example.com/page",
    "bootstrap_method": "GET",
    "capture_cookies": ["session_id", "_token"],
    "capture_headers": ["X-CSRF-Token"],
    "static_headers": {"X-Requested-With": "XMLHttpRequest"}
  }
}

Rules:
- Include ALL endpoints that return data — scores, listings, articles, products, user info, recommendations
- For GraphQL: include every unique operationName that returns data, with exact persisted query hashes
- Copy headers exactly as captured: custom API headers (x-api-key, x-imdb-*, authorization, etc.)
- Omit generic browser headers (cookie, referer, sec-*, accept-encoding, user-agent)
- Replace dynamic URL segments (IDs, UUIDs) with {variable_name}
- Set is_primary to false for all — the system decides which are primary after validation

Session detection:
- If data API requests carry cookies (Cookie header) or tokens from earlier Set-Cookie responses, include a "session" object
- bootstrap_url is the page URL that sets the cookies (usually the original page URL)
- capture_cookies: cookie names the APIs need (session_id, csrf_token, __cf_bm)
- capture_headers: response headers to forward (X-CSRF-Token)
- Do NOT include auth cookies that require login
- Omit the session object if no requests carry cookies
- Return valid JSON only`
}

// BuildPrompt constructs the user prompt from HAR traffic, DOM snapshot,
// and the original page URL for the LLM to analyze.
func BuildPrompt(har *browser.HARLog, dom string, originalURL string) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("## Original URL\n%s\n\n", originalURL))

	truncatedDOM := dom
	if len(truncatedDOM) > maxDOMLen {
		truncatedDOM = truncatedDOM[:maxDOMLen] + "\n... (truncated)"
	}
	b.WriteString(fmt.Sprintf("## DOM Snapshot\n```\n%s\n```\n\n", truncatedDOM))

	b.WriteString("## Captured HTTP Traffic\n\n")

	if har != nil {
		entries := har.Entries
		if len(entries) > maxHAREntries {
			entries = entries[:maxHAREntries]
		}
		for i, entry := range entries {
			b.WriteString(fmt.Sprintf("### Request %d\n", i+1))
			b.WriteString(fmt.Sprintf("- **Method:** %s\n", entry.Request.Method))
			b.WriteString(fmt.Sprintf("- **URL:** %s\n", entry.Request.URL))
			b.WriteString(fmt.Sprintf("- **Status:** %d\n", entry.Response.Status))
			b.WriteString(fmt.Sprintf("- **Content-Type:** %s\n", entry.Response.ContentType))
			b.WriteString(fmt.Sprintf("- **Response Size:** %d bytes\n", len(entry.Response.Body)))

			// Only include relevant headers (skip browser noise)
			if len(entry.Request.Headers) > 0 {
				relevant := filterRelevantHeaders(entry.Request.Headers)
				if len(relevant) > 0 {
					b.WriteString("- **Key Headers:**\n")
					for k, v := range relevant {
						b.WriteString(fmt.Sprintf("  - %s: %s\n", k, v))
					}
				}
			}

			if entry.Response.Body != "" {
				truncatedBody := truncateResponseBody(entry.Response.Body)
				b.WriteString(fmt.Sprintf("- **Response Body (preview):**\n```json\n%s\n```\n", truncatedBody))
			}

			b.WriteString("\n")
		}
	}

	return b.String()
}

// SuggestSystemPrompt returns the system prompt for knowledge-based API discovery.
// Used when browser capture fails — the LLM uses its training knowledge to suggest
// public API endpoints for the site.
func SuggestSystemPrompt() string {
	return `You are an API discovery expert. A user wants to get structured data from a URL, but the browser capture failed (the site may require authentication, use Cloudflare, or block headless browsers).

Your job: use your knowledge of this website to suggest PUBLIC API endpoints that return the same data WITHOUT authentication.

Think about:
- oEmbed endpoints (standard protocol — publish.twitter.com/oembed, www.youtube.com/oembed, etc.)
- Syndication/embed APIs (cdn.syndication.twimg.com for Twitter/X)
- Public REST APIs (api.github.com, hacker-news.firebaseio.com)
- RSS/Atom feeds (most blogs, news sites, forums)
- .json URL suffix (Reddit, Discourse forums, Rails apps)
- Public CDN/data endpoints
- Mobile API endpoints that may be less restricted

IMPORTANT: Only suggest endpoints you are confident exist and work without authentication.
If you don't know any public endpoint for this site, return {"endpoints": []}.

Respond with the same JSON format:
{
  "endpoints": [
    {
      "name": "descriptive name",
      "method": "GET",
      "url_template": "https://api.example.com/path/{variable}",
      "headers": {},
      "variables": [{"name": "id", "source": "path", "pattern": "\\d+"}],
      "is_primary": true,
      "response_mapping": {}
    }
  ]
}

Rules:
- Only include endpoints that work WITHOUT any authentication or API keys
- URL templates must use {variable_name} for dynamic parts
- Be specific — include exact domain, path, and query parameters
- Include the oEmbed endpoint if the site supports it
- Return valid JSON only`
}

// BuildSuggestPrompt constructs the user prompt for knowledge-based discovery.
func BuildSuggestPrompt(originalURL string, failureReason string) string {
	return fmt.Sprintf(`## URL the user wants to fetch
%s

## Why browser capture failed
%s

## Your task
Suggest public API endpoints that return structured data for this URL without requiring authentication.
Consider oEmbed, syndication APIs, public REST APIs, RSS feeds, and .json URL patterns.`, originalURL, failureReason)
}

const maxHTMLForLLM = 15000

// HTMLExtractionSystemPrompt returns the system prompt for identifying
// CSS selectors and entity fields that extract structured content from
// an HTML page. It asks for page type classification and typed entity
// extraction, producing compact agent-friendly output.
func HTMLExtractionSystemPrompt() string {
	return `You are an HTML content extraction expert. Given a web page's HTML, identify CSS selectors to extract ALL visible structured data.

## Your Task

1. Classify the PAGE TYPE: article, profile, product, listing, documentation, homepage, or generic
2. Find the MAIN CONTENT container selector
3. Extract EVERY visible data field — leave nothing useful behind

## CRITICAL: Extract ALL Fields

For EVERY piece of visible data on the page, create an entity field. If a user can see it, you must extract it.

### Listing pages (news feeds, search results, product lists, forums)
These are the MOST IMPORTANT to get right. Each repeated item has MULTIPLE fields — extract ALL of them:
- title, url/link (href attribute), domain/source
- score/points/votes, author/username, timestamp/age
- comment_count, category/tags, thumbnail (src attribute)
- rank/position, description/snippet

IMPORTANT — links with numeric text: When a link's visible text contains a number (e.g., "322 comments", "$29.99", "4.5 stars"), extract BOTH:
1. The text content as a SEPARATE field with type "number" (e.g., "comments_count")
2. The href as another field with "attribute": "href" (e.g., "comments_url")
Do NOT collapse both into one field. "322 comments" has TWO pieces of data: the count (322) and the URL.

Example — a news aggregator:
{"name": "stories", "selector": "tr.athing", "type": "list", "item_fields": [
  {"name": "rank", "selector": ".rank", "type": "string"},
  {"name": "title", "selector": ".titleline > a", "type": "string"},
  {"name": "url", "selector": ".titleline > a", "type": "string", "attribute": "href"},
  {"name": "domain", "selector": ".sitestr", "type": "string"},
  {"name": "points", "selector": ".score", "type": "number"},
  {"name": "author", "selector": ".hnuser", "type": "string"},
  {"name": "time_ago", "selector": ".age", "type": "string"},
  {"name": "comments_count", "selector": "a:last-child", "type": "number"},
  {"name": "comments_url", "selector": "a:last-child", "type": "string", "attribute": "href"}
]}

### Profile pages
- name, username, bio, avatar (src attribute)
- followers, following, post_count, join_date
- location, website (href attribute), social links

### Product pages
- product_name, price, rating, review_count
- description, brand, availability, images (src attribute)
- specifications as list with sub-fields

### Article pages
- author, published_date, category, read_time
- content (main body text), tags as list

## Response Format

{
  "extraction_rules": {
    "page_type": "listing",
    "content_selector": "main",
    "title_selector": "h1",
    "ignore_selectors": ["nav", "footer", ".sidebar"],
    "entity_fields": [...]
  }
}

## Selector Rules

- PREFER stable selectors: tag names, semantic attributes, [role=], [data-*], [itemprop=]
- AVOID generated classes: .css-abc123, .sc-xyz, .emotion-*, .styled-* — these break on redeployment
- For links: ALWAYS extract the text content AND the href as SEPARATE fields (use "attribute": "href" for the URL field). If the text contains a number (e.g., "322 comments", "$29.99", "4.5 stars"), extract the text as type "number" in its own field
- For images: use "attribute": "src"
- For lists: the top-level selector targets the REPEATING CONTAINER (the row/card), item_fields target elements WITHIN each container using RELATIVE selectors
- Mark fields "required": true if they are essential to the page's purpose (e.g., title and url on a listing page)
- Add "selectors": ["fallback1", "fallback2"] for fields that might use different markup across page variants

## Field Types

- "string": text content or attribute value
- "number": numeric value (system strips commas, $, k/m suffixes automatically)
- "list": repeated items — MUST include "item_fields" with sub-fields

## Table-Based Layouts (strategy: "table_rows")

If the page uses a <table> layout where one logical item spans MULTIPLE consecutive <tr> rows, you MUST use strategy "table_rows" instead of "css_selector".

Common examples: Hacker News, old forums (phpBB, vBulletin), government data tables, financial tables.

How to detect: look for a pattern where related data alternates across <tr> rows — e.g., one row has the title/link, the next row has metadata (score, author, date), and optionally a spacer row.

Response format for table_rows:
{
  "extraction_rules": {
    "strategy": "table_rows",
    "page_type": "listing",
    "content_selector": "table#maintable",
    "title_selector": "title",
    "table_rows": {
      "container": "table#maintable",
      "group_size": 3,
      "fields": [
        {"name": "title", "row": 0, "selector": ".titleline > a", "type": "string", "required": true},
        {"name": "url", "row": 0, "selector": ".titleline > a", "attribute": "href", "type": "string", "required": true},
        {"name": "domain", "row": 0, "selector": ".sitestr", "type": "string"},
        {"name": "points", "row": 1, "selector": ".score", "type": "number"},
        {"name": "author", "row": 1, "selector": ".hnuser", "type": "string"},
        {"name": "time_ago", "row": 1, "selector": ".age", "type": "string"},
        {"name": "comments_count", "row": 1, "selector": "a:last-child", "type": "number"},
        {"name": "comments_url", "row": 1, "selector": "a:last-child", "attribute": "href", "type": "string"}
      ]
    }
  }
}

Key rules for table_rows:
- "container" is the <table> or <tbody> that holds ALL the <tr> rows
- "group_size" is how many <tr> rows make up ONE logical item (count carefully — include spacer rows)
- "row" is 0-indexed within each group
- Each field's "selector" is applied WITHIN that specific <tr> row, not globally
- This solves the cross-sibling problem: fields in row 0 and row 1 of the same item are extracted correctly
- Do NOT use css_selector strategy with "list" type for table layouts — it won't work because sub-fields can't cross <tr> boundaries

Return valid JSON only.`
}

// BuildHTMLExtractionPrompt constructs the user prompt with truncated HTML.
func BuildHTMLExtractionPrompt(rawHTML string, originalURL string) string {
	// Strip script/style content to save tokens
	cleaned := stripScriptStyle(rawHTML)

	// Truncate to fit LLM context
	if len(cleaned) > maxHTMLForLLM {
		cleaned = cleaned[:maxHTMLForLLM] + "\n<!-- truncated -->"
	}

	return fmt.Sprintf("## URL\n%s\n\n## HTML\n```html\n%s\n```\n\nIdentify CSS selectors to extract the main content from this page.", originalURL, cleaned)
}

// NextDataPathsSystemPrompt returns the system prompt for analyzing __NEXT_DATA__
// pageProps and identifying named extraction paths.
func NextDataPathsSystemPrompt() string {
	return `You are a data structure analyzer. You analyze the top-level keys of a Next.js __NEXT_DATA__ pageProps object and identify meaningful extraction paths.

Your task: given the key structure of pageProps, return named paths to the most useful data sub-trees. Each path is a dot-notation path from the root of pageProps (e.g. ".ssrQuery.hits" for product listings).

Focus on paths that contain:
- Product/item listings or search results
- Navigation or category structures
- Facets, filters, or taxonomy data
- Pagination metadata
- User-facing content (articles, reviews, pricing)

Skip paths to:
- Internal framework state (_app, _nextI18Next, buildId, runtimeConfig)
- Authentication/session tokens
- Tracking/analytics config
- CSS/theme/i18n translation blobs

Respond with a JSON object:
{
  "paths": {
    "descriptive_alias": ".dot.path.to.data",
    "another_alias": ".another.path"
  }
}

Rules:
- Alias names should be snake_case and describe the data (e.g. "products", "navigation_categories", "facet_filters")
- Paths start with "." and use dot notation for object keys (e.g. ".ssrQuery.hits", ".initialZustandState.navigationData")
- CRITICAL: only use paths that appear EXACTLY in the key structure above. Do not guess or invent paths. Every segment in your path must match an actual key shown
- Keep to 3-10 paths — only the most useful ones
- If the data has no meaningful extractable paths (e.g. just a simple content page), return {"paths": {}}

Return valid JSON only.`
}

// BuildNextDataPathsPrompt constructs the user prompt with a key-tree
// skeleton of pageProps. Shows the structure (keys, types, array lengths)
// without the bulky values, so the LLM can identify paths accurately.
func BuildNextDataPathsPrompt(pageProps map[string]any, originalURL string) string {
	var sb strings.Builder
	buildKeyTree(&sb, pageProps, "", 0, 4)

	tree := sb.String()
	const maxTreeLen = 15000
	if len(tree) > maxTreeLen {
		tree = tree[:maxTreeLen] + "\n...[truncated]"
	}

	return fmt.Sprintf("## URL\n%s\n\n## __NEXT_DATA__ pageProps key structure\nEach line shows: path, type, and sample value (strings truncated). Arrays show length and first item's keys.\n\n```\n%s\n```\n\nIdentify the most useful named extraction paths. Use ONLY paths you see above — do not guess or invent paths.", originalURL, tree)
}

// buildKeyTree writes a compact key-tree representation of a JSON-like value.
// maxDepth limits recursion to prevent explosion on deeply nested structures.
func buildKeyTree(sb *strings.Builder, v any, prefix string, depth, maxDepth int) {
	if depth >= maxDepth {
		return
	}
	switch val := v.(type) {
	case map[string]any:
		// Sort keys for deterministic output
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		// Simple sort — avoid importing sort just for this
		for i := 0; i < len(keys); i++ {
			for j := i + 1; j < len(keys); j++ {
				if keys[i] > keys[j] {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		}
		for _, k := range keys {
			child := val[k]
			path := prefix + "." + k
			switch cv := child.(type) {
			case map[string]any:
				fmt.Fprintf(sb, "%s  (object, %d keys)\n", path, len(cv))
				buildKeyTree(sb, cv, path, depth+1, maxDepth)
			case []any:
				fmt.Fprintf(sb, "%s  (array, %d items)\n", path, len(cv))
				if len(cv) > 0 {
					// Show first item's structure
					buildKeyTree(sb, cv[0], path+"[0]", depth+1, maxDepth)
				}
			case string:
				sample := cv
				if len(sample) > 50 {
					sample = sample[:50] + "..."
				}
				fmt.Fprintf(sb, "%s  (string) = %q\n", path, sample)
			case float64:
				fmt.Fprintf(sb, "%s  (number) = %v\n", path, cv)
			case bool:
				fmt.Fprintf(sb, "%s  (bool) = %v\n", path, cv)
			case nil:
				fmt.Fprintf(sb, "%s  (null)\n", path)
			default:
				fmt.Fprintf(sb, "%s  (%T)\n", path, cv)
			}
		}
	case []any:
		if len(val) > 0 {
			buildKeyTree(sb, val[0], prefix+"[0]", depth, maxDepth)
		}
	}
}

// stripScriptStyle removes inline <script> and <style> content to reduce HTML size.
func stripScriptStyle(html string) string {
	var result strings.Builder
	result.Grow(len(html))

	i := 0
	for i < len(html) {
		// Check for <script or <style
		if i+7 < len(html) && html[i] == '<' {
			lower := strings.ToLower(html[i : i+7])
			if strings.HasPrefix(lower, "<script") || strings.HasPrefix(lower, "<style") {
				tag := "script"
				if strings.HasPrefix(lower, "<style") {
					tag = "style"
				}
				// Find closing tag
				closeTag := "</" + tag
				end := strings.Index(strings.ToLower(html[i:]), closeTag)
				if end != -1 {
					// Skip to after closing tag
					closeEnd := strings.Index(html[i+end:], ">")
					if closeEnd != -1 {
						i = i + end + closeEnd + 1
						continue
					}
				}
			}
		}
		result.WriteByte(html[i])
		i++
	}
	return result.String()
}

// filterRelevantHeaders keeps only headers that matter for API replay.
// Drops browser noise (sec-ch-*, accept-encoding, etc.) to save LLM tokens.
func filterRelevantHeaders(headers map[string]string) map[string]string {
	skip := map[string]bool{
		"accept-encoding": true, "accept-language": true,
		"cache-control": true, "connection": true,
		"sec-ch-ua": true, "sec-ch-ua-mobile": true, "sec-ch-ua-platform": true,
		"sec-fetch-dest": true, "sec-fetch-mode": true, "sec-fetch-site": true,
		"upgrade-insecure-requests": true, "user-agent": true,
		"referer": true, "origin": true, "dnt": true,
		"pragma": true, "if-none-match": true, "if-modified-since": true,
	}

	result := make(map[string]string)
	for k, v := range headers {
		if !skip[strings.ToLower(k)] {
			result[k] = v
		}
	}
	return result
}

// truncateResponseBody attempts to parse the body as JSON and truncate
// long values. If the body is not valid JSON, it truncates as a raw string.
func truncateResponseBody(body string) string {
	var parsed any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		if len(body) > maxResponseBodyLen {
			return body[:maxResponseBodyLen] + "..."
		}
		return body
	}

	truncated := TruncateJSONValues(parsed, maxResponseValueLen)
	result, err := json.MarshalIndent(truncated, "", "  ")
	if err != nil {
		return body
	}
	s := string(result)
	if len(s) > maxResponseBodyLen {
		return s[:maxResponseBodyLen] + "\n... (truncated)"
	}
	return s
}
