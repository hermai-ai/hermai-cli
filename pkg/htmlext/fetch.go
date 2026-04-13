package htmlext

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
)

const maxHTMLSize = 5 * 1024 * 1024 // 5MB

// FetchHTML performs an HTTP GET and returns the response body as a string.
// It uses a browser-like User-Agent to avoid bot-detection blocks.
func FetchHTML(ctx context.Context, targetURL string, proxyURL string, insecure bool) (string, error) {
	client := httpclient.New(httpclient.Options{
		ProxyURL: proxyURL,
		Insecure: insecure,
		Timeout:  10 * time.Second,
	})
	return FetchHTMLWithClient(ctx, client, targetURL)
}

// FetchHTMLWithClient is like FetchHTML but accepts a caller-provided HTTP client.
func FetchHTMLWithClient(ctx context.Context, client httpclient.Doer, targetURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", fmt.Errorf("htmlext: failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", httpclient.BrowserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("htmlext: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("htmlext: HTTP %d from %s", resp.StatusCode, targetURL)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLSize))
	if err != nil {
		return "", fmt.Errorf("htmlext: failed to read response: %w", err)
	}

	return string(body), nil
}
