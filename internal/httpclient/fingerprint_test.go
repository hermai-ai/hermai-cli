package httpclient_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
)

// TestTLSFingerprint verifies the stealth client presents a Chrome-like
// TLS fingerprint by checking against tls.peet.ws/api/all.
func TestTLSFingerprint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Plain Go client
	plainClient := httpclient.New(httpclient.Options{Timeout: 10 * time.Second})
	plainFP := getTLSFingerprint(ctx, t, plainClient)

	// Stealth client
	stealthClient, err := httpclient.NewStealth(httpclient.Options{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewStealth failed: %v", err)
	}
	stealthFP := getTLSFingerprint(ctx, t, stealthClient)

	t.Logf("Plain JA3 hash:   %s", plainFP.ja3Hash)
	t.Logf("Stealth JA3 hash: %s", stealthFP.ja3Hash)
	t.Logf("Plain JA4:        %s", plainFP.ja4)
	t.Logf("Stealth JA4:      %s", stealthFP.ja4)
	t.Logf("Plain H2 fp:      %s", plainFP.h2Fingerprint)
	t.Logf("Stealth H2 fp:    %s", stealthFP.h2Fingerprint)

	// The fingerprints should differ — stealth should look like Chrome
	if plainFP.ja3Hash == stealthFP.ja3Hash {
		t.Errorf("JA3 hashes are the same — stealth TLS fingerprinting may not be working")
	}

	// HTTP/2 fingerprint should also differ (tls-client patches HTTP/2 SETTINGS)
	if plainFP.h2Fingerprint != "" && stealthFP.h2Fingerprint != "" {
		if plainFP.h2Fingerprint == stealthFP.h2Fingerprint {
			t.Logf("WARNING: HTTP/2 fingerprints are the same — HTTP/2 spoofing may not be active")
		} else {
			t.Logf("HTTP/2 fingerprints differ — HTTP/2 spoofing is active")
		}
	}
}

type fingerprint struct {
	ja3Hash       string
	ja4           string
	h2Fingerprint string
}

func getTLSFingerprint(ctx context.Context, t *testing.T, client httpclient.Doer) fingerprint {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://tls.peet.ws/api/all", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	var result struct {
		TLS struct {
			JA3Hash string `json:"ja3_hash"`
			JA4     string `json:"ja4"`
		} `json:"tls"`
		HTTP2 struct {
			AkamaiFingerprint string `json:"akamai_fingerprint"`
		} `json:"http2"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		// Try to find JA3 in plain text
		s := string(body)
		if strings.Contains(s, "ja3_hash") {
			t.Logf("Raw response (first 500 chars): %s", s[:min(500, len(s))])
		}
		t.Fatalf("failed to parse response: %v", err)
	}

	return fingerprint{
		ja3Hash:       result.TLS.JA3Hash,
		ja4:           result.TLS.JA4,
		h2Fingerprint: result.HTTP2.AkamaiFingerprint,
	}
}
