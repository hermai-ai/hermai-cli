package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

const (
	defaultTimeout    = 5 * time.Second
	perRequestTimeout = 3 * time.Second
	maxBodySize       = 2 * 1024 * 1024 // 2MB
	minKeyCount       = 2

	browserUserAgent = httpclient.BrowserUserAgent
)

// SetBrowserHeaders sets headers that match a real Chrome browser navigation.
// Cloudflare and similar WAFs check Sec-Fetch-* headers to distinguish
// real browsers from HTTP libraries.
func SetBrowserHeaders(req *http.Request) {
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
}

// Result contains what the probe found.
type Result struct {
	Schema          *schema.Schema
	Strategy        string // best-ranked candidate strategy
	Candidates      []Candidate
	HTMLBody        string // If probe got HTML instead of JSON, the body is stored here for reuse
	RequiresStealth bool   // true if stealth TLS was needed to bypass anti-bot
}

// Candidate is one successful probe-discovered schema candidate.
type Candidate struct {
	Schema   *schema.Schema
	Strategy string
	Score    int
}

// Options configures probe behavior.
type Options struct {
	ProxyURL string
	Timeout  time.Duration
	Insecure bool // skip TLS certificate verification
	Stealth  bool // use TLS+HTTP/2 fingerprinting (Chrome profile)
}

// strategy is a probe function that returns a schema and strategy name on success.
type strategy struct {
	name string
	fn   func(ctx context.Context, client httpclient.Doer, targetURL string) (*schema.Schema, error)
}

// Probe tries to detect direct JSON access patterns for a URL. Successful
// candidates are scored so deterministic, reusable endpoints beat whichever
// request happens to finish first.
func Probe(ctx context.Context, targetURL string, opts Options) (*Result, error) {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := NewClient(opts)
	strategies := buildStrategies()

	outcome := runConcurrent(ctx, client, targetURL, strategies)
	if len(outcome.candidates) > 0 {
		return &Result{
			Schema:     outcome.candidates[0].Schema,
			Strategy:   outcome.candidates[0].Strategy,
			Candidates: outcome.candidates,
		}, nil
	}

	// Check HTML fallback before escalating
	htmlBody, htmlErr := fetchHTMLBody(ctx, client, targetURL)
	if htmlErr == nil && htmlBody != "" {
		return &Result{HTMLBody: htmlBody}, nil
	}

	// Stealth escalation: if blocked and not already using stealth, retry
	blocked := outcome.blocked || errors.Is(htmlErr, errAntiBot)
	if blocked && !opts.Stealth {
		stealthClient := httpclient.NewStealthOrFallback(httpclient.Options{
			ProxyURL: opts.ProxyURL,
			Insecure: opts.Insecure,
			Timeout:  perRequestTimeout,
		})

		stealthOutcome := runConcurrent(ctx, stealthClient, targetURL, strategies)
		if len(stealthOutcome.candidates) > 0 {
			return &Result{
				Schema:          stealthOutcome.candidates[0].Schema,
				Strategy:        stealthOutcome.candidates[0].Strategy,
				Candidates:      stealthOutcome.candidates,
				RequiresStealth: true,
			}, nil
		}

		stealthHTML, stealthErr := fetchHTMLBody(ctx, stealthClient, targetURL)
		if stealthErr == nil && stealthHTML != "" {
			return &Result{HTMLBody: stealthHTML, RequiresStealth: true}, nil
		}
	}

	return &Result{}, nil
}

func buildStrategies() []strategy {
	return []strategy{
		{
			"known_site",
			func(ctx context.Context, client httpclient.Doer, targetURL string) (*schema.Schema, error) {
				s, _, err := tryKnownSite(ctx, client, targetURL)
				return s, err
			},
		},
		{"shopify", tryShopify},
		{"json_suffix", tryJSONSuffix},
		{"accept_header", tryAcceptHeader},
		{"wp_json", tryWordPressAPI},
	}
}

// probeOutcome holds candidates and whether any strategy detected an anti-bot block.
type probeOutcome struct {
	candidates []Candidate
	blocked    bool
}

// runConcurrent runs all strategies concurrently and returns ranked candidates.
func runConcurrent(ctx context.Context, client httpclient.Doer, targetURL string, strategies []strategy) probeOutcome {
	type probeResult struct {
		candidate Candidate
		blocked   bool
	}

	resultCh := make(chan probeResult, len(strategies))
	var wg sync.WaitGroup

	for _, s := range strategies {
		wg.Add(1)
		go func(st strategy) {
			defer wg.Done()
			candidateSchema, err := st.fn(ctx, client, targetURL)
			if errors.Is(err, errAntiBot) {
				resultCh <- probeResult{blocked: true}
				return
			}
			if err == nil && candidateSchema != nil {
				resultCh <- probeResult{
					candidate: Candidate{
						Schema:   candidateSchema,
						Strategy: strategyLabel(st.name, candidateSchema),
						Score:    candidateScore(st.name, candidateSchema),
					},
				}
			}
		}(s)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var outcome probeOutcome
	for r := range resultCh {
		if r.blocked {
			outcome.blocked = true
			continue
		}
		outcome.candidates = append(outcome.candidates, r.candidate)
	}

	if len(outcome.candidates) > 1 {
		sort.SliceStable(outcome.candidates, func(i, j int) bool {
			if outcome.candidates[i].Score == outcome.candidates[j].Score {
				return outcome.candidates[i].Strategy < outcome.candidates[j].Strategy
			}
			return outcome.candidates[i].Score > outcome.candidates[j].Score
		})
		outcome.candidates = dedupeCandidates(outcome.candidates)
	}

	return outcome
}

// NewClient builds an HTTP client from probe options. Follows up to 10
// redirects so callers see the final response instead of 301/302 hops.
func NewClient(opts Options) httpclient.Doer {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = perRequestTimeout
	}
	clientOpts := httpclient.Options{
		ProxyURL: opts.ProxyURL,
		Insecure: opts.Insecure,
		Timeout:  timeout,
	}
	if opts.Stealth {
		if c, err := httpclient.NewStealthWithRedirects(clientOpts, 10); err == nil {
			return c
		}
		// Fall through to plain client if stealth init fails.
	}
	return httpclient.New(clientOpts)
}

// tryJSONSuffix appends .json to the URL path and checks if the server
// returns valid JSON. Works for Reddit, Discourse, Rails apps.
func tryJSONSuffix(ctx context.Context, client httpclient.Doer, targetURL string) (*schema.Schema, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + ".json"
	body, err := doJSONRequest(ctx, client, parsed.String(), nil)
	if err != nil || body == nil {
		return nil, err
	}

	return buildSchema(targetURL, parsed.String(), nil, "page_json", "JSON endpoint discovered by appending .json to the page URL", 0.96)
}

// tryAcceptHeader sends the original URL with Accept: application/json.
// Works for many REST APIs that content-negotiate.
func tryAcceptHeader(ctx context.Context, client httpclient.Doer, targetURL string) (*schema.Schema, error) {
	headers := map[string]string{"Accept": "application/json"}

	body, err := doJSONRequest(ctx, client, targetURL, headers)
	if err != nil || body == nil {
		return nil, err
	}

	return buildSchema(targetURL, targetURL, headers, "page_data", "JSON representation of the page negotiated with Accept: application/json", 0.8)
}

// tryWordPressAPI checks if the site has a WordPress REST API.
// Pattern: {origin}/wp-json/wp/v2/posts?per_page=1
func tryWordPressAPI(ctx context.Context, client httpclient.Doer, targetURL string) (*schema.Schema, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}

	wpURL := parsed.Scheme + "://" + parsed.Host + "/wp-json/wp/v2/posts?per_page=1"
	body, err := doJSONRequest(ctx, client, wpURL, nil)
	if err != nil || body == nil {
		return nil, err
	}

	return buildSchema(targetURL, wpURL, nil, "wordpress_posts", "Generic WordPress REST posts feed discovered from the site origin", 0.3)
}

// fetchHTMLBody performs a simple GET request and returns the response body as HTML.
// Used as a last-resort probe step to capture HTML for the engine's fallback.
func fetchHTMLBody(ctx context.Context, client httpclient.Doer, targetURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", err
	}

	SetBrowserHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return "", err
	}

	body := string(bodyBytes)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if isBlockedResponse(resp.StatusCode, body) {
			return "", errAntiBot
		}
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	if isBlockedResponse(resp.StatusCode, body) {
		return "", errAntiBot
	}

	return body, nil
}

// doJSONRequest performs a GET request and validates the response is
// meaningful JSON. Returns the parsed body, or nil if the response is
// not valid/sufficient JSON.
func doJSONRequest(ctx context.Context, client httpclient.Doer, requestURL string, extraHeaders map[string]string) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, nil
	}

	req.Header.Set("User-Agent", browserUserAgent)
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read body to check for anti-bot challenge markers
		if resp.StatusCode == 403 || resp.StatusCode == 429 || resp.StatusCode == 503 {
			bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
			if readErr == nil && isBlockedResponse(resp.StatusCode, string(bodyBytes)) {
				return nil, errAntiBot
			}
		}
		return nil, nil
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(contentType), "json") {
		// 200 with HTML might be a challenge page
		if strings.Contains(strings.ToLower(contentType), "html") {
			bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
			if readErr == nil && isBlockedResponse(resp.StatusCode, string(bodyBytes)) {
				return nil, errAntiBot
			}
		}
		return nil, nil
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, nil
	}

	return validateJSON(bodyBytes)
}

// validateJSON checks that the bytes are valid JSON with sufficient complexity.
func validateJSON(data []byte) (any, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil
	}

	if countKeys(raw, 2) >= minKeyCount {
		return raw, nil
	}

	return nil, nil
}

// countKeys counts total keys recursively up to maxDepth.
func countKeys(v any, maxDepth int) int {
	if maxDepth <= 0 {
		return 0
	}
	switch val := v.(type) {
	case map[string]any:
		count := len(val)
		for _, child := range val {
			count += countKeys(child, maxDepth-1)
		}
		return count
	case []any:
		if len(val) == 0 {
			return 0
		}
		return countKeys(val[0], maxDepth)
	default:
		return 0
	}
}

// buildSchema constructs a schema from a successful probe result.
func buildSchema(originalURL, probeURL string, headers map[string]string, endpointName, description string, confidence float64) (*schema.Schema, error) {
	parsed, err := url.Parse(originalURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse original URL: %w", err)
	}
	probeParsed, err := url.Parse(probeURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse probe URL: %w", err)
	}

	domain := parsed.Host
	urlPattern, variables := inferURLPattern(parsed.Path, probeParsed.Path)
	urlTemplate := templatizeURL(probeURL, parsed.Path, variables)

	endpoint := schema.Endpoint{
		Name:        endpointName,
		Description: description,
		Method:      "GET",
		URLTemplate: urlTemplate,
		Headers:     headers,
		Variables:   variables,
		IsPrimary:   true,
		Confidence:  confidence,
	}

	if endpoint.Headers == nil {
		endpoint.Headers = map[string]string{}
	}

	return &schema.Schema{
		ID:             schema.GenerateID(domain, urlPattern),
		Domain:         domain,
		URLPattern:     urlPattern,
		SchemaType:     schema.SchemaTypeAPI,
		Coverage:       schema.SchemaCoveragePartial,
		Version:        1,
		CreatedAt:      time.Now(),
		DiscoveredFrom: originalURL,
		Endpoints:      []schema.Endpoint{endpoint},
	}, nil
}

func strategyLabel(strategy string, s *schema.Schema) string {
	if strategy != "known_site" || s == nil || len(s.Endpoints) == 0 {
		return strategy
	}
	return "known_site:" + s.Endpoints[0].Name
}

func candidateScore(strategy string, s *schema.Schema) int {
	score := 0
	switch strategy {
	case "known_site":
		score = 140
	case "shopify":
		score = 130
	case "json_suffix":
		score = 120
	case "accept_header":
		score = 80
	case "wp_json":
		score = 20
	}

	if s != nil {
		if strings.Contains(s.URLPattern, "{}") {
			score += 10
		}
		if len(s.Endpoints) > 0 && len(s.Endpoints[0].Headers) > 0 {
			score -= 5
		}
	}

	return score
}

func dedupeCandidates(candidates []Candidate) []Candidate {
	seen := make(map[string]bool, len(candidates))
	deduped := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Schema == nil || len(candidate.Schema.Endpoints) == 0 {
			continue
		}
		key := candidate.Schema.Endpoints[0].Method + " " + candidate.Schema.Endpoints[0].URLTemplate
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, candidate)
	}
	return deduped
}

var resourceLikeParents = map[string]string{
	"r":          "subreddit",
	"u":          "user",
	"user":       "user",
	"users":      "user",
	"profile":    "profile",
	"profiles":   "profile",
	"product":    "product",
	"products":   "product",
	"item":       "item",
	"items":      "item",
	"pokemon":    "pokemon",
	"repo":       "repo",
	"repos":      "repo",
	"org":        "org",
	"orgs":       "org",
	"article":    "article",
	"articles":   "article",
	"post":       "post",
	"posts":      "post",
	"topic":      "topic",
	"topics":     "topic",
	"tag":        "tag",
	"tags":       "tag",
	"category":   "category",
	"categories": "category",
	"channel":    "channel",
	"channels":   "channel",
	"creator":    "creator",
	"creators":   "creator",
	"company":    "company",
	"companies":  "company",
	"project":    "project",
	"projects":   "project",
	"shop":       "shop",
	"shops":      "shop",
	"store":      "store",
	"stores":     "store",
	"vendor":     "vendor",
	"vendors":    "vendor",
	"member":     "member",
	"members":    "member",
}

var reservedStaticSlugs = map[string]bool{
	"about": true, "all": true, "api": true, "archive": true, "archives": true,
	"auth": true, "best": true, "comments": true, "explore": true, "feed": true,
	"feeds": true, "help": true, "home": true, "hot": true, "index": true,
	"latest": true, "list": true, "lists": true, "login": true, "me": true,
	"new": true, "oauth": true, "overview": true, "popular": true, "preferences": true,
	"replies": true, "saved": true, "search": true, "self": true, "settings": true,
	"sign-in": true, "signin": true, "sign-up": true, "signup": true, "top": true,
	"trending": true,
}

func inferURLPattern(originalPath, probePath string) (string, []schema.Variable) {
	pathOnly := strings.SplitN(originalPath, "?", 2)[0]
	if pathOnly == "/" {
		return "/", nil
	}

	segments := splitPathSegments(pathOnly)
	normalized := splitPathSegments(schema.NormalizePathStructure(pathOnly))
	variables := extractVariables(pathOnly)

	// Probe-discovered schemas need an extra heuristic for short slugs like
	// /r/programming or /pokemon/pikachu, which global path normalization
	// intentionally does not wildcard to avoid cache collisions elsewhere.
	if len(segments) > 0 && len(segments) == len(normalized) {
		last := len(segments) - 1
		if normalized[last] != "{}" && shouldPromoteSlugVariable(segments, probePath) {
			normalized[last] = "{}"
			variables = append(variables, schema.Variable{
				Name:    slugVariableName(segments),
				Source:  "path",
				Pattern: fmt.Sprintf("%d", last),
			})
		}
	}

	return "/" + strings.Join(normalized, "/"), variables
}

func shouldPromoteSlugVariable(segments []string, probePath string) bool {
	if len(segments) == 0 {
		return false
	}

	last := segments[len(segments)-1]
	if !isSlugLike(last) || reservedStaticSlugs[strings.ToLower(last)] {
		return false
	}

	parent := ""
	if len(segments) >= 2 {
		parent = strings.ToLower(segments[len(segments)-2])
	}

	if _, ok := resourceLikeParents[parent]; ok {
		return true
	}

	probeSegments := splitPathSegments(probePath)
	if len(probeSegments) != len(segments) {
		return false
	}

	// Suffix-style endpoints like /r/programming -> /r/programming.json
	// are a strong signal that the final segment is the page variable.
	return probeSegments[len(probeSegments)-1] == last+".json"
}

func slugVariableName(segments []string) string {
	if len(segments) < 2 {
		return "slug"
	}

	parent := strings.ToLower(segments[len(segments)-2])
	if name, ok := resourceLikeParents[parent]; ok {
		return name
	}
	if strings.HasSuffix(parent, "ies") && len(parent) > 3 {
		return parent[:len(parent)-3] + "y"
	}
	if strings.HasSuffix(parent, "s") && len(parent) > 1 {
		return parent[:len(parent)-1]
	}
	if parent != "" {
		return parent
	}
	return "slug"
}

func isSlugLike(segment string) bool {
	if segment == "" {
		return false
	}

	hasLetter := false
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z':
			hasLetter = true
		case r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return hasLetter
}

func splitPathSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// templatizeURL replaces dynamic path segments with named placeholders.
func templatizeURL(probeURL, originalPath string, variables []schema.Variable) string {
	if len(variables) == 0 {
		return probeURL
	}

	parsed, err := url.Parse(probeURL)
	if err != nil {
		return probeURL
	}

	segments := splitPathSegments(originalPath)
	probeSegments := splitPathSegments(parsed.Path)
	for _, v := range variables {
		idx, err := strconv.Atoi(v.Pattern)
		if err != nil || idx < 0 || idx >= len(segments) || idx >= len(probeSegments) {
			continue
		}
		placeholder := "{" + v.Name + "}"
		probeSegments[idx] = strings.Replace(probeSegments[idx], segments[idx], placeholder, 1)
	}

	newPath := "/"
	if len(probeSegments) > 0 {
		newPath = "/" + strings.Join(probeSegments, "/")
	}

	oldPath := parsed.EscapedPath()
	if oldPath == "" {
		oldPath = parsed.Path
	}
	if oldPath == "" {
		return probeURL
	}

	return strings.Replace(probeURL, oldPath, newPath, 1)
}

// extractVariables identifies dynamic path segments.
func extractVariables(path string) []schema.Variable {
	pathOnly := strings.SplitN(path, "?", 2)[0]
	segments := strings.Split(strings.Trim(pathOnly, "/"), "/")
	normalized := strings.Split(strings.Trim(schema.NormalizePathStructure(pathOnly), "/"), "/")

	var variables []schema.Variable
	for i, norm := range normalized {
		if norm == "{}" && i < len(segments) {
			variables = append(variables, schema.Variable{
				Name:    fmt.Sprintf("param_%d", i),
				Source:  "path",
				Pattern: fmt.Sprintf("%d", i),
			})
		}
	}

	return variables
}
