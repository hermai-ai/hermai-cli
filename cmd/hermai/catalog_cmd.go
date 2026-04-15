package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hermai-ai/hermai-cli/pkg/cache"
	"github.com/hermai-ai/hermai-cli/pkg/config"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
	"github.com/spf13/cobra"
)

func newCatalogCmd() *cobra.Command {
	var (
		format string
	)

	cmd := &cobra.Command{
		Use:   "catalog <url>",
		Short: "Show known endpoints and actions for a URL (no discovery)",
		Long: `Catalog shows what's already known about a URL — from the local schema cache
or (with --registry) from the hermai platform registry. Unlike "hermai discover",
this does NOT run the engine or launch a browser. It's instant.

If nothing is cached locally, try "hermai registry pull <site>" to download from
the registry, or "hermai discover <url>" to run discovery from scratch.

Examples:
  hermai catalog https://allbirds.com
  hermai catalog https://news.ycombinator.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetURL := args[0]
			cfg := config.Load()

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			c := cache.NewFileCache(cfg.Cache.Dir, cfg.Cache.TTL)

			apiSchema, cssSchema, _ := c.LookupAll(ctx, targetURL)
			if apiSchema == nil && cssSchema == nil {
				return fmt.Errorf("no cached schema for %s — try \"hermai discover %s\" or \"hermai registry pull <site>\"", targetURL, targetURL)
			}

			// Build a summary from whatever's cached.
			result := buildCatalogFromCache(targetURL, apiSchema, cssSchema)
			return writeJSON(os.Stdout, result, format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or compact")

	return cmd
}

type catalogResult struct {
	Domain    string            `json:"domain"`
	URL       string            `json:"url"`
	Source    string            `json:"source"`
	Endpoints []catalogEndpoint `json:"endpoints,omitempty"`
	Actions   []schema.Action   `json:"actions,omitempty"`
}

type catalogEndpoint struct {
	Name        string            `json:"name"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers,omitempty"`
	Description string            `json:"description,omitempty"`
}

func buildCatalogFromCache(targetURL string, apiSchema, cssSchema *schema.Schema) catalogResult {
	s := apiSchema
	if s == nil {
		s = cssSchema
	}

	domain := s.Domain
	source := "local_cache"

	result := catalogResult{
		Domain: domain,
		URL:    targetURL,
		Source: source,
	}

	if apiSchema != nil {
		for _, ep := range apiSchema.Endpoints {
			result.Endpoints = append(result.Endpoints, catalogEndpoint{
				Name:        ep.Name,
				Method:      ep.Method,
				URL:         ep.URLTemplate,
				Headers:     ep.Headers,
				Description: ep.Description,
			})
		}
		result.Actions = append(result.Actions, apiSchema.Actions...)
	}

	return result
}
