package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/probe"
	"github.com/spf13/cobra"
)

type detectionSignal struct {
	Name     string
	Marker   string
	InBody   bool
	InHeader string
}

// platformSignals is intentionally minimal. Maintaining a comprehensive
// platform catalogue in the binary doesn't scale — thousands of platforms
// exist and new ones ship constantly. Instead, detect extracts structural
// evidence (script hosts, CDN preconnects, generator meta, x-powered-by,
// server header) that the agent composes into a platform identification.
// A richer list can come from the registry later.

var antibotDetectionSignals = []detectionSignal{
	// CF-RAY header excluded: present on ALL Cloudflare-proxied sites, not just challenges.
	{"Cloudflare", "cf_chl_opt", true, ""},
	{"Cloudflare", "cf-browser-verification", true, ""},
	{"Cloudflare", "challenges.cloudflare.com", true, ""},
	{"DataDome", "datadome", true, ""},
	{"DataDome", "geo.captcha-delivery.com", true, ""},
	{"PerimeterX", "perimeterx", true, ""},
	{"PerimeterX", "_pxhd", false, "Set-Cookie"},
	{"Akamai", "akamai", true, ""},
	{"Akamai", "_abck", false, "Set-Cookie"},
	{"AWS WAF", "awswaf", true, ""},
	{"AWS WAF", "aws-waf-token", true, ""},
	{"Imperva", "incapsula", true, ""},
	{"Imperva", "incap_ses", false, "Set-Cookie"},
	{"hCaptcha", "hcaptcha.com", true, ""},
	{"reCAPTCHA", "recaptcha", true, ""},
	{"TikTok Signing", "webmssdk", true, ""},
	{"TikTok Signing", "byted_acrawler", true, ""},
}

func newDetectCmd() *cobra.Command {
	var (
		stealth  bool
		proxyURL string
		timeout  string
		insecure bool
		format   string
	)

	cmd := &cobra.Command{
		Use:   "detect <url>",
		Short: "Detect anti-bot systems and platform/CMS for a URL",
		Long: `Detect fetches a URL and analyzes the response for anti-bot systems
(Cloudflare, DataDome, PerimeterX, AWS WAF, etc.) and platform/CMS
signatures (WordPress, Shopify, Next.js, React, etc.).

No API key required.

Examples:
  hermai detect https://www.booking.com
  hermai detect --stealth https://www.tiktok.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetURL := args[0]

			dur, err := parseTimeout(timeout, 10*time.Second)
			if err != nil {
				return err
			}
			opts := buildProbeOpts(proxyURL, stealth, insecure, dur)

			ctx, cancel := signalContext(dur)
			defer cancel()

			client := probe.NewClient(opts)

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}
			probe.SetBrowserHeaders(req)

			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
			if err != nil {
				return fmt.Errorf("failed to read response: %w", err)
			}
			body := string(bodyBytes)
			bodyLower := strings.ToLower(body)

			output := map[string]any{
				"url":    targetURL,
				"status": resp.StatusCode,
			}
			if server := resp.Header.Get("Server"); server != "" {
				output["server"] = server
			}
			if xpb := resp.Header.Get("X-Powered-By"); xpb != "" {
				output["x_powered_by"] = xpb
			}

			// Structural evidence — mechanical extractions the agent uses
			// to identify the platform without a hardcoded catalogue.
			if gen := extractMetaGenerator(body); gen != "" {
				output["meta_generator"] = gen
			}
			if hosts := extractScriptHosts(body); len(hosts) > 0 {
				output["script_hosts"] = hosts
			}
			if hosts := extractPreconnectHosts(body); len(hosts) > 0 {
				output["preconnect_hosts"] = hosts
			}

			// Anti-bot markers remain curated: small, stable, well-known set
			// where early warning matters more than exhaustive coverage.
			if hits := collectHits(antibotDetectionSignals, bodyLower, resp.Header); len(hits) > 0 {
				output["antibot_signals"] = hits
			}

			// blocking_indicators: mechanical signals only. Agent decides
			// what they mean in context.
			var indicators []string
			switch resp.StatusCode {
			case 403:
				indicators = append(indicators, "status:403")
			case 429:
				indicators = append(indicators, "status:429")
			case 503:
				indicators = append(indicators, "status:503")
			case 202:
				indicators = append(indicators, "status:202 (AWS WAF challenge pattern)")
			}
			if len(bodyBytes) < 50*1024 {
				indicators = append(indicators, fmt.Sprintf("small_body (%d bytes)", len(bodyBytes)))
			}
			if len(indicators) > 0 {
				output["blocking_indicators"] = indicators
			}

			return writeJSON(os.Stdout, output, format)
		},
	}

	cmd.Flags().BoolVar(&stealth, "stealth", false, "Use Chrome TLS fingerprinting")
	cmd.Flags().StringVar(&proxyURL, "proxy", "", "Proxy URL")
	cmd.Flags().StringVar(&timeout, "timeout", "10s", "Request timeout")
	cmd.Flags().BoolVarP(&insecure, "insecure", "k", false, "Skip TLS verification")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or compact")

	return cmd
}

// extractMetaGenerator returns the content of <meta name="generator">
// which many platforms self-identify with (WordPress, Drupal, Hugo, etc.).
func extractMetaGenerator(html string) string {
	return firstMetaContent(html, "generator")
}

func firstMetaContent(html, name string) string {
	// Minimal parse: find <meta name="NAME" content="...">
	lower := strings.ToLower(html)
	needle := `name="` + strings.ToLower(name) + `"`
	idx := strings.Index(lower, needle)
	if idx < 0 {
		needle = `name='` + strings.ToLower(name) + `'`
		idx = strings.Index(lower, needle)
		if idx < 0 {
			return ""
		}
	}
	// Look backward for the start of the meta tag, forward for content=""
	tagStart := strings.LastIndex(lower[:idx], "<meta")
	tagEnd := strings.Index(lower[idx:], ">")
	if tagStart < 0 || tagEnd < 0 {
		return ""
	}
	tag := html[tagStart : idx+tagEnd+1]
	for _, quote := range []string{`content="`, `content='`} {
		if ci := strings.Index(strings.ToLower(tag), quote); ci >= 0 {
			rest := tag[ci+len(quote):]
			closeQuote := quote[len(quote)-1]
			end := strings.IndexByte(rest, closeQuote)
			if end > 0 {
				return rest[:end]
			}
		}
	}
	return ""
}

// extractScriptHosts returns unique, sorted hostnames of <script src="...">.
// Agents use these to identify platforms (cdn.shoplineapp.com → Shopline,
// cdn.shopify.com → Shopify, etc.) without a hardcoded catalogue.
func extractScriptHosts(html string) []string {
	return uniqueHostsFrom(html, `src="`, `src='`)
}

// extractPreconnectHosts returns hosts from <link rel="preconnect"> and
// <link rel="dns-prefetch"> — strong platform signals since they're
// curated by the site itself.
func extractPreconnectHosts(html string) []string {
	lower := strings.ToLower(html)
	var hosts []string
	seen := make(map[string]bool)
	for _, rel := range []string{`rel="preconnect"`, `rel="dns-prefetch"`, `rel='preconnect'`, `rel='dns-prefetch'`} {
		start := 0
		for {
			i := strings.Index(lower[start:], rel)
			if i < 0 {
				break
			}
			i += start
			// find the <link ...> around this match
			tagStart := strings.LastIndex(lower[:i], "<link")
			tagEnd := strings.Index(lower[i:], ">")
			if tagStart < 0 || tagEnd < 0 {
				start = i + len(rel)
				continue
			}
			tag := html[tagStart : i+tagEnd+1]
			for _, quote := range []string{`href="`, `href='`} {
				if hi := strings.Index(strings.ToLower(tag), quote); hi >= 0 {
					rest := tag[hi+len(quote):]
					closeQuote := quote[len(quote)-1]
					end := strings.IndexByte(rest, closeQuote)
					if end > 0 {
						if h := hostOnly(rest[:end]); h != "" && !seen[h] {
							seen[h] = true
							hosts = append(hosts, h)
						}
					}
				}
			}
			start = i + len(rel)
		}
	}
	sort.Strings(hosts)
	return hosts
}

func uniqueHostsFrom(html string, quotes ...string) []string {
	var hosts []string
	seen := make(map[string]bool)
	for _, prefix := range quotes {
		start := 0
		for {
			i := strings.Index(html[start:], prefix)
			if i < 0 {
				break
			}
			i += start + len(prefix)
			closeQuote := prefix[len(prefix)-1]
			end := strings.IndexByte(html[i:], closeQuote)
			if end < 0 {
				break
			}
			if h := hostOnly(html[i : i+end]); h != "" && !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
			start = i + end
		}
	}
	sort.Strings(hosts)
	return hosts
}

// hostOnly extracts the host from a URL. Returns "" for relative URLs
// (not useful as platform signals).
func hostOnly(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "//") {
		return ""
	}
	rawURL = strings.TrimPrefix(rawURL, "http://")
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "//")
	if i := strings.IndexAny(rawURL, "/?#"); i >= 0 {
		rawURL = rawURL[:i]
	}
	return rawURL
}

type signalHit struct {
	Name     string `json:"name"`
	Location string `json:"location"` // "body" or "header:<name>"
	Marker   string `json:"marker"`
}

// collectHits returns one entry per marker match, preserving evidence.
// Multiple hits for the same platform name are kept — the caller decides
// how to weight "one string reference" vs "multiple strong markers".
func collectHits(signals []detectionSignal, bodyLower string, headers http.Header) []signalHit {
	var hits []signalHit
	for _, sig := range signals {
		if sig.InBody && strings.Contains(bodyLower, strings.ToLower(sig.Marker)) {
			hits = append(hits, signalHit{
				Name:     sig.Name,
				Location: "body",
				Marker:   sig.Marker,
			})
		}
		if sig.InHeader != "" {
			if val := headers.Get(sig.InHeader); val != "" && strings.Contains(strings.ToLower(val), strings.ToLower(sig.Marker)) {
				hits = append(hits, signalHit{
					Name:     sig.Name,
					Location: "header:" + sig.InHeader,
					Marker:   sig.Marker,
				})
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Name != hits[j].Name {
			return hits[i].Name < hits[j].Name
		}
		return hits[i].Location < hits[j].Location
	})
	return hits
}
