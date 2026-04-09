package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
	"github.com/hermai-ai/hermai-cli/internal/version"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

const validateTimeout = 10 * time.Second

var errValidationHTTP = errors.New("validation: endpoint returned error")
var errValidationUnreachable = errors.New("validation: endpoint unreachable")

type validationOptions struct {
	requireSemanticMatch bool
	stealth              bool     // use TLS fingerprinting for validation requests
	cookies              []string // name=value cookies to include in validation requests
}

// validationResult holds the outcome of validating a single endpoint.
type validationResult struct {
	endpoint      schema.Endpoint
	responseSize  int
	responseBody  []byte
	semanticScore float64
	ok            bool
}

// validateAllEndpoints tests every candidate endpoint concurrently.
// Returns only the endpoints that respond with valid JSON, ranked by semantic
// match first and response size second.
func validateAllEndpoints(ctx context.Context, s *schema.Schema, opts validationOptions) []schema.Endpoint {
	if len(s.Endpoints) == 0 {
		return nil
	}

	validateCtx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()

	var client httpclient.Doer
	if opts.stealth {
		client = httpclient.NewStealthOrFallback(httpclient.Options{Timeout: validateTimeout})
	} else {
		client = httpclient.New(httpclient.Options{Timeout: validateTimeout})
	}

	var sessionHeaders map[string]string
	if s.Session != nil {
		sessionHeaders = bootstrapSession(validateCtx, client, s.Session)
	}

	results := make([]validationResult, len(s.Endpoints))
	var wg sync.WaitGroup

	targetURL := s.DiscoveredFrom
	for i, ep := range s.Endpoints {
		wg.Add(1)
		go func(idx int, endpoint schema.Endpoint) {
			defer wg.Done()
			size, body, semantic, ok := validateEndpoint(validateCtx, client, endpoint, targetURL, sessionHeaders, opts.cookies)
			results[idx] = validationResult{
				endpoint:      endpoint,
				responseSize:  size,
				responseBody:  body,
				semanticScore: semantic,
				ok:            ok,
			}
		}(i, ep)
	}
	wg.Wait()

	var validated []schema.Endpoint
	allUnreachable := true
	for _, r := range results {
		if r.ok && (!opts.requireSemanticMatch || r.semanticScore >= 0.55) {
			ep := r.endpoint
			ep.Confidence = mergeConfidence(ep.Confidence, r.semanticScore)
			if len(r.responseBody) > 0 {
				ep.ResponseSchema = schema.InferResponseSchema(r.responseBody)
			}
			validated = append(validated, ep)
		}
		if r.responseSize != -1 {
			allUnreachable = false
		}
	}

	if allUnreachable && len(validated) == 0 {
		return s.Endpoints
	}

	sort.SliceStable(validated, func(i, j int) bool {
		left := resultForEndpoint(results, validated[i])
		right := resultForEndpoint(results, validated[j])
		if left.semanticScore == right.semanticScore {
			return left.responseSize > right.responseSize
		}
		return left.semanticScore > right.semanticScore
	})

	return validated
}

func mergeConfidence(existing float64, semantic float64) float64 {
	if semantic <= 0 {
		return existing
	}
	if existing <= 0 {
		return semantic
	}
	if semantic > existing {
		return semantic
	}
	return existing
}

func resultForEndpoint(results []validationResult, endpoint schema.Endpoint) validationResult {
	for _, result := range results {
		if result.endpoint.Method == endpoint.Method && result.endpoint.URLTemplate == endpoint.URLTemplate {
			return result
		}
	}
	return validationResult{}
}

// validateEndpoint tests a single endpoint. responseSize = -1 means
// unreachable, 0 means reachable but invalid, >0 means valid JSON.
func validateEndpoint(ctx context.Context, client httpclient.Doer, ep schema.Endpoint, targetURL string, sessionHeaders map[string]string, cookies []string) (int, []byte, float64, bool) {
	resolvedURL := ep.URLTemplate
	if len(ep.Variables) > 0 && targetURL != "" {
		resolved, err := resolveEndpointURL(ep.URLTemplate, targetURL, ep.Variables)
		if err == nil {
			resolvedURL = resolved
		}
	}

	req, err := http.NewRequestWithContext(ctx, ep.Method, resolvedURL, nil)
	if err != nil {
		return -1, nil, 0, false
	}

	req.Header.Set("User-Agent", version.UserAgent())
	for k, v := range ep.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range sessionHeaders {
		req.Header.Set(k, v)
	}
	// Inject user-provided cookies for authenticated endpoint validation
	if len(cookies) > 0 {
		var pairs []string
		for _, c := range cookies {
			if name, value, ok := strings.Cut(c, "="); ok && name != "" {
				pairs = append(pairs, name+"="+value)
			}
		}
		if len(pairs) > 0 {
			existing := req.Header.Get("Cookie")
			if existing != "" {
				req.Header.Set("Cookie", existing+"; "+strings.Join(pairs, "; "))
			} else {
				req.Header.Set("Cookie", strings.Join(pairs, "; "))
			}
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return -1, nil, 0, false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, nil, 0, false
	}

	const probeSize = 64 * 1024
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, probeSize))
	if err != nil {
		return 0, nil, 0, false
	}

	trimmed := strings.TrimSpace(string(bodyBytes))
	if len(trimmed) == 0 {
		return 0, nil, 0, false
	}

	firstByte := trimmed[0]
	if firstByte != '{' && firstByte != '[' {
		return 0, nil, 0, false
	}

	if len(bodyBytes) < probeSize {
		var probe json.RawMessage
		if err := json.Unmarshal(bodyBytes, &probe); err != nil {
			return 0, nil, 0, false
		}
	}

	return len(bodyBytes), bodyBytes, semanticScore(targetURL, resolvedURL, bodyBytes), true
}

// bootstrapSession hits the bootstrap URL and extracts session cookies/headers.
func bootstrapSession(ctx context.Context, client httpclient.Doer, session *schema.SessionConfig) map[string]string {
	method := session.BootstrapMethod
	if method == "" {
		method = "GET"
	}

	req, err := http.NewRequestWithContext(ctx, method, session.BootstrapURL, nil)
	if err != nil {
		return nil
	}

	req.Header.Set("User-Agent", version.UserAgent())
	for k, v := range session.StaticHeaders {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	result := make(map[string]string)

	for _, h := range session.CaptureHeaders {
		if val := resp.Header.Get(h); val != "" {
			result[h] = val
		}
	}

	if len(session.CaptureCookies) > 0 {
		wantCookie := make(map[string]bool, len(session.CaptureCookies))
		for _, name := range session.CaptureCookies {
			wantCookie[name] = true
		}

		var cookies []string
		for _, c := range resp.Cookies() {
			if wantCookie[c.Name] {
				cookies = append(cookies, c.Name+"="+url.QueryEscape(c.Value))
			}
		}
		if len(cookies) > 0 {
			result["Cookie"] = strings.Join(cookies, "; ")
		}
	}

	return result
}

// resolveEndpointURL replaces {varName} placeholders in the URL template
// using the variable definitions and the target URL.
func resolveEndpointURL(template, targetURL string, variables []schema.Variable) (string, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return "", err
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")

	result := template
	for _, v := range variables {
		var value string
		switch v.Source {
		case "path", "url":
			idx, idxErr := strconv.Atoi(v.Pattern)
			if idxErr == nil && idx >= 0 && idx < len(segments) {
				value = segments[idx]
			} else if idxErr != nil && v.Pattern != "" {
				// Pattern is a regex — scan segments from the end to find a match
				// (dynamic values like IDs are usually the last segments)
				if re, reErr := regexp.Compile(v.Pattern); reErr == nil {
					for i := len(segments) - 1; i >= 0; i-- {
						if re.MatchString(segments[i]) {
							value = segments[i]
							break
						}
					}
				}
			}
		case "path_rest":
			idx, idxErr := strconv.Atoi(v.Pattern)
			if idxErr == nil && idx >= 0 && idx < len(segments) {
				value = strings.Join(segments[idx:], "/")
			}
		case "query":
			value = parsed.Query().Get(v.Pattern)
		case "fixed":
			value = v.Pattern
		}
		if value != "" {
			result = strings.ReplaceAll(result, "{"+v.Name+"}", value)
		}
	}
	return result, nil
}

func findPrimaryEndpoint(s *schema.Schema) *schema.Endpoint {
	for i := range s.Endpoints {
		if s.Endpoints[i].IsPrimary {
			return &s.Endpoints[i]
		}
	}
	if len(s.Endpoints) > 0 {
		return &s.Endpoints[0]
	}
	return nil
}
