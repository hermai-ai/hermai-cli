package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/probe"
	"github.com/spf13/cobra"
)

type wellKnownPath struct {
	Path        string `json:"path"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

var defaultWellKnownPaths = []wellKnownPath{
	{"/robots.txt", "robots", "Crawl rules and sitemap references"},
	{"/sitemap.xml", "sitemap", "XML sitemap index"},
	{"/feed", "rss", "RSS/Atom feed"},
	{"/feeds", "rss", "RSS/Atom feed directory"},
	{"/rss", "rss", "RSS feed"},
	{"/atom.xml", "rss", "Atom feed"},
	{"/feed.xml", "rss", "RSS feed"},
	{"/.well-known/openid-configuration", "oidc", "OpenID Connect discovery"},
	{"/graphql", "graphql", "GraphQL endpoint"},
	{"/api/graphql", "graphql", "GraphQL endpoint"},
	{"/oembed", "oembed", "oEmbed endpoint"},
	{"/wp-json/wp/v2/posts?per_page=1", "wordpress", "WordPress REST API"},
	{"/api", "api", "API root"},
	{"/api/v1", "api", "API v1 root"},
	{"/.json", "json", "JSON representation"},
}

type wellKnownResult struct {
	Path        string `json:"path"`
	URL         string `json:"url"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size_bytes"`
}

func newWellKnownCmd() *cobra.Command {
	var (
		stealth  bool
		proxyURL string
		timeout  string
		insecure bool
		format   string
	)

	cmd := &cobra.Command{
		Use:   "wellknown <domain>",
		Short: "Probe standard paths for APIs, feeds, sitemaps, and GraphQL",
		Long: `Wellknown probes a domain for standard discovery paths — robots.txt,
sitemaps, RSS feeds, GraphQL endpoints, oEmbed, WordPress API, and more.

Returns which paths exist and what content type they serve.

No API key required.

Examples:
  hermai wellknown example.com
  hermai wellknown --stealth youtube.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			domain := args[0]
			if !strings.Contains(domain, "://") {
				domain = "https://" + domain
			}
			parsed, err := url.Parse(domain)
			if err != nil {
				return fmt.Errorf("invalid domain: %w", err)
			}
			baseURL := parsed.Scheme + "://" + parsed.Host

			dur := 15 * time.Second
			if timeout != "" {
				d, err := time.ParseDuration(timeout)
				if err != nil {
					return fmt.Errorf("invalid --timeout: %w", err)
				}
				dur = d
			}

			opts := probe.Options{
				ProxyURL: proxyURL,
				Stealth:  stealth,
				Insecure: insecure,
				Timeout:  dur,
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			client := probe.NewClient(opts)

			type indexedResult struct {
				idx    int
				result wellKnownResult
			}
			resultCh := make(chan indexedResult, len(defaultWellKnownPaths))
			sem := make(chan struct{}, 5)

			for i, wk := range defaultWellKnownPaths {
				sem <- struct{}{}
				go func(idx int, wk wellKnownPath) {
					defer func() { <-sem }()

					fullURL := baseURL + wk.Path
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
					if err != nil {
						return
					}
					probe.SetBrowserHeaders(req)

					resp, err := client.Do(req)
					if err != nil {
						return
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()

					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						ct := resp.Header.Get("Content-Type")
						cl := int(resp.ContentLength)
						if cl < 0 {
							cl = 0
						}
						resultCh <- indexedResult{idx, wellKnownResult{
							Path:        wk.Path,
							URL:         fullURL,
							Type:        wk.Type,
							Description: wk.Description,
							Status:      resp.StatusCode,
							ContentType: ct,
							Size:        cl,
						}}
					}
				}(i, wk)
			}

			// Wait for all goroutines to finish
			for range cap(sem) {
				sem <- struct{}{}
			}
			close(resultCh)

			var found []wellKnownResult
			for r := range resultCh {
				_ = r.idx
				found = append(found, r.result)
			}

			output := map[string]any{
				"domain": parsed.Host,
				"probed": len(defaultWellKnownPaths),
				"found":  len(found),
			}
			if len(found) > 0 {
				output["results"] = found
			}

			return writeJSON(os.Stdout, output, format)
		},
	}

	cmd.Flags().BoolVar(&stealth, "stealth", false, "Use Chrome TLS fingerprinting")
	cmd.Flags().StringVar(&proxyURL, "proxy", "", "Proxy URL")
	cmd.Flags().StringVar(&timeout, "timeout", "15s", "Overall timeout")
	cmd.Flags().BoolVarP(&insecure, "insecure", "k", false, "Skip TLS verification")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or compact")

	return cmd
}
