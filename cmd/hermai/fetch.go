package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/actions"
	"github.com/hermai-ai/hermai-cli/pkg/config"
	"github.com/hermai-ai/hermai-cli/pkg/analyzer"
	"github.com/hermai-ai/hermai-cli/pkg/browser"
	"github.com/hermai-ai/hermai-cli/pkg/cache"
	"github.com/hermai-ai/hermai-cli/pkg/engine"
	"github.com/hermai-ai/hermai-cli/pkg/fetcher"
	"github.com/hermai-ai/hermai-cli/pkg/log"
	"github.com/itchyny/gojq"
	"github.com/spf13/cobra"
)

func newFetchCmd() *cobra.Command {
	var (
		raw           bool
		pipe          bool
		query         string
		noCache       bool
		proxy         string
		browserPath   string
		cacheTTL      string
		model         string
		classifyModel string
		timeout       string
		noBrowser     bool
		insecure      bool
		verbose       bool
		format        string
		cookies       []string
	)

	cmd := &cobra.Command{
		Use:   "fetch <url> [question]",
		Short: "Fetch structured data from a website URL",
		Long: `Fetch discovers API endpoints behind a website URL, analyzes them with
an LLM, and returns structured JSON data. Results are cached for subsequent requests.

If a question is provided after the URL, hermai fetches the data (using cache
if available) and answers the question using a fast LLM. This is the "fetch once,
query many" mode — subsequent questions about the same URL reuse cached data.

Examples:
  hermai fetch https://github.com/golang/go
  hermai fetch https://github.com/golang/go "how many stars?"
  hermai fetch https://www.firecrawl.dev/pricing "what are the pricing tiers?"`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetURL := args[0]
			var askPrompt string
			if len(args) > 1 {
				askPrompt = args[1]
			}
			cfg := config.Load()

			// CLI flag overrides
			if proxy != "" {
				cfg.Proxy = proxy
			}
			if browserPath != "" {
				cfg.Browser.Path = browserPath
			}
			if model != "" {
				cfg.LLM.Model = model
			}
			if classifyModel != "" {
				cfg.LLM.ClassifyModel = classifyModel
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

			// Logger: CLI flag overrides config/env
			// --pipe implies quiet mode (no info logs cluttering stdout)
			level := log.LevelInfo
			if verbose || cfg.Verbose {
				level = log.LevelDebug
			}
			if (pipe || query != "" || askPrompt != "") && !verbose {
				level = log.LevelError
			}
			logger := log.New(level)

			// Context with timeout and signal handling
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			// Timeout: CLI flag > config/env > no limit
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

			// Wire services
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

				// Enable auto-learning: domains where Lightpanda fails get
				// recorded so subsequent visits skip straight to Chromium.
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

			// Auto-resolve a saved session jar for the target URL's hostname.
			// Hermai's warm-once model is: `hermai session import|bootstrap <site>`
			// saves cookies to ~/.hermai/sessions/<site>/cookies.json; subsequent
			// reads should replay those cookies automatically. Before this wiring
			// fetch only honored --cookie overrides, so session-gated reads failed
			// even when a full jar was on disk. The action runner already did this
			// for writes — now reads get the same treatment.
			//
			// Precedence: saved jar first, then --cookie overrides append/override
			// by name. Missing jar is not an error; fetch falls back to whatever
			// --cookie values the caller passed.
			mergedCookies := mergeCookiesForURL(sessionsDir(cfg), targetURL, cookies, logger)

			opts := engine.FetchOpts{
				ProxyURL:            cfg.Proxy,
				Raw:                 raw,
				RetryOnBrokenSchema: !noCache,
				BrowserPath:         cfg.Browser.Path,
				BrowserTimeout:      cfg.Browser.Timeout,
				WaitAfterLoad:       cfg.Browser.WaitAfterLoad,
				NoBrowser:           noBrowser,
				NoCache:             noCache,
				Insecure:            insecure,
				Cookies:             mergedCookies,
			}

			result, err := eng.Fetch(ctx, targetURL, opts)
			if err != nil {
				return humanizeError(err)
			}

			// Ask mode: fetch data, then answer question via LLM
			if askPrompt != "" {
				dataJSON, jsonErr := json.Marshal(result.Payload())
				if jsonErr != nil {
					return fmt.Errorf("failed to serialize data: %w", jsonErr)
				}
				answer, askErr := a.Ask(ctx, string(dataJSON), askPrompt)
				if askErr != nil {
					return fmt.Errorf("ask failed: %w", askErr)
				}
				fmt.Fprintln(os.Stdout, answer)
				eng.WaitBackground()
				return nil
			}

			// Determine output payload
			// -q implies raw data (no wrapper) — query runs against content/data
			// -p also strips the wrapper
			var payload any
			if pipe || query != "" {
				payload = result.Payload()
			} else {
				payload = result
			}

			// -q: built-in jq query — extract fields without external tools
			if query != "" {
				queryErr := runQuery(query, payload)
				eng.WaitBackground()
				return queryErr
			}

			var output []byte
			switch format {
			case "compact":
				output, err = json.Marshal(payload)
			default:
				output, err = json.MarshalIndent(payload, "", "  ")
			}
			if err != nil {
				return fmt.Errorf("failed to marshal result: %w", err)
			}

			fmt.Fprintln(os.Stdout, string(output))

			// Hint about background discovery (suppress in pipe/query modes)
			if result.Metadata.APISchemaStatus == fetcher.SchemaStatusDiscovering && !pipe && !verbose {
				fmt.Fprintln(os.Stderr, "hint: API discovery running in background. Next fetch will use cached API data.")
			}

			// Wait for background API discovery to complete before exit
			eng.WaitBackground()
			return nil
		},
	}

	cmd.Flags().BoolVar(&raw, "raw", false, "Include raw API responses in output")
	cmd.Flags().BoolVarP(&pipe, "pipe", "p", false, "Output raw data only, no metadata wrapper (ideal for piping to jq)")
	cmd.Flags().StringVarP(&query, "query", "q", "", "jq query on the raw data (e.g. '.title', '.items[].name')")
	cmd.Flags().StringVar(&proxy, "proxy", "", "Proxy URL (http:// or socks5://)")
	cmd.Flags().StringVar(&browserPath, "browser-path", "", "Path to Chrome/Chromium binary")
	cmd.Flags().StringVar(&cacheTTL, "cache-ttl", "", "Schema cache TTL (e.g. 7d, 30d)")
	cmd.Flags().StringVar(&model, "model", "", "LLM model name")
	cmd.Flags().StringVar(&classifyModel, "classify-model", "", "Fast model for HAR classification (defaults to --model)")
	cmd.Flags().StringVar(&timeout, "timeout", "", "Overall operation timeout (e.g. 15s, 30s, 1m)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Skip browser, use probe + LLM only (faster)")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "Skip cache, always fresh discovery")
	cmd.Flags().BoolVarP(&insecure, "insecure", "k", false, "Skip TLS certificate verification")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable debug logging")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json (indented) or compact")
	cmd.Flags().StringArrayVar(&cookies, "cookie", nil, "Cookies to inject into browser (name=value), can be repeated")

	return cmd
}

// noopBrowser is used when --no-browser is set. The engine skips browser
// capture entirely, but we still need a Service to satisfy the interface.
type noopBrowser struct{}

func (n *noopBrowser) Capture(_ context.Context, _ string, _ browser.CaptureOpts) (*browser.CaptureResult, error) {
	return nil, fmt.Errorf("browser disabled via --no-browser")
}

func (n *noopBrowser) Close() error { return nil }

// runQuery applies a jq expression to the data and prints each result.
// Scalars are printed as plain text, objects/arrays as JSON.
func runQuery(expr string, data any) error {
	q, err := gojq.Parse(expr)
	if err != nil {
		return fmt.Errorf("invalid query %q: %w", expr, err)
	}

	// gojq needs the data as a Go native type (map/slice/scalar).
	// Re-marshal and unmarshal to normalize (fetcher.Result fields, etc.)
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to serialize data: %w", err)
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return fmt.Errorf("failed to normalize data: %w", err)
	}

	iter := q.Run(normalized)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return fmt.Errorf("query error: %w", err)
		}

		switch val := v.(type) {
		case string:
			fmt.Fprintln(os.Stdout, val)
		case nil:
			fmt.Fprintln(os.Stdout, "null")
		default:
			out, _ := json.MarshalIndent(val, "", "  ")
			fmt.Fprintln(os.Stdout, string(out))
		}
	}

	return nil
}

// humanizeError converts internal errors to user-friendly messages.
func humanizeError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("operation timed out — try increasing --timeout or using --no-browser for faster results")
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("operation cancelled")
	case errors.Is(err, browser.ErrAuthWall):
		return fmt.Errorf("this page requires authentication — use --cookie name=value to provide session cookies, " +
			"or if the site has a registered schema run: hermai session bootstrap <site>")
	case errors.Is(err, engine.ErrAnalysisFailed):
		return fmt.Errorf("could not identify API endpoints — the site may not have a public JSON API")
	case errors.Is(err, engine.ErrNoEndpoints):
		return fmt.Errorf("no API endpoints found and HTML extraction failed")
	case errors.Is(err, fetcher.ErrSchemaBroken):
		return fmt.Errorf("cached API schema no longer works — try 'hermai cache clear' and retry")
	default:
		// Anti-bot gates often surface as opaque 403s or timeouts without a
		// typed error. When the message looks like one of the common gates,
		// point the user at the session bootstrap flow — many of those sites
		// have a registered schema with bootstrap instructions.
		msg := err.Error()
		if looksLikeAntiBot(msg) {
			return fmt.Errorf("%w\n\n"+
				"this looks like an anti-bot gate (Cloudflare / PerimeterX / DataDome / TikTok signing).\n"+
				"if the site has a registered schema, run: hermai session bootstrap <site>\n"+
				"then retry the fetch. See: hermai session --help", err)
		}
		return fmt.Errorf("fetch failed: %w", err)
	}
}

// mergeCookiesForURL resolves the saved session jar that matches the target
// URL's hostname and merges its cookies with the caller's explicit --cookie
// overrides. Hostname resolution walks the label stack so a jar saved under
// "x.com" matches requests to "x.com", "www.x.com", or "api.x.com".
//
// Precedence: saved jar first; --cookie values override same-named entries
// and append new ones. Missing jar is not an error — the returned slice is
// exactly the caller's input. A debug log records which jar was loaded so
// users can diagnose auth failures without re-reading the source.
func mergeCookiesForURL(storageDir, targetURL string, explicit []string, logger *log.Logger) []string {
	u, err := url.Parse(targetURL)
	if err != nil || u.Host == "" {
		return explicit
	}
	host := strings.ToLower(u.Hostname())

	// Candidate lookup order: exact host, then progressively strip leading
	// labels. "api.www.x.com" checks api.www.x.com → www.x.com → x.com.
	// Stops at the last two labels — "com" alone never matches.
	var jar *actions.CookieFile
	var matchedSite string
	candidates := []string{host}
	labels := strings.Split(host, ".")
	for i := 1; i < len(labels)-1; i++ {
		candidates = append(candidates, strings.Join(labels[i:], "."))
	}
	for _, candidate := range candidates {
		f, err := actions.LoadCookieFile(storageDir, candidate)
		if err != nil {
			if logger != nil {
				logger.Debug("session jar at %s could not be read: %v", candidate, err)
			}
			continue
		}
		if f != nil && len(f.Cookies) > 0 {
			jar = f
			matchedSite = candidate
			break
		}
	}
	if jar == nil {
		return explicit
	}
	if logger != nil {
		logger.Debug("attached %d cookies from ~/.hermai/sessions/%s/cookies.json to %s",
			len(jar.Cookies), matchedSite, host)
	}

	// Build name → value map with jar first, then explicit overrides.
	merged := make(map[string]string, len(jar.Cookies)+len(explicit))
	for name, value := range jar.Cookies {
		merged[name] = value
	}
	for _, pair := range explicit {
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		merged[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	out := make([]string, 0, len(merged))
	for name, value := range merged {
		out = append(out, name+"="+value)
	}
	return out
}

// looksLikeAntiBot is a shallow heuristic on error text that matches the
// common symptoms of an anti-bot gate. Used only for the CLI's user-facing
// hint — it never drives logic that changes what hermai fetches.
func looksLikeAntiBot(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{
		"http 403",
		"403 forbidden",
		"status 403",
		"perimeterx",
		"datadome",
		"cloudflare",
		"captcha",
		"access denied",
		"x-bogus",
		"bot detect",
	} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
