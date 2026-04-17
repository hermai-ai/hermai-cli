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

	"github.com/hermai-ai/hermai-cli/pkg/browser"
	"github.com/hermai-ai/hermai-cli/pkg/config"
	"github.com/spf13/cobra"
)

func newInterceptCmd() *cobra.Command {
	var (
		browserPath string
		timeout     string
		wait        string
		cookies     []string
		format      string
		raw         bool
		headful     bool
		sessionSite string
	)

	cmd := &cobra.Command{
		Use:   "intercept <url>",
		Short: "Launch browser, navigate to URL, capture XHR/API calls",
		Long: `Intercept launches a headless browser, navigates to the target URL,
and captures all XHR/API network requests made by the page. Returns
filtered JSON API calls — analytics and tracking noise is removed.

Use this to discover what API endpoints a page calls behind the scenes.

Examples:
  hermai intercept https://www.booking.com/hotel/us/example.html
  hermai intercept --wait 10s https://www.tiktok.com/@user
  hermai intercept --cookie "session=abc123" https://example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetURL := args[0]
			cfg := config.Load()

			if browserPath == "" {
				browserPath = cfg.Browser.Path
			}

			captureDuration := 15 * time.Second
			if timeout != "" {
				d, err := time.ParseDuration(timeout)
				if err != nil {
					return fmt.Errorf("invalid --timeout: %w", err)
				}
				captureDuration = d
			}

			waitAfterLoad := cfg.Browser.WaitAfterLoad
			if wait != "" {
				d, err := time.ParseDuration(wait)
				if err != nil {
					return fmt.Errorf("invalid --wait: %w", err)
				}
				waitAfterLoad = d
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			var b *browser.RodBrowser
			var err error
			if cfg.Browser.CDPURL != "" {
				b, err = browser.NewRodBrowserWithCDP(cfg.Browser.CDPURL, browserPath)
			} else {
				b, err = browser.NewRodBrowser(browserPath)
			}
			if err != nil {
				return fmt.Errorf("failed to start browser: %w", err)
			}
			defer b.Close()

			spaDomainsFile := filepath.Join(filepath.Dir(cfg.Cache.Dir), "spa_domains.txt")
			b.SetSPADomainsFile(spaDomainsFile)

			// If --session <site> was passed, fold the cached cookies for
			// that site into the injection list so the visible browser
			// opens already-logged-in. Skips the relogin step when
			// discovering authenticated write flows.
			if sessionSite != "" {
				sess, err := loadSessionCookies(cfg, sessionSite)
				if err != nil {
					return fmt.Errorf("load session cookies for %s: %w", sessionSite, err)
				}
				cookies = append(cookies, sess...)
				fmt.Fprintf(os.Stderr, "injected %d cookies from ~/.hermai/sessions/%s\n", len(sess), sessionSite)
			}

			capture, err := b.Capture(ctx, targetURL, browser.CaptureOpts{
				BrowserPath:   browserPath,
				Timeout:       captureDuration,
				WaitAfterLoad: waitAfterLoad,
				Cookies:       cookies,
				Headful:       headful,
			})
			if err != nil {
				return fmt.Errorf("browser capture failed: %w", err)
			}

			if capture == nil || capture.HAR == nil {
				return fmt.Errorf("no network traffic captured")
			}

			if raw {
				out, err := json.Marshal(capture.HAR.Entries)
				if err != nil {
					return err
				}
				out = append(out, '\n')
				_, err = os.Stdout.Write(out)
				return err
			}

			filtered := browser.FilterHAR(capture.HAR)
			entries := filtered.Entries

			type compactEntry struct {
				Method      string `json:"method"`
				URL         string `json:"url"`
				Status      int    `json:"status"`
				ContentType string `json:"content_type"`
				BodySize    int    `json:"body_size_bytes,omitempty"`
			}

			output := map[string]any{
				"url":            targetURL,
				"total_requests": len(capture.HAR.Entries),
				"api_requests":   len(entries),
			}

			if len(entries) > 0 {
				compact := make([]compactEntry, len(entries))
				replaySpecs := make([]map[string]any, len(entries))
				for i, e := range entries {
					compact[i] = compactEntry{
						Method:      e.Request.Method,
						URL:         e.Request.URL,
						Status:      e.Response.Status,
						ContentType: e.Response.ContentType,
						BodySize:    len(e.Response.Body),
					}
					spec := map[string]any{
						"method": e.Request.Method,
						"url":    e.Request.URL,
					}
					if len(e.Request.Headers) > 0 {
						spec["headers"] = e.Request.Headers
					}
					if e.Request.Body != "" {
						spec["body"] = e.Request.Body
					}
					replaySpecs[i] = spec
				}
				output["entries"] = compact
				output["replay_specs"] = replaySpecs
			}

			return writeJSON(os.Stdout, output, format)
		},
	}

	cmd.Flags().StringVar(&browserPath, "browser-path", "", "Path to Chrome/Chromium binary")
	cmd.Flags().StringVar(&timeout, "timeout", "15s", "Browser capture timeout")
	cmd.Flags().StringVar(&wait, "wait", "", "Extra wait time after page load (e.g. 5s)")
	cmd.Flags().StringArrayVar(&cookies, "cookie", nil, "Cookies to inject (name=value)")
	cmd.Flags().BoolVar(&raw, "raw", false, "Include full HAR entries without filtering")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or compact")
	cmd.Flags().BoolVar(&headful, "headful", false,
		"Launch a visible browser window so you can perform interactions (save draft, add to cart) while capture runs")
	cmd.Flags().StringVar(&sessionSite, "session", "",
		"Inject cookies from ~/.hermai/sessions/<site>/cookies.json so the browser opens already-logged-in")

	return cmd
}

// loadSessionCookies reads the saved cookies for a site and returns
// them as name=value pairs ready to hand to CaptureOpts.Cookies.
func loadSessionCookies(cfg config.Config, site string) ([]string, error) {
	path := filepath.Join(sessionsDir(cfg), site, "cookies.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cf struct {
		Cookies map[string]string `json:"cookies"`
	}
	if err := json.Unmarshal(b, &cf); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cf.Cookies))
	for k, v := range cf.Cookies {
		out = append(out, k+"="+v)
	}
	return out, nil
}
