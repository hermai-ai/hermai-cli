package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/probe"
	"github.com/spf13/cobra"
)

func newProbeCmd() *cobra.Command {
	var (
		stealth  bool
		proxyURL string
		timeout  string
		insecure bool
		body     bool
		save     string
		format   string
	)

	cmd := &cobra.Command{
		Use:   "probe <url>",
		Short: "TLS-fingerprinted HTTP fetch with anti-bot detection",
		Long: `Probe performs a TLS-fingerprinted HTTP request and runs discovery
strategies to find direct API access patterns. Returns response metadata,
anti-bot detection results, and any discovered JSON endpoints.

Use --body to output raw HTML (pipe to 'hermai extract' for pattern analysis).
Use --stealth to force Chrome TLS fingerprinting on the first attempt.

No API key required — all operations are deterministic.

Examples:
  hermai probe https://www.youtube.com/watch?v=dQw4w9WgXcQ
  hermai probe --body https://example.com | hermai extract
  hermai probe --stealth https://www.amazon.com/dp/B0CX23V2ZK
  hermai probe --save page.html https://example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetURL := args[0]

			dur, err := parseTimeout(timeout, 10*time.Second)
			if err != nil {
				return err
			}
			opts := buildProbeOpts(proxyURL, stealth, insecure, dur)

			ctx, cancel := signalContext()
			defer cancel()

			if body {
				return probeBodyMode(ctx, targetURL, opts)
			}

			result, err := probe.Probe(ctx, targetURL, opts)
			if err != nil {
				return fmt.Errorf("probe failed: %w", err)
			}

			if save != "" && result.HTMLBody != "" {
				if err := os.WriteFile(save, []byte(result.HTMLBody), 0o644); err != nil {
					return fmt.Errorf("failed to save HTML to %s: %w", save, err)
				}
				fmt.Fprintf(os.Stderr, "saved %d bytes to %s\n", len(result.HTMLBody), save)
			}

			output := buildProbeOutput(targetURL, result)
			return writeJSON(os.Stdout, output, format)
		},
	}

	cmd.Flags().BoolVar(&stealth, "stealth", false, "Force Chrome TLS fingerprinting on first attempt")
	cmd.Flags().StringVar(&proxyURL, "proxy", "", "Proxy URL (http:// or socks5://)")
	cmd.Flags().StringVar(&timeout, "timeout", "10s", "Request timeout (e.g. 5s, 30s)")
	cmd.Flags().BoolVarP(&insecure, "insecure", "k", false, "Skip TLS certificate verification")
	cmd.Flags().BoolVar(&body, "body", false, "Output raw HTML body to stdout (for piping to extract)")
	cmd.Flags().StringVar(&save, "save", "", "Save HTML body to file (alongside JSON output)")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json (indented) or compact")

	return cmd
}

func probeBodyMode(ctx context.Context, targetURL string, opts probe.Options) error {
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

	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "warning: HTTP %d\n", resp.StatusCode)
	}

	_, err = io.Copy(os.Stdout, io.LimitReader(resp.Body, maxHTMLInputSize))
	return err
}

func buildProbeOutput(targetURL string, result *probe.Result) map[string]any {
	output := map[string]any{
		"url": targetURL,
	}

	if result.Strategy != "" {
		output["strategy"] = result.Strategy
	}
	if result.RequiresStealth {
		output["stealth_required"] = true
	}
	if result.HTMLBody != "" {
		output["has_html"] = true
		output["html_size_bytes"] = len(result.HTMLBody)
	}

	if len(result.Candidates) > 0 {
		candidates := make([]map[string]any, len(result.Candidates))
		for i, c := range result.Candidates {
			entry := map[string]any{
				"strategy": c.Strategy,
				"score":    c.Score,
			}
			if c.Schema != nil {
				entry["schema_id"] = c.Schema.ID
				entry["endpoint_count"] = len(c.Schema.Endpoints)
				if len(c.Schema.Endpoints) > 0 {
					entry["url_template"] = c.Schema.Endpoints[0].URLTemplate
				}
			}
			candidates[i] = entry
		}
		output["candidates"] = candidates
	}

	if result.Schema != nil {
		output["best_schema"] = result.Schema
	}

	return output
}
