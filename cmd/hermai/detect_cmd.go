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

var platformSignals = []detectionSignal{
	{"WordPress", "wp-content", true, ""},
	{"WordPress", "wp-includes", true, ""},
	{"Shopify", "cdn.shopify.com", true, ""},
	{"Shopify", "myshopify.com", true, ""},
	{"Next.js", "__NEXT_DATA__", true, ""},
	{"Nuxt", "__NUXT__", true, ""},
	{"Nuxt", "__NUXT_DATA__", true, ""},
	{"React", "react-root", true, ""},
	{"React", "_reactRootContainer", true, ""},
	{"Angular", "ng-version", true, ""},
	{"Vue.js", "__vue__", true, ""},
	{"Remix", "__remixContext", true, ""},
	{"Gatsby", "gatsby-", true, ""},
	{"Wix", "wix.com", true, ""},
	{"Squarespace", "squarespace.com", true, ""},
	{"Drupal", "drupal", true, ""},
	{"Django", "csrfmiddlewaretoken", true, ""},
	{"Ruby on Rails", "csrf-token", true, ""},
	{"Laravel", "laravel_session", true, ""},
	{"ASP.NET", "__VIEWSTATE", true, ""},
	{"ASP.NET", "asp.net", false, "X-Powered-By"},
}

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
			bodyStr := strings.ToLower(string(bodyBytes))

			// Detect anti-bot systems
			antibotFound := matchSignals(antibotDetectionSignals, bodyStr, resp.Header)
			platformFound := matchSignals(platformSignals, bodyStr, resp.Header)

			output := map[string]any{
				"url":         targetURL,
				"status":      resp.StatusCode,
			}

			if len(antibotFound) > 0 {
				output["antibot"] = sortedKeys(antibotFound)
			}

			if len(platformFound) > 0 {
				output["platform"] = sortedKeys(platformFound)
			}

			statusBlocked := resp.StatusCode == 403 || resp.StatusCode == 429 || resp.StatusCode == 503
			// A challenge-only page has antibot markers but no real platform
			// content. Sites like TechCrunch serve real WordPress pages with
			// Cloudflare JS embedded — those are not blocked.
			challengePage := len(antibotFound) > 0 && len(platformFound) == 0
			if statusBlocked || challengePage {
				output["blocked"] = true
			}

			server := resp.Header.Get("Server")
			if server != "" {
				output["server"] = server
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

func matchSignals(signals []detectionSignal, bodyLower string, headers http.Header) map[string]bool {
	found := make(map[string]bool)
	for _, sig := range signals {
		if sig.InBody && strings.Contains(bodyLower, strings.ToLower(sig.Marker)) {
			found[sig.Name] = true
		}
		if sig.InHeader != "" {
			if val := headers.Get(sig.InHeader); val != "" && strings.Contains(strings.ToLower(val), strings.ToLower(sig.Marker)) {
				found[sig.Name] = true
			}
		}
	}
	return found
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
