package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/config"
	"github.com/hermai-ai/hermai-cli/pkg/actions"
	"github.com/hermai-ai/hermai-cli/pkg/analyzer"
	"github.com/hermai-ai/hermai-cli/pkg/browser"
	"github.com/hermai-ai/hermai-cli/pkg/cache"
	"github.com/hermai-ai/hermai-cli/pkg/engine"
	"github.com/hermai-ai/hermai-cli/pkg/fetcher"
	"github.com/hermai-ai/hermai-cli/pkg/log"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
	"github.com/spf13/cobra"
)

func newExecuteCmd() *cobra.Command {
	var (
		proxy       string
		browserPath string
		model       string
		timeout     string
		noBrowser   bool
		insecure    bool
		stealth     bool
		verbose     bool
		format      string
		params      []string
	)

	cmd := &cobra.Command{
		Use:   "execute <url> <action>",
		Short: "Execute a public website action without a browser",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetURL := args[0]
			actionName := args[1]
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
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid configuration: %w", err)
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

			paramMap, err := parseParams(params)
			if err != nil {
				return err
			}

			c := cache.NewFileCache(cfg.Cache.Dir, cfg.Cache.TTL)

			// Auto-stealth: check if cached schema requires stealth
			if !stealth {
				apiSchema, cssSchema, _ := c.LookupAll(ctx, targetURL)
				if (apiSchema != nil && apiSchema.RequiresStealth) || (cssSchema != nil && cssSchema.RequiresStealth) {
					stealth = true
				}
			}

			catalog, _ := actions.BuildCatalog(ctx, c, targetURL, actions.DiscoverOptions{
				ProxyURL: cfg.Proxy,
				Insecure: insecure,
			})

			selected := findAction(catalog, actionName)
			if selected == nil && !noBrowser && cfg.LLM.APIKey != "" {
				if err := runDiscovery(ctx, cfg, logger, targetURL, insecure, false); err == nil {
					catalog, _ = actions.BuildCatalog(ctx, c, targetURL, actions.DiscoverOptions{
						ProxyURL: cfg.Proxy,
						Insecure: insecure,
					})
					selected = findAction(catalog, actionName)
				}
			}
			if selected == nil {
				return fmt.Errorf("action %q not found for %s", actionName, targetURL)
			}

			result, err := actions.ExecuteAction(ctx, targetURL, *selected, paramMap, actions.HTTPOptions{
				ProxyURL:    cfg.Proxy,
				Insecure:    insecure,
				Stealth:     stealth,
				BrowserPath: cfg.Browser.Path,
				NoBrowser:   noBrowser,
				Cache:       c,
			})
			if err != nil {
				return err
			}

			return writeJSON(os.Stdout, result, format)
		},
	}

	cmd.Flags().StringVar(&proxy, "proxy", "", "Proxy URL")
	cmd.Flags().StringVar(&browserPath, "browser-path", "", "Path to Chrome/Chromium binary for fallback discovery")
	cmd.Flags().StringVar(&model, "model", "", "LLM model name for fallback discovery")
	cmd.Flags().StringVar(&timeout, "timeout", "", "Overall operation timeout")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Disable browser fallback discovery; execute only cached/API-free actions")
	cmd.Flags().BoolVarP(&insecure, "insecure", "k", false, "Skip TLS certificate verification")
	cmd.Flags().BoolVar(&stealth, "stealth", false, "Use Chrome TLS+HTTP/2 fingerprinting to bypass anti-bot detection")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable debug logging")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or compact")
	cmd.Flags().StringArrayVar(&params, "param", nil, "Action parameter in key=value form; repeat as needed")

	return cmd
}

func parseParams(items []string) (map[string]string, error) {
	out := make(map[string]string, len(items))
	for _, item := range items {
		key, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid --param %q, expected key=value", item)
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out, nil
}

func findAction(catalog *actions.Catalog, name string) *schema.Action {
	if catalog == nil {
		return nil
	}
	for i := range catalog.Actions {
		if catalog.Actions[i].Name == name {
			return &catalog.Actions[i]
		}
	}
	return nil
}

func runDiscovery(ctx context.Context, cfg config.Config, logger *log.Logger, targetURL string, insecure, noBrowser bool) error {
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
			return err
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

	_, err := eng.Fetch(ctx, targetURL, engine.FetchOpts{
		ProxyURL:            cfg.Proxy,
		RetryOnBrokenSchema: true,
		BrowserPath:         cfg.Browser.Path,
		BrowserTimeout:      cfg.Browser.Timeout,
		WaitAfterLoad:       cfg.Browser.WaitAfterLoad,
		NoBrowser:           noBrowser,
		Insecure:            insecure,
		CatalogMode:         true,
	})
	eng.WaitBackground()
	return err
}
