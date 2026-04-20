package httpclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
)

// brightDataProxyURL returns the residential-proxy URL from the
// HERMAI_BRIGHTDATA_PROXY env var. Never hardcode a credential here — the
// file lives in a public repo and credential strings get scraped within
// hours of a commit. A past hardcoded value was rotated after exposure;
// contributors who want to run these tests locally must set the env var
// to their own BrightData residential-proxy URL:
//
//	export HERMAI_BRIGHTDATA_PROXY='http://USER:PASS@brd.superproxy.io:33335'
//	go test ./internal/httpclient/ -run TestStealthVsPlainViaProxy -v
//
// Tests skip cleanly when the env var is unset so CI stays green without
// requiring a proxy credential in the pipeline.
func brightDataProxyURL() string {
	return strings.TrimSpace(os.Getenv("HERMAI_BRIGHTDATA_PROXY"))
}

// TestStealthVsPlainViaProxy tests stealth vs plain HTTP through BrightData
// residential proxy. This is the real test: residential IP + Chrome TLS
// fingerprint should bypass Cloudflare where plain Go TLS gets blocked.
//
// Run with: go test -v -run TestStealthVsPlainViaProxy -count=1 ./internal/httpclient/
func TestStealthVsPlainViaProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping proxy test in short mode")
	}
	proxyURL := brightDataProxyURL()
	if proxyURL == "" {
		t.Skip("HERMAI_BRIGHTDATA_PROXY not set — skipping proxy test")
	}

	targets := []struct {
		name    string
		url     string
		wantStr string // string that should appear in successful response
	}{
		{
			"linux.do RSS (Cloudflare Turnstile-protected domain)",
			"https://linux.do/latest.rss",
			"<rss",
		},
		{
			"linux.do site.json (Cloudflare IP+TLS check)",
			"https://linux.do/site.json",
			"default_locale",
		},
		{
			"linux.do topic JSON (Cloudflare full Turnstile)",
			"https://linux.do/t/topic/1871540.json",
			"",
		},
		{
			"eBay search (Akamai)",
			"https://www.ebay.com/sch/i.html?_nkw=headphones",
			"headphones",
		},
	}

	for _, tt := range targets {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			opts := httpclient.Options{
				ProxyURL: proxyURL,
				Insecure: true,
				Timeout:  20 * time.Second,
			}

			// Plain client via proxy
			plainClient := httpclient.New(opts)
			pStatus, pSize, pHasStr := proxyFetch(ctx, plainClient, tt.url, tt.wantStr)

			// Stealth client via proxy
			stealthClient, err := httpclient.NewStealth(opts)
			if err != nil {
				t.Fatalf("NewStealth failed: %v", err)
			}
			sStatus, sSize, sHasStr := proxyFetch(ctx, stealthClient, tt.url, tt.wantStr)

			t.Logf("  plain:   HTTP %d, %s, has_content=%v", pStatus, humanSize(pSize), pHasStr)
			t.Logf("  stealth: HTTP %d, %s, has_content=%v", sStatus, humanSize(sSize), sHasStr)

			// Log wins
			if sHasStr && !pHasStr {
				t.Logf("  >> STEALTH WIN: got content that plain missed")
			}
			if sStatus == 200 && pStatus != 200 {
				t.Logf("  >> STEALTH WIN: HTTP 200 vs plain HTTP %d", pStatus)
			}
		})
	}
}

// TestFingerprintViaProxy checks what TLS fingerprint the proxy endpoint sees.
func TestFingerprintViaProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping proxy test in short mode")
	}
	proxyURL := brightDataProxyURL()
	if proxyURL == "" {
		t.Skip("HERMAI_BRIGHTDATA_PROXY not set — skipping proxy test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	opts := httpclient.Options{
		ProxyURL: proxyURL,
		Insecure: true,
		Timeout:  10 * time.Second,
	}

	plainClient := httpclient.New(opts)
	plainFP, err := getTLSFingerprint(ctx, plainClient)
	if errors.Is(err, errFingerprintServiceUnavailable) {
		t.Skipf("skipping: %v", err)
	}
	if err != nil {
		t.Fatalf("plain fingerprint: %v", err)
	}

	stealthClient, err := httpclient.NewStealth(opts)
	if err != nil {
		t.Fatalf("NewStealth failed: %v", err)
	}
	stealthFP, err := getTLSFingerprint(ctx, stealthClient)
	if errors.Is(err, errFingerprintServiceUnavailable) {
		t.Skipf("skipping: %v", err)
	}
	if err != nil {
		t.Fatalf("stealth fingerprint: %v", err)
	}

	t.Logf("Via residential proxy:")
	t.Logf("  Plain JA3:    %s", plainFP.ja3Hash)
	t.Logf("  Stealth JA3:  %s", stealthFP.ja3Hash)
	t.Logf("  Plain JA4:    %s", plainFP.ja4)
	t.Logf("  Stealth JA4:  %s", stealthFP.ja4)
	t.Logf("  Plain H2:     %s", plainFP.h2Fingerprint)
	t.Logf("  Stealth H2:   %s", stealthFP.h2Fingerprint)

	if plainFP.ja3Hash == stealthFP.ja3Hash {
		t.Errorf("JA3 hashes match — fingerprinting not working through proxy")
	}
}

func proxyFetch(ctx context.Context, client httpclient.Doer, url, wantStr string) (int, int, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	hasStr := wantStr != "" && strings.Contains(strings.ToLower(string(body)), strings.ToLower(wantStr))

	return resp.StatusCode, len(body), hasStr
}
