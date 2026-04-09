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

// TestDiscourseEndpoints probes various Discourse API endpoints on linux.do
// to find which ones bypass Cloudflare Turnstile.
//
// Discourse has many undocumented or less-known endpoints that the site admin
// may not have put behind Cloudflare WAF rules.
func TestDiscourseEndpoints(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	// Use stealth + residential proxy for best chance
	opts := httpclient.Options{
		ProxyURL: brightDataProxy,
		Insecure: true,
		Timeout:  15 * time.Second,
	}
	client, err := httpclient.NewStealth(opts)
	if err != nil {
		t.Fatalf("NewStealth failed: %v", err)
	}

	topicID := "1871540"

	endpoints := []struct {
		name    string
		url     string
		headers map[string]string
		wantStr string
	}{
		// Known working
		{"site.json", "https://linux.do/site.json", nil, "default_locale"},
		{"latest.rss", "https://linux.do/latest.rss", nil, "<rss"},

		// Known blocked (full Turnstile)
		{"topic.json (direct)", "https://linux.do/t/topic/" + topicID + ".json", nil, ""},

		// Alternative Discourse endpoints to probe
		{"posts by ID", "https://linux.do/posts/" + topicID + ".json", nil, ""},
		{"topic posts.json", "https://linux.do/t/" + topicID + "/posts.json", nil, ""},
		{"raw post", "https://linux.do/raw/" + topicID, nil, ""},
		{"topic feed", "https://linux.do/t/topic/" + topicID + ".rss", nil, "<rss"},
		{"categories.json", "https://linux.do/categories.json", nil, "category_list"},
		{"latest.json", "https://linux.do/latest.json", nil, "topic_list"},
		{"top.json", "https://linux.do/top.json", nil, "topic_list"},
		{"search.json", "https://linux.do/search.json?q=test", nil, ""},
		{"tag list", "https://linux.do/tags.json", nil, ""},
		{"about.json", "https://linux.do/about.json", nil, "about"},
		{"user directory", "https://linux.do/directory_items.json?period=weekly&order=likes_received", nil, ""},

		// Try with Accept header instead of .json suffix
		{"topic via Accept header", "https://linux.do/t/topic/" + topicID, map[string]string{"Accept": "application/json"}, ""},

		// Try Discourse API key header (empty, just to see if path changes)
		{"topic with Api-Key header", "https://linux.do/t/topic/" + topicID + ".json", map[string]string{"Api-Key": "test", "Api-Username": "system"}, ""},

		// Embed endpoint (used for oEmbed)
		{"embed info", "https://linux.do/t/topic/" + topicID + "/summary.json", nil, ""},

		// WordPress-style feed
		{"feed atom", "https://linux.do/posts.rss", nil, "<rss"},
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.url, nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
			req.Header.Set("Accept-Language", "en-US,en;q=0.9")
			if ep.headers != nil {
				for k, v := range ep.headers {
					req.Header.Set(k, v)
				}
			} else {
				req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Logf("  ERROR: %v", err)
				return
			}
			defer resp.Body.Close()

			body, _ := io.ReadAll(io.LimitReader(resp.Body, 100*1024))
			bodyStr := string(body)

			hasContent := ep.wantStr != "" && strings.Contains(strings.ToLower(bodyStr), strings.ToLower(ep.wantStr))
			isJSON := strings.Contains(resp.Header.Get("Content-Type"), "json")
			isChallenge := strings.Contains(bodyStr, "cf-browser-verification") ||
				strings.Contains(bodyStr, "cf_chl_opt") ||
				strings.Contains(bodyStr, "Just a moment")

			status := "BLOCKED"
			if resp.StatusCode == 200 && !isChallenge {
				if isJSON {
					status = "OK (JSON)"
				} else if hasContent {
					status = "OK (content)"
				} else if len(body) > 1000 {
					status = "OK (large response)"
				} else {
					status = "OK (small)"
				}
			} else if resp.StatusCode == 403 {
				status = "403 FORBIDDEN"
			} else if resp.StatusCode == 302 || resp.StatusCode == 301 {
				status = fmt.Sprintf("REDIRECT → %s", resp.Header.Get("Location"))
			} else if isChallenge {
				status = "CHALLENGE PAGE"
			} else {
				status = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}

			t.Logf("  [%s] %s (%s)", status, ep.url, humanSize(len(body)))
		})
	}
}
