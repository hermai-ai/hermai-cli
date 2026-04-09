package httpclient_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
)

// TestStealthVsPlain compares TLS fingerprinted requests against plain Go HTTP.
// Run with: go test -v -run TestStealthVsPlain -count=1 ./internal/httpclient/
// Sites that block Go's default TLS fingerprint should succeed with stealth.
func TestStealthVsPlain(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	targets := []struct {
		name string
		url  string
		// minBody is the minimum response body length to consider success
		minBody int
	}{
		{"httpbin (baseline)", "https://httpbin.org/get", 100},
		{"linux.do RSS (Cloudflare)", "https://linux.do/latest.rss", 500},
		{"eBay homepage (Akamai)", "https://www.ebay.com/", 1000},
		{"Indeed homepage", "https://www.indeed.com/", 1000},
	}

	for _, tt := range targets {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			// Plain client
			plainClient := httpclient.New(httpclient.Options{Timeout: 10 * time.Second})
			plainStatus, plainSize := fetchStatus(ctx, plainClient, tt.url)

			// Stealth client
			stealthClient, err := httpclient.NewStealth(httpclient.Options{Timeout: 10 * time.Second})
			if err != nil {
				t.Fatalf("NewStealth failed: %v", err)
			}
			stealthStatus, stealthSize := fetchStatus(ctx, stealthClient, tt.url)

			t.Logf("  plain:   HTTP %d, %s", plainStatus, humanSize(plainSize))
			t.Logf("  stealth: HTTP %d, %s", stealthStatus, humanSize(stealthSize))

			// Stealth should at least get a response
			if stealthStatus == 0 {
				t.Errorf("stealth got no response")
			}
		})
	}
}

func fetchStatus(ctx context.Context, client httpclient.Doer, url string) (int, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))

	// Follow redirects manually for stealth client
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if loc != "" && !strings.HasPrefix(loc, "/") {
			req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
			if req2 != nil {
				req2.Header.Set("User-Agent", req.Header.Get("User-Agent"))
				resp2, err2 := client.Do(req2)
				if err2 == nil {
					defer resp2.Body.Close()
					body2, _ := io.ReadAll(io.LimitReader(resp2.Body, 5*1024*1024))
					return resp2.StatusCode, len(body2)
				}
			}
		}
	}

	return resp.StatusCode, len(body)
}

func humanSize(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
}
