package actions

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

var browserUserAgent = httpclient.BrowserUserAgent

type pageResponse struct {
	FinalURL    string
	StatusCode  int
	ContentType string
	Body        string
}

func fetchPage(ctx context.Context, targetURL string, opts HTTPOptions) (*pageResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range opts.HeaderOverrides {
		req.Header.Set(key, value)
	}
	return doPageRequest(newPageClient(opts), req)
}

// newHTTPClient returns a concrete *http.Client with cookie jar.
// Used by clearance.go which needs Jar access.
func newHTTPClient(opts HTTPOptions) *http.Client {
	return httpclient.New(httpclient.Options{
		ProxyURL: opts.ProxyURL,
		Insecure: opts.Insecure,
		WithJar:  true,
	})
}

// newPageClient returns a Doer for hitting target websites.
// When Stealth is enabled, uses Chrome TLS+HTTP/2 fingerprinting.
func newPageClient(opts HTTPOptions) httpclient.Doer {
	if opts.Stealth {
		return httpclient.NewStealthOrFallback(httpclient.Options{
			ProxyURL: opts.ProxyURL,
			Insecure: opts.Insecure,
		})
	}
	return newHTTPClient(opts)
}

func doPageRequest(client httpclient.Doer, req *http.Request) (*pageResponse, error) {
	page, err := doPageRequestRaw(client, req)
	if err != nil {
		return nil, err
	}
	if page.StatusCode < 200 || page.StatusCode >= 300 {
		return nil, fmt.Errorf("request failed with HTTP %d", page.StatusCode)
	}
	return page, nil
}

// doPageRequestRaw executes the request and returns the response even on
// non-2xx status codes. This allows callers to inspect the body for anti-bot
// challenge detection before deciding whether to treat it as an error.
func doPageRequestRaw(client httpclient.Doer, req *http.Request) (*pageResponse, error) {
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, err
	}

	return &pageResponse{
		FinalURL:    resp.Request.URL.String(),
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        string(body),
	}, nil
}

// executePageRequestAllowChallenge is like executePageRequest but returns the
// response even on anti-bot challenge status codes (403, 503) so the caller
// can detect and handle them.
func executePageRequestAllowChallenge(ctx context.Context, requestURL, method string, spec []schema.ActionParam, params map[string]string, opts HTTPOptions) (*pageResponse, error) {
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
		return doPageRequestRaw(client, req)
	default:
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		for key, value := range opts.HeaderOverrides {
			req.Header.Set(key, value)
		}
		return doPageRequestRaw(client, req)
	}
}

func resolveActionTemplate(template string, spec []schema.ActionParam, params map[string]string) string {
	resolved := template
	for _, param := range spec {
		value := resolveParamValue(param, params)
		rawToken := "{" + param.Name + "}"
		encodedToken := url.QueryEscape(rawToken)
		if param.In == "url" {
			resolved = strings.ReplaceAll(resolved, rawToken, value)
			resolved = strings.ReplaceAll(resolved, encodedToken, url.QueryEscape(value))
			continue
		}
		escapedValue := url.QueryEscape(value)
		resolved = strings.ReplaceAll(resolved, rawToken, escapedValue)
		resolved = strings.ReplaceAll(resolved, encodedToken, escapedValue)
	}
	return resolved
}

func resolveParamValue(param schema.ActionParam, params map[string]string) string {
	if value := strings.TrimSpace(params[param.Name]); value != "" {
		return value
	}
	return param.Default
}

func ensureSameHost(baseURL, nextURL string) error {
	base, err := url.Parse(baseURL)
	if err != nil {
		return err
	}
	next, err := url.Parse(nextURL)
	if err != nil {
		return err
	}
	if next.Host != "" && !strings.EqualFold(base.Host, next.Host) {
		return fmt.Errorf("navigate only supports the same host: %s != %s", next.Host, base.Host)
	}
	return nil
}
