package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/cache"
	"github.com/hermai-ai/hermai-cli/pkg/fetcher"
	"github.com/hermai-ai/hermai-cli/pkg/htmlext"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// HTTPOptions configures browserless HTTP execution.
type HTTPOptions struct {
	ProxyURL        string
	Insecure        bool
	Stealth         bool          // use TLS+HTTP/2 fingerprinting (Chrome profile)
	HeaderOverrides map[string]string
	BrowserPath     string        // path to Chromium binary for anti-bot fallback (empty = auto-detect)
	NoBrowser       bool          // disable browser anti-bot fallback entirely
	CacheDir        string        // schema cache dir for persisting clearance cookies
	Cache           cache.Service // optional pre-built cache (takes precedence over CacheDir)
}

// ExecutionResult is returned by browserless action execution.
type ExecutionResult struct {
	URL         string          `json:"url"`
	Action      string          `json:"action"`
	Kind        string          `json:"kind"`
	Transport   string          `json:"transport"`
	Source      string          `json:"source"`
	Content     any             `json:"content,omitempty"`
	Data        any             `json:"data,omitempty"`
	NextActions []schema.Action `json:"next_actions,omitempty"`
	Metadata    Metadata        `json:"metadata"`
}

// Metadata captures browserless execution details.
type Metadata struct {
	StatusCode     int   `json:"status_code"`
	TotalLatencyMs int64 `json:"total_latency_ms"`
}

// ExecuteAction executes one browserless action.
func ExecuteAction(ctx context.Context, targetURL string, action schema.Action, params map[string]string, opts HTTPOptions) (*ExecutionResult, error) {
	start := time.Now()

	switch action.Transport {
	case schema.ActionTransportAPICall:
		return executeAPIAction(ctx, targetURL, action, params, opts, start)
	case schema.ActionTransportHTTPGet:
		return executeHTTPAction(ctx, targetURL, action, params, opts, start, "GET")
	case schema.ActionTransportHTTPPostForm:
		return executeHTTPAction(ctx, targetURL, action, params, opts, start, "POST")
	default:
		return nil, fmt.Errorf("unsupported action transport: %s", action.Transport)
	}
}

func executeAPIAction(ctx context.Context, targetURL string, action schema.Action, params map[string]string, opts HTTPOptions, start time.Time) (*ExecutionResult, error) {
	endpoint := schema.Endpoint{
		Name:           action.Name,
		Description:    action.Description,
		Method:         firstNonEmpty(action.Method, "GET"),
		URLTemplate:    resolveActionTemplate(action.URLTemplate, action.Params, params),
		Headers:        action.Headers,
		ResponseSchema: nil,
	}

	f := fetcher.NewHTTPFetcherWithProxy(opts.ProxyURL, opts.Insecure)
	result, err := f.Fetch(ctx, &schema.Schema{
		ID:        "action",
		Version:   1,
		Endpoints: []schema.Endpoint{endpoint},
	}, targetURL, fetcher.FetchOpts{
		ProxyURL:        opts.ProxyURL,
		Insecure:        opts.Insecure,
		Stealth:         opts.Stealth,
		HeaderOverrides: opts.HeaderOverrides,
	})
	if err != nil {
		return nil, err
	}

	return &ExecutionResult{
		URL:       targetURL,
		Action:    action.Name,
		Kind:      action.Kind,
		Transport: action.Transport,
		Source:    "api",
		Data:      result.Data,
		Metadata: Metadata{
			StatusCode:     http.StatusOK,
			TotalLatencyMs: time.Since(start).Milliseconds(),
		},
	}, nil
}

func executeHTTPAction(ctx context.Context, targetURL string, action schema.Action, params map[string]string, opts HTTPOptions, start time.Time, method string) (*ExecutionResult, error) {
	requestURL := resolveActionTemplate(action.URLTemplate, action.Params, params)
	if action.Name == "navigate" {
		if err := ensureSameHost(targetURL, requestURL); err != nil {
			return nil, err
		}
	}

	c := resolveCache(opts)

	// Session bootstrap: obtain clearance cookies before the request
	clearance := obtainClearance(ctx, targetURL, c, opts)
	optsWithClearance := mergeClearanceCookies(opts, clearance)

	page, err := executePageRequestAllowChallenge(ctx, requestURL, method, action.Params, params, optsWithClearance)
	if err != nil {
		return nil, err
	}

	// Anti-bot fallback: if blocked, try browser clearance and retry once
	if isAntiBotChallenge(page.StatusCode, page.Body) && !opts.NoBrowser {
		cookies, browserErr := browserClearance(ctx, requestURL, opts.BrowserPath)
		if browserErr == nil && len(cookies) > 0 {
			persistClearance(ctx, targetURL, cookies, c)
			browserResult := &ClearanceResult{Cookies: cookies, Source: "browser"}
			retryOpts := mergeClearanceCookies(opts, browserResult)
			retryPage, retryErr := executePageRequestAllowChallenge(ctx, requestURL, method, action.Params, params, retryOpts)
			if retryErr == nil && !isAntiBotChallenge(retryPage.StatusCode, retryPage.Body) {
				page = retryPage
			}
		}
	}

	return buildHTTPResult(page, action, start)
}

// buildHTTPResult converts a pageResponse into an ExecutionResult.
func buildHTTPResult(page *pageResponse, action schema.Action, start time.Time) (*ExecutionResult, error) {
	contentType := strings.ToLower(page.ContentType)
	if strings.Contains(contentType, "json") {
		var data any
		if err := json.Unmarshal([]byte(page.Body), &data); err != nil {
			return nil, fmt.Errorf("response was JSON-like but could not be parsed: %w", err)
		}
		return &ExecutionResult{
			URL:       page.FinalURL,
			Action:    action.Name,
			Kind:      action.Kind,
			Transport: action.Transport,
			Source:    "api",
			Data:      data,
			Metadata: Metadata{
				StatusCode:     page.StatusCode,
				TotalLatencyMs: time.Since(start).Milliseconds(),
			},
		}, nil
	}

	content := htmlext.Extract(page.Body, page.FinalURL)
	nextActions := dedupeActions(compileHTMLActions(page.FinalURL, content))
	return &ExecutionResult{
		URL:         page.FinalURL,
		Action:      action.Name,
		Kind:        action.Kind,
		Transport:   action.Transport,
		Source:      "html_extraction",
		Content:     content,
		NextActions: nextActions,
		Metadata: Metadata{
			StatusCode:     page.StatusCode,
			TotalLatencyMs: time.Since(start).Milliseconds(),
		},
	}, nil
}

// mergeClearanceCookies adds clearance cookies to opts.HeaderOverrides["Cookie"].
func mergeClearanceCookies(opts HTTPOptions, cr *ClearanceResult) HTTPOptions {
	header := cr.cookieHeader()
	if header == "" {
		return opts
	}
	merged := HTTPOptions{
		ProxyURL:        opts.ProxyURL,
		Insecure:        opts.Insecure,
		Stealth:         opts.Stealth,
		BrowserPath:     opts.BrowserPath,
		NoBrowser:       opts.NoBrowser,
		CacheDir:        opts.CacheDir,
		Cache:           opts.Cache,
		HeaderOverrides: make(map[string]string),
	}
	for k, v := range opts.HeaderOverrides {
		merged.HeaderOverrides[k] = v
	}
	if existing := merged.HeaderOverrides["Cookie"]; existing != "" {
		merged.HeaderOverrides["Cookie"] = existing + "; " + header
	} else {
		merged.HeaderOverrides["Cookie"] = header
	}
	return merged
}

// resolveCache returns the cache service from opts, creating a FileCache if needed.
func resolveCache(opts HTTPOptions) cache.Service {
	if opts.Cache != nil {
		return opts.Cache
	}
	if opts.CacheDir != "" {
		return cache.NewFileCache(opts.CacheDir, 0)
	}
	return nil
}

func executePageRequest(ctx context.Context, requestURL, method string, spec []schema.ActionParam, params map[string]string, opts HTTPOptions) (*pageResponse, error) {
	client := newPageClient(opts)
	switch method {
	case "POST":
		form := url.Values{}
		for _, param := range spec {
			if param.In != "form" {
				continue
			}
			form.Set(param.Name, resolveParamValue(param, params))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for key, value := range opts.HeaderOverrides {
			req.Header.Set(key, value)
		}
		return doPageRequest(client, req)
	default:
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		for key, value := range opts.HeaderOverrides {
			req.Header.Set(key, value)
		}
		return doPageRequest(client, req)
	}
}
