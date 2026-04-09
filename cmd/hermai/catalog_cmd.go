package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/actions"
	"github.com/hermai-ai/hermai-cli/internal/config"
	"github.com/hermai-ai/hermai-cli/pkg/analyzer"
	"github.com/hermai-ai/hermai-cli/pkg/browser"
	"github.com/hermai-ai/hermai-cli/pkg/cache"
	"github.com/hermai-ai/hermai-cli/pkg/engine"
	"github.com/hermai-ai/hermai-cli/pkg/fetcher"
	"github.com/hermai-ai/hermai-cli/pkg/log"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
	"github.com/spf13/cobra"
)

// CatalogOutput is the JSON structure returned by hermai catalog.
type CatalogOutput struct {
	Domain   string          `json:"domain"`
	URL      string          `json:"url"`
	Source   string          `json:"source"`
	Coverage string          `json:"coverage,omitempty"`
	Actions  []schema.Action `json:"actions"`
}

func newCatalogCmd() *cobra.Command {
	var (
		noCache     bool
		proxy       string
		browserPath string
		cacheTTL    string
		model       string
		noBrowser   bool
		insecure    bool
		verbose     bool
		timeout     string
		format      string
	)

	cmd := &cobra.Command{
		Use:   "catalog <url>",
		Short: "Discover and list all API endpoints for a URL",
		Long: `Catalog discovers the API endpoints behind a website and returns a structured
list of available actions. Each action includes its name, HTTP method, parameters,
and response schema.

This is the "API map" for any website — discover what actions are available
before deciding which endpoint to call.

Examples:
  hermai catalog https://amazon.com/dp/B09V3KXJPB
  hermai catalog https://news.ycombinator.com
  hermai catalog https://github.com/golang/go`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetURL := args[0]
			cfg := config.Load()

			if proxy != "" {
				cfg.Proxy = proxy
			}
			if browserPath != "" {
				cfg.Browser.Path = browserPath
			}
			if model != "" {
				cfg.LLM.Model = model
			}
			if cacheTTL != "" {
				ttl, err := config.ParseTTL(cacheTTL)
				if err != nil {
					return fmt.Errorf("invalid --cache-ttl: %w", err)
				}
				cfg.Cache.TTL = ttl
			}

			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid configuration: %w", err)
			}
			if cfg.LLM.APIKey == "" {
				return fmt.Errorf("no API key configured. Run 'hermai init' to set up, or set HERMAI_API_KEY")
			}

			level := log.LevelInfo
			if verbose || cfg.Verbose {
				level = log.LevelDebug
			}
			logger := log.New(level)

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			effectiveTimeout := cfg.Timeout
			if timeout != "" {
				d, err := time.ParseDuration(timeout)
				if err != nil {
					return fmt.Errorf("invalid --timeout: %w", err)
				}
				effectiveTimeout = d
			}
			if effectiveTimeout > 0 {
				var timeoutCancel context.CancelFunc
				ctx, timeoutCancel = context.WithTimeout(ctx, effectiveTimeout)
				defer timeoutCancel()
			}

			var b browser.Service
			if noBrowser {
				b = &noopBrowser{}
			} else {
				var rodBrowser *browser.RodBrowser
				var err error
				if cfg.Browser.CDPURL != "" {
					rodBrowser, err = browser.NewRodBrowserWithCDP(cfg.Browser.CDPURL, cfg.Browser.Path)
				} else {
					rodBrowser, err = browser.NewRodBrowser(cfg.Browser.Path)
				}
				if err != nil {
					return fmt.Errorf("failed to start browser: %w", err)
				}
				defer rodBrowser.Close()

				spaDomainsFile := filepath.Join(filepath.Dir(cfg.Cache.Dir), "spa_domains.txt")
				rodBrowser.SetSPADomainsFile(spaDomainsFile)
				b = rodBrowser
			}

			a := analyzer.NewOpenAIAnalyzer(analyzer.OpenAIConfig{
				BaseURL:       cfg.LLM.BaseURL,
				APIKey:        cfg.LLM.APIKey,
				Model:         cfg.LLM.Model,
				ClassifyModel: cfg.LLM.ClassifyModel,
			})
			f := fetcher.NewHTTPFetcherWithProxy(cfg.Proxy, insecure)
			c := cache.NewFileCache(cfg.Cache.Dir, cfg.Cache.TTL)

			eng := engine.New(b, a, f, c).WithLogger(logger)

			_, fetchErr := eng.Fetch(ctx, targetURL, engine.FetchOpts{
				ProxyURL:            cfg.Proxy,
				RetryOnBrokenSchema: !noCache,
				BrowserPath:         cfg.Browser.Path,
				BrowserTimeout:      cfg.Browser.Timeout,
				WaitAfterLoad:       cfg.Browser.WaitAfterLoad,
				NoBrowser:           noBrowser,
				NoCache:             noCache,
				Insecure:            insecure,
				CatalogMode:         true,
			})

			eng.WaitBackground()

			catalogData, lookupErr := actions.BuildCatalog(ctx, c, targetURL, actions.DiscoverOptions{
				ProxyURL: cfg.Proxy,
				Insecure: insecure,
			})
			if lookupErr != nil && fetchErr != nil {
				return humanizeError(fetchErr)
			}
			if catalogData == nil || len(catalogData.Actions) == 0 {
				if fetchErr != nil {
					return humanizeError(fetchErr)
				}
				return fmt.Errorf("no actions discovered for %s", targetURL)
			}

			catalog := CatalogOutput{
				Domain:   catalogData.Domain,
				URL:      catalogData.URL,
				Source:   catalogData.Source,
				Coverage: catalogData.Coverage,
				Actions:  catalogData.Actions,
			}

			var output []byte
			var marshalErr error
			switch format {
			case "compact":
				output, marshalErr = json.Marshal(catalog)
			default:
				output, marshalErr = json.MarshalIndent(catalog, "", "  ")
			}
			if marshalErr != nil {
				return fmt.Errorf("failed to marshal catalog: %w", marshalErr)
			}
			fmt.Fprintln(os.Stdout, string(output))

			return nil
		},
	}

	cmd.Flags().BoolVar(&noCache, "no-cache", false, "Skip cache, always fresh discovery")
	cmd.Flags().StringVar(&proxy, "proxy", "", "Proxy URL")
	cmd.Flags().StringVar(&browserPath, "browser-path", "", "Path to Chrome/Chromium binary")
	cmd.Flags().StringVar(&cacheTTL, "cache-ttl", "", "Schema cache TTL (e.g. 7d, 30d)")
	cmd.Flags().StringVar(&model, "model", "", "LLM model name")
	cmd.Flags().StringVar(&timeout, "timeout", "", "Overall operation timeout")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Skip browser, use probe + LLM only")
	cmd.Flags().BoolVarP(&insecure, "insecure", "k", false, "Skip TLS certificate verification")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable debug logging")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or compact")

	return cmd
}
