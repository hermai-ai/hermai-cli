package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/probe"
	"github.com/spf13/cobra"
)

const introspectionQuery = `{"query":"{ __schema { queryType { name } mutationType { name } types { name kind fields { name type { name kind ofType { name kind } } } } } }"}`

func newIntrospectCmd() *cobra.Command {
	var (
		stealth  bool
		proxyURL string
		timeout  string
		insecure bool
		format   string
		full     bool
		headers  []string
	)

	cmd := &cobra.Command{
		Use:   "introspect <url>",
		Short: "Discover GraphQL schema via introspection",
		Long: `Introspect sends a GraphQL introspection query to the given URL and
returns the schema — query types, mutation types, and their fields.

No API key required.

Examples:
  hermai introspect https://api.example.com/graphql
  hermai introspect --full https://countries.trevorblades.com/graphql`,
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

			query := introspectionQuery
			if full {
				query = `{"query":"{ __schema { queryType { name fields { name description args { name type { name kind ofType { name kind ofType { name } } } defaultValue } type { name kind ofType { name kind ofType { name } } } } } mutationType { name fields { name description args { name type { name kind ofType { name kind } } } } } types { name kind description fields { name description args { name type { name kind ofType { name kind } } defaultValue } type { name kind ofType { name kind ofType { name } } } } } } }"}`
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewBufferString(query))
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			probe.SetBrowserHeaders(req)
			// GraphQL endpoints require JSON accept, not the HTML accept from SetBrowserHeaders
			req.Header.Set("Accept", "application/json")
			// Custom headers via --header name=value. Applied last so
			// they override defaults (notably when a GraphQL endpoint
			// wants a site-specific Accept or custom auth shape).
			for _, h := range headers {
				k, v, ok := strings.Cut(h, "=")
				if !ok || strings.TrimSpace(k) == "" {
					return fmt.Errorf("--header must be key=value, got %q", h)
				}
				req.Header.Set(strings.TrimSpace(k), v)
			}

			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("introspection failed: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				return fmt.Errorf("introspection returned HTTP %d", resp.StatusCode)
			}

			body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLInputSize))
			if err != nil {
				return fmt.Errorf("failed to read response: %w", err)
			}

			var raw map[string]any
			if err := json.Unmarshal(body, &raw); err != nil {
				return fmt.Errorf("response is not valid JSON: %w", err)
			}

			// Extract the schema from the GraphQL response envelope
			data, _ := raw["data"].(map[string]any)
			if data == nil {
				// Check for errors
				if errs, ok := raw["errors"]; ok {
					return writeJSON(os.Stdout, map[string]any{
						"url":                 targetURL,
						"introspection":       false,
						"errors":              errs,
					}, format)
				}
				return fmt.Errorf("introspection response missing 'data' field")
			}

			schema, _ := data["__schema"].(map[string]any)
			if schema == nil {
				return fmt.Errorf("introspection response missing '__schema' field")
			}

			output := map[string]any{
				"url":           targetURL,
				"introspection": true,
			}

			if qt, ok := schema["queryType"].(map[string]any); ok {
				output["query_type"] = qt
			}
			if mt, ok := schema["mutationType"].(map[string]any); ok {
				output["mutation_type"] = mt
			}

			// Summarize types (skip built-in __ types unless --full)
			if types, ok := schema["types"].([]any); ok {
				var userTypes []any
				for _, t := range types {
					tm, ok := t.(map[string]any)
					if !ok {
						continue
					}
					name, _ := tm["name"].(string)
					if !full && strings.HasPrefix(name, "__") {
						continue
					}
					userTypes = append(userTypes, tm)
				}
				output["types"] = userTypes
				output["type_count"] = len(userTypes)
			}

			return writeJSON(os.Stdout, output, format)
		},
	}

	cmd.Flags().BoolVar(&stealth, "stealth", false, "Use Chrome TLS fingerprinting")
	cmd.Flags().StringVar(&proxyURL, "proxy", "", "Proxy URL")
	cmd.Flags().StringVar(&timeout, "timeout", "10s", "Request timeout")
	cmd.Flags().BoolVarP(&insecure, "insecure", "k", false, "Skip TLS verification")
	cmd.Flags().BoolVar(&full, "full", false, "Full introspection with args, descriptions, and built-in types")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or compact")
	cmd.Flags().StringArrayVar(&headers, "header", nil,
		"Custom header to send, repeatable: --header name=value. Use for auth-gated GraphQL (Stardust, Shopify Storefront, etc.)")

	return cmd
}
