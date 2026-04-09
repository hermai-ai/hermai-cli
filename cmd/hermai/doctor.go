package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/config"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check system readiness and diagnose issues",
		Long:  `Verifies that all required components are configured and reachable.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			out := cmd.OutOrStdout()
			allOK := true

			// Check 1: Config file
			configPath := config.ConfigFilePath()
			if _, err := os.Stat(configPath); err == nil {
				fmt.Fprintf(out, "[OK] Config file: %s\n", configPath)
			} else {
				fmt.Fprintf(out, "[WARN] No config file at %s (using env vars/defaults)\n", configPath)
			}

			// Check 2: Config validation
			if err := cfg.Validate(); err != nil {
				fmt.Fprintf(out, "[FAIL] Config validation: %v\n", err)
				allOK = false
			} else {
				fmt.Fprintf(out, "[OK] Config validation passed\n")
			}

			// Check 3: API key
			if cfg.LLM.APIKey != "" {
				maskedKey := "(configured)"
				if len(cfg.LLM.APIKey) > 8 {
					maskedKey = cfg.LLM.APIKey[:4] + "..." + cfg.LLM.APIKey[len(cfg.LLM.APIKey)-4:]
				}
				fmt.Fprintf(out, "[OK] API key: %s\n", maskedKey)
			} else {
				fmt.Fprintf(out, "[FAIL] No API key configured\n")
				allOK = false
			}

			// Check 4: LLM endpoint reachable
			if cfg.LLM.BaseURL != "" {
				if err := checkEndpoint(cfg.LLM.BaseURL + "/models"); err != nil {
					fmt.Fprintf(out, "[WARN] LLM endpoint not reachable: %v\n", err)
				} else {
					fmt.Fprintf(out, "[OK] LLM endpoint: %s\n", cfg.LLM.BaseURL)
				}
			}

			// Check 5: Model
			if cfg.LLM.Model != "" {
				fmt.Fprintf(out, "[OK] Model: %s\n", cfg.LLM.Model)
			} else {
				fmt.Fprintf(out, "[WARN] No model configured\n")
			}

			// Check 6: Cache directory writable
			if err := checkCacheDir(cfg.Cache.Dir); err != nil {
				fmt.Fprintf(out, "[FAIL] Cache directory: %v\n", err)
				allOK = false
			} else {
				fmt.Fprintf(out, "[OK] Cache directory: %s\n", cfg.Cache.Dir)
			}

			// Check 7: Browser (Lightpanda or Chromium)
			checkBrowser(out, cfg.Browser.Path, cfg.Browser.CDPURL)

			fmt.Fprintln(out)
			if allOK {
				fmt.Fprintln(out, "All checks passed. Run 'hermai fetch <url>' to get started.")
			} else {
				fmt.Fprintln(out, "Some checks failed. Fix the issues above and run 'hermai doctor' again.")
			}

			return nil
		},
	}
}

func checkEndpoint(url string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func checkCacheDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create directory %s: %w", dir, err)
	}

	testFile := dir + "/.doctor-test"
	if err := os.WriteFile(testFile, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	os.Remove(testFile)

	return nil
}

func checkBrowser(out io.Writer, browserPath, cdpURL string) {
	// Check configured CDP URL first
	if cdpURL != "" {
		httpURL := strings.Replace(cdpURL, "ws://", "http://", 1)
		httpURL = strings.Replace(httpURL, "wss://", "https://", 1)
		httpURL = strings.TrimSuffix(httpURL, "/") + "/json/version"
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(httpURL)
		if err == nil {
			resp.Body.Close()
			fmt.Fprintf(out, "[OK] CDP endpoint: %s\n", cdpURL)
			return
		}
		fmt.Fprintf(out, "[FAIL] CDP endpoint not reachable: %s\n", cdpURL)
		return
	}

	// Check Lightpanda on default port
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:9222/json/version")
	if err == nil {
		resp.Body.Close()
		fmt.Fprintln(out, "[OK] Lightpanda available on port 9222")
		return
	}

	if browserPath != "" {
		if _, err := os.Stat(browserPath); err == nil {
			fmt.Fprintf(out, "[OK] Browser binary: %s\n", browserPath)
			return
		}
		fmt.Fprintf(out, "[FAIL] Browser binary not found: %s\n", browserPath)
		return
	}

	fmt.Fprintln(out, "[WARN] No browser detected (Lightpanda not running, no browser-path set)")
	fmt.Fprintln(out, "       Use --no-browser flag to skip browser-based discovery")
}
