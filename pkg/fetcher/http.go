package fetcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
	"github.com/hermai-ai/hermai-cli/internal/version"
	"github.com/hermai-ai/hermai-cli/pkg/retry"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// HTTPFetcher implements Service by making direct HTTP calls to API endpoints.
type HTTPFetcher struct {
	client httpclient.Doer
	retry  retry.Config
}

// NewHTTPFetcher creates a new HTTPFetcher with a default HTTP client.
func NewHTTPFetcher() *HTTPFetcher {
	return &HTTPFetcher{
		client: httpclient.New(httpclient.Options{}),
		retry:  retry.Default(),
	}
}

// NewHTTPFetcherWithProxy creates a fetcher that routes requests through a proxy.
func NewHTTPFetcherWithProxy(proxyURL string, insecure bool) *HTTPFetcher {
	return &HTTPFetcher{
		client: httpclient.New(httpclient.Options{
			ProxyURL: proxyURL,
			Insecure: insecure,
		}),
		retry: retry.Default(),
	}
}

// NewHTTPFetcherWithClient creates a fetcher with a caller-provided HTTP client.
// Useful for testing or injecting a tls-client transport.
func NewHTTPFetcherWithClient(client httpclient.Doer) *HTTPFetcher {
	return &HTTPFetcher{
		client: client,
		retry:  retry.Default(),
	}
}

// WithRetry returns a copy with custom retry config. Useful for testing.
func (f *HTTPFetcher) WithRetry(cfg retry.Config) *HTTPFetcher {
	return &HTTPFetcher{
		client: f.client,
		retry:  cfg,
	}
}

// endpointResult holds the outcome of a single endpoint call.
type endpointResult struct {
	index      int
	endpoint   schema.Endpoint
	body       []byte
	statusCode int
	headers    map[string]string
	err        error
}

// Fetch executes all endpoints in the schema concurrently and returns the aggregated result.
// If the schema has a Session config, a bootstrap request is made first to obtain
// session cookies and tokens that are carried into subsequent data API calls.
func (f *HTTPFetcher) Fetch(ctx context.Context, s *schema.Schema, targetURL string, opts FetchOpts) (*Result, error) {
	start := time.Now()

	if len(s.Endpoints) == 0 {
		return &Result{
			Data: make(map[string]any),
			Metadata: ResultMetadata{
				SchemaID:      s.ID,
				SchemaVersion: s.Version,
				Source:        "http_fetcher",
			},
		}, nil
	}

	// Session bootstrap: hit the bootstrap URL to get fresh cookies/tokens
	if s.Session != nil {
		sessionHeaders, err := f.bootstrap(ctx, s.Session)
		if err == nil {
			if opts.HeaderOverrides == nil {
				opts.HeaderOverrides = make(map[string]string)
			}
			for k, v := range sessionHeaders {
				opts.HeaderOverrides[k] = v
			}
		}
		// Bootstrap failure is non-fatal — try the data fetch anyway
	}

	if len(s.Endpoints) == 1 {
		return f.fetchSingle(ctx, s, targetURL, opts, start)
	}

	return f.fetchConcurrent(ctx, s, targetURL, opts, start)
}

func (f *HTTPFetcher) fetchSingle(ctx context.Context, s *schema.Schema, targetURL string, opts FetchOpts, start time.Time) (*Result, error) {
	ep := s.Endpoints[0]
	body, statusCode, headers, err := f.callWithRetry(ctx, ep, targetURL, opts)
	if err != nil {
		return nil, err
	}

	// Single endpoint: return data flat (not nested under name) for backward compatibility
	var data any
	if parseErr := json.Unmarshal(body, &data); parseErr != nil {
		return nil, fmt.Errorf("%w: endpoint %s returned non-JSON response", ErrSchemaBroken, ep.Name)
	}

	var rawResponses []RawResponse
	if opts.Raw {
		rawResponses = append(rawResponses, RawResponse{
			EndpointName: ep.Name,
			StatusCode:   statusCode,
			Headers:      headers,
			Body:         json.RawMessage(body),
		})
	}

	return &Result{
		Data: data,
		Raw:  rawResponses,
		Metadata: ResultMetadata{
			SchemaID:        s.ID,
			SchemaVersion:   s.Version,
			Source:          "http_fetcher",
			EndpointsCalled: 1,
			TotalLatencyMs:  time.Since(start).Milliseconds(),
		},
	}, nil
}

func (f *HTTPFetcher) fetchConcurrent(ctx context.Context, s *schema.Schema, targetURL string, opts FetchOpts, start time.Time) (*Result, error) {
	results := make([]endpointResult, len(s.Endpoints))

	var wg sync.WaitGroup
	for i, ep := range s.Endpoints {
		wg.Add(1)
		go func(idx int, endpoint schema.Endpoint) {
			defer wg.Done()
			body, statusCode, headers, err := f.callWithRetry(ctx, endpoint, targetURL, opts)
			results[idx] = endpointResult{
				index:      idx,
				endpoint:   endpoint,
				body:       body,
				statusCode: statusCode,
				headers:    headers,
				err:        err,
			}
		}(i, ep)
	}
	wg.Wait()

	// Merge ALL endpoint results by endpoint name.
	// Each endpoint's parsed JSON goes under its Name key.
	merged := make(map[string]any)
	var rawResponses []RawResponse

	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}

		if opts.Raw {
			rawResponses = append(rawResponses, RawResponse{
				EndpointName: r.endpoint.Name,
				StatusCode:   r.statusCode,
				Headers:      r.headers,
				Body:         json.RawMessage(r.body),
			})
		}

		var parsed any
		if err := json.Unmarshal(r.body, &parsed); err != nil {
			return nil, fmt.Errorf("%w: endpoint %s returned non-JSON response", ErrSchemaBroken, r.endpoint.Name)
		}
		merged[r.endpoint.Name] = parsed
	}

	return &Result{
		Data: merged,
		Raw:  rawResponses,
		Metadata: ResultMetadata{
			SchemaID:        s.ID,
			SchemaVersion:   s.Version,
			Source:          "http_fetcher",
			EndpointsCalled: len(s.Endpoints),
			TotalLatencyMs:  time.Since(start).Milliseconds(),
		},
	}, nil
}

// callWithRetry wraps callEndpoint with retry for transient failures.
func (f *HTTPFetcher) callWithRetry(ctx context.Context, endpoint schema.Endpoint, targetURL string, opts FetchOpts) ([]byte, int, map[string]string, error) {
	type callResult struct {
		body       []byte
		statusCode int
		headers    map[string]string
	}

	r, err := retry.Do(ctx, f.retry, func() (callResult, error) {
		body, statusCode, headers, err := f.callEndpoint(ctx, endpoint, targetURL, opts)
		if err != nil {
			return callResult{}, err
		}
		return callResult{body: body, statusCode: statusCode, headers: headers}, nil
	}, IsTransient)

	if err != nil {
		return nil, 0, nil, err
	}
	return r.body, r.statusCode, r.headers, nil
}

// callEndpoint executes a single API endpoint and returns the raw response.
// clientFor returns a stealth Doer when opts.Stealth is true, otherwise the default client.
func (f *HTTPFetcher) clientFor(opts FetchOpts) httpclient.Doer {
	if !opts.Stealth {
		return f.client
	}
	return httpclient.NewStealthOrFallback(httpclient.Options{
		ProxyURL: opts.ProxyURL,
		Insecure: opts.Insecure,
	})
}

func (f *HTTPFetcher) callEndpoint(ctx context.Context, endpoint schema.Endpoint, targetURL string, opts FetchOpts) (body []byte, statusCode int, headers map[string]string, err error) {
	resolvedURL, err := resolveURL(endpoint.URLTemplate, targetURL, endpoint.Variables)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to resolve URL for endpoint %s: %w", endpoint.Name, err)
	}

	var bodyReader io.Reader
	var contentType string
	if endpoint.Body != nil {
		resolvedBody, bodyErr := resolveBodyTemplate(endpoint.Body.Template, targetURL, endpoint.Variables)
		if bodyErr != nil {
			return nil, 0, nil, fmt.Errorf("failed to resolve body for endpoint %s: %w", endpoint.Name, bodyErr)
		}
		bodyReader = bytes.NewBufferString(resolvedBody)
		contentType = endpoint.Body.ContentType
	}

	req, err := http.NewRequestWithContext(ctx, endpoint.Method, resolvedURL, bodyReader)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to create request for endpoint %s: %w", endpoint.Name, err)
	}

	req.Header.Set("User-Agent", version.UserAgent())
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for key, value := range endpoint.Headers {
		req.Header.Set(key, value)
	}
	for key, value := range opts.HeaderOverrides {
		req.Header.Set(key, value)
	}

	resp, err := f.clientFor(opts).Do(req)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to execute request for endpoint %s: %w", endpoint.Name, err)
	}

	const maxAPIResponse = 10 * 1024 * 1024 // 10MB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
	resp.Body.Close()
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to read response body for endpoint %s: %w", endpoint.Name, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, 0, nil, WrapTransient(endpoint.Name, resp.StatusCode)
	}

	respHeaders := make(map[string]string, len(resp.Header))
	for key := range resp.Header {
		respHeaders[key] = resp.Header.Get(key)
	}

	return respBody, resp.StatusCode, respHeaders, nil
}

// resolveURL replaces {variableName} placeholders in the URL template.
func resolveURL(template, targetURL string, variables []schema.Variable) (string, error) {
	resolved := template
	for _, v := range variables {
		value, err := resolveVariable(v, targetURL)
		if err != nil {
			return "", fmt.Errorf("failed to resolve variable %s: %w", v.Name, err)
		}
		resolved = strings.ReplaceAll(resolved, "{"+v.Name+"}", value)
	}
	return resolved, nil
}

// resolveBodyTemplate replaces {variableName} placeholders in the body template.
func resolveBodyTemplate(template, targetURL string, variables []schema.Variable) (string, error) {
	resolved := template
	for _, v := range variables {
		value, err := resolveVariable(v, targetURL)
		if err != nil {
			return "", fmt.Errorf("failed to resolve variable %s in body: %w", v.Name, err)
		}
		resolved = strings.ReplaceAll(resolved, "{"+v.Name+"}", value)
	}
	return resolved, nil
}

// resolveVariable extracts a value for a single variable from the target URL.
func resolveVariable(v schema.Variable, targetURL string) (string, error) {
	switch v.Source {
	case "path", "url":
		return resolvePathVariable(v, targetURL)
	case "path_rest":
		return resolvePathRestVariable(v, targetURL)
	case "query":
		return resolveQueryVariable(v, targetURL)
	case "fixed":
		return v.Pattern, nil
	default:
		return "", fmt.Errorf("unknown variable source: %s", v.Source)
	}
}

func resolvePathVariable(v schema.Variable, targetURL string) (string, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse target URL: %w", err)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")

	index, err := strconv.Atoi(v.Pattern)
	if err == nil {
		if index < 0 || index >= len(segments) {
			return "", fmt.Errorf("segment index %d out of bounds (path has %d segments)", index, len(segments))
		}
		return segments[index], nil
	}

	re, reErr := regexp.Compile(v.Pattern)
	if reErr != nil {
		for i := len(segments) - 1; i >= 0; i-- {
			if segments[i] != "" {
				return segments[i], nil
			}
		}
		return "", fmt.Errorf("no path segments found in %q", targetURL)
	}

	for i := len(segments) - 1; i >= 0; i-- {
		if re.MatchString(segments[i]) {
			return segments[i], nil
		}
	}
	return "", fmt.Errorf("no path segment matched pattern %q in %q", v.Pattern, parsed.Path)
}

// resolvePathRestVariable joins all path segments from the given index onward.
// Used for multi-segment captures like file paths (e.g., "src/pkg/file.go").
func resolvePathRestVariable(v schema.Variable, targetURL string) (string, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse target URL: %w", err)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")

	index, err := strconv.Atoi(v.Pattern)
	if err != nil {
		return "", fmt.Errorf("path_rest pattern must be a segment index, got %q", v.Pattern)
	}
	if index < 0 || index >= len(segments) {
		return "", fmt.Errorf("segment index %d out of bounds (path has %d segments)", index, len(segments))
	}
	return strings.Join(segments[index:], "/"), nil
}

func resolveQueryVariable(v schema.Variable, targetURL string) (string, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse target URL: %w", err)
	}
	return parsed.Query().Get(v.Pattern), nil
}

// bootstrap hits the session bootstrap URL and extracts specified cookies
// and headers from the response. Returns a map of headers to carry forward
// into subsequent data API calls (cookies are merged into a Cookie header).
func (f *HTTPFetcher) bootstrap(ctx context.Context, session *schema.SessionConfig) (map[string]string, error) {
	method := session.BootstrapMethod
	if method == "" {
		method = "GET"
	}

	req, err := http.NewRequestWithContext(ctx, method, session.BootstrapURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bootstrap request creation failed: %w", err)
	}

	req.Header.Set("User-Agent", version.UserAgent())
	for k, v := range session.StaticHeaders {
		req.Header.Set(k, v)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bootstrap request failed: %w", err)
	}
	defer resp.Body.Close()

	// Don't read the body — we only need headers and cookies
	result := make(map[string]string)

	// Capture specified response headers
	for _, h := range session.CaptureHeaders {
		if val := resp.Header.Get(h); val != "" {
			result[h] = val
		}
	}

	// Capture specified cookies from Set-Cookie headers
	if len(session.CaptureCookies) > 0 {
		wantCookie := make(map[string]bool, len(session.CaptureCookies))
		for _, name := range session.CaptureCookies {
			wantCookie[name] = true
		}

		var cookies []string
		for _, c := range resp.Cookies() {
			if wantCookie[c.Name] {
				cookies = append(cookies, c.Name+"="+c.Value)
			}
		}
		if len(cookies) > 0 {
			result["Cookie"] = strings.Join(cookies, "; ")
		}
	}

	return result, nil
}
