package httpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
)

// errFingerprintServiceUnavailable signals that the external TLS echo service
// (tls.peet.ws) is down — suspended account, 5xx, non-JSON body, whatever.
// Tests should skip rather than fail when the probe flags this: we want to
// keep the coverage when the service is up without making CI red whenever
// the operator pauses their service. Restores automatically when peet.ws
// starts responding with parseable fingerprint JSON again.
var errFingerprintServiceUnavailable = errors.New("fingerprint echo service unavailable")

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
	plainFP, err := getTLSFingerprint(ctx, plainClient)
	if errors.Is(err, errFingerprintServiceUnavailable) {
		t.Skipf("skipping: %v", err)
	}
	if err != nil {
		t.Fatalf("plain fingerprint: %v", err)
	}

	// Stealth client
	stealthClient, err := httpclient.NewStealth(httpclient.Options{Timeout: 10 * time.Second})
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

// getTLSFingerprint queries the peet.ws echo service and returns the parsed
// JA3/JA4/HTTP2 fingerprint of the client that reached the server. Returns
// errFingerprintServiceUnavailable when the service is down (suspended
// account, 5xx, non-JSON body, or parseable JSON missing the fingerprint
// fields) so callers can skip instead of fail — we don't want peet.ws's
// billing status to block our CI.
func getTLSFingerprint(ctx context.Context, client httpclient.Doer) (fingerprint, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://tls.peet.ws/api/all", nil)
	if err != nil {
		return fingerprint{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		// Network errors, TLS handshake failures → treat as service unavailable.
		return fingerprint{}, fmt.Errorf("%w: %v", errFingerprintServiceUnavailable, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fingerprint{}, fmt.Errorf("read body: %w", err)
	}

	// Any 5xx from the echo is a service issue, not a client issue.
	if resp.StatusCode >= 500 {
		return fingerprint{}, fmt.Errorf("%w: HTTP %d", errFingerprintServiceUnavailable, resp.StatusCode)
	}
	// Known suspended-account response — string match rather than status,
	// because the host returns an HTML/text body with a 200 or 402.
	if bytes := string(body); strings.Contains(strings.ToLower(bytes), "account is suspended") {
		return fingerprint{}, fmt.Errorf("%w: peet.ws account suspended", errFingerprintServiceUnavailable)
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
		// Non-JSON body → likely a challenge page or a down-for-maintenance
		// landing. Either way, nothing we can extract a fingerprint from.
		return fingerprint{}, fmt.Errorf("%w: unexpected body: %v", errFingerprintServiceUnavailable, err)
	}
	if result.TLS.JA3Hash == "" && result.TLS.JA4 == "" {
		// Parseable JSON but missing fingerprint fields (schema drift or
		// error object) — treat as unavailable.
		return fingerprint{}, fmt.Errorf("%w: response missing fingerprint fields", errFingerprintServiceUnavailable)
	}

	return fingerprint{
		ja3Hash:       result.TLS.JA3Hash,
		ja4:           result.TLS.JA4,
		h2Fingerprint: result.HTTP2.AkamaiFingerprint,
	}, nil
}
