package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/actions"
	"github.com/hermai-ai/hermai-cli/pkg/browsercookies"
	"github.com/hermai-ai/hermai-cli/pkg/config"
	"github.com/spf13/cobra"
)

// sessionSchemaResponse matches the shape of GET /v1/schemas/{site} .data
type sessionSchemaResponse struct {
	Site       string `json:"site"`
	PublicCard struct {
		Session                    sessionCardBlock `json:"session"`
		RequiresSessionBootstrap   bool             `json:"requires_session_bootstrap"`
	} `json:"public_card"`
}

type sessionCardBlock struct {
	BootstrapURL            string   `json:"bootstrap_url,omitempty"`
	TLSProfile              string   `json:"tls_profile,omitempty"`
	RequiredCookies         []string `json:"required_cookies,omitempty"`
	EndpointsNeedingSession []string `json:"endpoints_needing_session,omitempty"`
	SignFunction            string   `json:"sign_function,omitempty"`
	SignStrategy            string   `json:"sign_strategy,omitempty"`
	Description             string   `json:"description,omitempty"`
}

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage warm browser sessions for anti-bot sites",
		Long: `Some sites gate their APIs behind anti-bot systems. Their registry schemas
carry a session block documenting what a local client must do: navigate a
bootstrap URL in a real browser, wait for specific cookies, and replay
those cookies from a Chrome-TLS HTTP client.

'bootstrap' launches a stealth Chrome, navigates to the bootstrap URL,
waits for the required cookies, and saves them to ~/.hermai/sessions/.
'status' inspects what's saved. 'list' shows all sessions on disk.`,
	}

	cmd.AddCommand(newSessionBootstrapCmd())
	cmd.AddCommand(newSessionImportCmd())
	cmd.AddCommand(newSessionStatusCmd())
	cmd.AddCommand(newSessionListCmd())

	return cmd
}

func newSessionImportCmd() *cobra.Command {
	var (
		browsersFlag []string
		timeout      time.Duration
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "import <site>",
		Short: "Import cookies for a site from your existing browser session",
		Long: `Reads cookies for <site> from the browsers installed on this machine
(Chrome, Firefox, Safari, Edge, Brave, Chromium, Opera, Vivaldi) and writes
them to ~/.hermai/sessions/<site>/cookies.json — the same format as
'hermai session bootstrap'.

Use this when you're already signed in to a site in your everyday browser
and want Hermai to replay that session on your behalf without asking you
to log in again.

Privacy: only cookies scoped to <site> are ever read. The first run may
trigger an OS-level prompt (macOS Keychain / Windows DPAPI / Linux
libsecret) asking you to authorize Hermai to read the browser's cookie
store; Hermai cannot access your cookies without your explicit OS consent.

Examples:
  hermai session import x.com
  hermai session import airbnb.com --browsers firefox,chrome
  hermai session import x.com --dry-run        # check if cookies exist`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			site := strings.ToLower(strings.TrimSpace(args[0]))
			if site == "" {
				return fmt.Errorf("site is required")
			}

			ctx, cancel := signalContext(timeout)
			defer cancel()

			src := &browsercookies.Source{BrowserPreference: browsersFlag}
			start := time.Now()
			cookies, err := src.GetCookies(ctx, site)
			if err != nil {
				return fmt.Errorf("read browser cookies: %w", err)
			}

			if len(cookies) == 0 {
				fmt.Fprintf(os.Stderr, "no cookies found for %s in any installed browser.\n", site)
				fmt.Fprintf(os.Stderr, "make sure you're signed in to %s in Chrome/Firefox/Safari/Edge, then retry.\n", site)
				fmt.Fprintf(os.Stderr, "if you don't have the site open in a browser, run: hermai session bootstrap %s\n", site)
				os.Exit(1)
			}

			fmt.Fprintf(os.Stderr, "read %d cookies for %s from local browser (%v)\n",
				len(cookies), site, time.Since(start).Round(time.Millisecond))

			if dryRun {
				fmt.Fprintf(os.Stderr, "--dry-run: cookie names (not values):\n")
				for _, c := range cookies {
					fmt.Fprintf(os.Stderr, "  %s  (domain=%s path=%s)\n", c.Name, c.Domain, c.Path)
				}
				return nil
			}

			// Normalize into the same CookieFile shape as 'session bootstrap'
			// so downstream commands (replay, execute) treat it the same.
			domain, _ := browsercookies.NormalizeDomain(site)
			cookieMap := make(map[string]string, len(cookies))
			for _, c := range cookies {
				cookieMap[c.Name] = c.Value
			}

			storageDir := sessionsDir(config.Load())
			siteDir := filepath.Join(storageDir, site)
			if err := os.MkdirAll(siteDir, 0700); err != nil {
				return fmt.Errorf("mkdir %s: %w", siteDir, err)
			}
			storagePath := filepath.Join(siteDir, "cookies.json")

			file := actions.CookieFile{
				Site:    site,
				SavedAt: time.Now().UTC(),
				Domain:  domain,
				Cookies: cookieMap,
			}
			body, err := json.MarshalIndent(file, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal cookie file: %w", err)
			}
			if err := os.WriteFile(storagePath, body, 0600); err != nil {
				return fmt.Errorf("write %s: %w", storagePath, err)
			}
			fmt.Fprintf(os.Stderr, "saved %s\n", storagePath)
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&browsersFlag, "browsers", nil,
		"Restrict to specific browsers (comma-separated: chrome,firefox,safari,edge,brave,chromium,opera,vivaldi)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second,
		"Maximum time to spend scanning browser cookie stores")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print the cookie names that would be imported, without saving values to disk")

	return cmd
}

func newSessionBootstrapCmd() *cobra.Command {
	var (
		browserPath string
		headful     bool
		timeout     time.Duration
	)

	cmd := &cobra.Command{
		Use:   "bootstrap <site>",
		Short: "Warm a browser session for a site and save its cookies",
		Long: `Pulls the schema for <site> from the hermai registry, reads the session
block, and launches a stealth Chrome to navigate to the bootstrap URL.
Waits up to --timeout for every required cookie to appear, then writes
the full cookie jar to ~/.hermai/sessions/<site>/cookies.json.

Examples:
  hermai session bootstrap tiktok.com
  hermai session bootstrap airbnb.com --headful
  hermai session bootstrap zillow.com --timeout 60s`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			site := strings.ToLower(strings.TrimSpace(args[0]))
			if site == "" {
				return fmt.Errorf("site is required")
			}

			cfg := config.Load()
			storageDir := sessionsDir(cfg)

			card, err := fetchSessionCard(cfg, site)
			if err != nil {
				return fmt.Errorf("fetching schema for %s: %w", site, err)
			}
			session := card.PublicCard.Session
			if session.BootstrapURL == "" {
				return fmt.Errorf("schema for %s does not declare a bootstrap_url — nothing to warm", site)
			}

			fmt.Fprintf(os.Stderr, "-> bootstrapping %s\n", site)
			fmt.Fprintf(os.Stderr, "  URL: %s\n", session.BootstrapURL)
			if len(session.RequiredCookies) > 0 {
				fmt.Fprintf(os.Stderr, "  waiting for cookies: %s\n",
					strings.Join(session.RequiredCookies, ", "))
			}
			if session.Description != "" {
				fmt.Fprintf(os.Stderr, "  docs: %s\n", truncate(session.Description, 140))
			}

			ctx, cancel := signalContext(timeout)
			defer cancel()

			// Fall back to configured browser path if flag not set
			effectiveBrowserPath := browserPath
			if effectiveBrowserPath == "" {
				effectiveBrowserPath = cfg.Browser.Path
			}

			req := actions.BootstrapRequest{
				Site:            site,
				BootstrapURL:    session.BootstrapURL,
				RequiredCookies: session.RequiredCookies,
				Timeout:         timeout,
				BrowserPath:     effectiveBrowserPath,
				Headless:        !headful,
				StorageDir:      storageDir,
			}
			result, err := actions.BootstrapSession(ctx, req)
			if err != nil {
				if result != nil && result.CookieCount > 0 {
					fmt.Fprintf(os.Stderr, "partial bootstrap: captured %d cookies, missing required %v -> %s\n",
						result.CookieCount, result.RequiredMiss, result.StoragePath)
					return err
				}
				return fmt.Errorf("bootstrap failed: %w", err)
			}

			fmt.Fprintf(os.Stderr, "session ready (%d cookies, %v)\n", result.CookieCount, result.Duration.Round(time.Millisecond))
			fmt.Fprintf(os.Stderr, "  saved: %s\n", result.StoragePath)
			fmt.Fprintf(os.Stderr, "  required cookies found: %v\n", result.RequiredFound)
			if session.SignFunction != "" {
				fmt.Fprintf(os.Stderr, "  note: this site also uses per-request signing via %s\n", session.SignFunction)
				fmt.Fprintf(os.Stderr, "        replay strategy: %s\n", session.SignStrategy)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&browserPath, "browser-path", "", "Path to Chrome/Chromium binary (default: config or rod auto-detect)")
	cmd.Flags().BoolVar(&headful, "headful", false, "Launch Chrome with a visible window")
	cmd.Flags().DurationVar(&timeout, "timeout", 45*time.Second, "Maximum time to wait for required cookies")

	return cmd
}

func newSessionStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <site>",
		Short: "Show the saved session for a site",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			site := strings.ToLower(strings.TrimSpace(args[0]))
			cfg := config.Load()
			file, err := actions.LoadCookieFile(sessionsDir(cfg), site)
			if err != nil {
				return err
			}
			if file == nil {
				fmt.Fprintf(os.Stderr, "no saved session for %s. Run: hermai session bootstrap %s\n", site, site)
				os.Exit(1)
			}
			fmt.Printf("site:    %s\n", file.Site)
			fmt.Printf("domain:  %s\n", file.Domain)
			fmt.Printf("saved:   %s (%s ago)\n", file.SavedAt.Format(time.RFC3339), time.Since(file.SavedAt).Round(time.Second))
			fmt.Printf("cookies: %d\n", len(file.Cookies))
			if len(file.Required) > 0 {
				var missing []string
				for _, name := range file.Required {
					if _, ok := file.Cookies[name]; !ok {
						missing = append(missing, name)
					}
				}
				if len(missing) == 0 {
					fmt.Printf("required: all present (%v)\n", file.Required)
				} else {
					fmt.Printf("required: MISSING %v\n", missing)
				}
			}
			return nil
		},
	}
}

func newSessionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all saved sessions on disk",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			dir := sessionsDir(cfg)
			entries, err := os.ReadDir(dir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(os.Stderr, "no sessions on disk yet. Run: hermai session bootstrap <site>")
					return nil
				}
				return err
			}
			if len(entries) == 0 {
				fmt.Fprintln(os.Stderr, "no sessions on disk yet.")
				return nil
			}
			fmt.Printf("%-32s %-10s %-22s %s\n", "SITE", "COOKIES", "SAVED", "REQUIRED")
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				file, loadErr := actions.LoadCookieFile(dir, e.Name())
				if loadErr != nil || file == nil {
					continue
				}
				missing := 0
				for _, name := range file.Required {
					if _, ok := file.Cookies[name]; !ok {
						missing++
					}
				}
				status := fmt.Sprintf("%d/%d ok", len(file.Required)-missing, len(file.Required))
				if len(file.Required) == 0 {
					status = "n/a"
				}
				fmt.Printf("%-32s %-10d %-22s %s\n",
					file.Site,
					len(file.Cookies),
					file.SavedAt.Format("2006-01-02 15:04:05"),
					status)
			}
			return nil
		},
	}
}

func fetchSessionCard(cfg config.Config, site string) (*sessionSchemaResponse, error) {
	client := newPlatformClient(cfg.Platform)
	path := "/v1/schemas/" + url.PathEscape(site)
	raw, err := client.do("GET", path, nil, false, nil)
	if err != nil {
		return nil, err
	}
	var card sessionSchemaResponse
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, fmt.Errorf("parse session card: %w", err)
	}
	return &card, nil
}

func sessionsDir(cfg config.Config) string {
	home := cfg.Cache.Dir
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".hermai", "cache")
		}
	}
	return filepath.Join(filepath.Dir(home), "sessions")
}
