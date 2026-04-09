package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/config"
	"github.com/spf13/cobra"
)

// platformClient is a thin wrapper around net/http for talking to the hosted
// platform. Lives in the CLI binary only — the platform itself uses its own
// repos directly.
type platformClient struct {
	baseURL string
	apiKey  string
	hc      *http.Client
}

func newPlatformClient(cfg config.PlatformConfig) *platformClient {
	return &platformClient{
		baseURL: strings.TrimRight(cfg.URL, "/"),
		apiKey:  cfg.Key,
		hc:      &http.Client{Timeout: 30 * time.Second},
	}
}

// platformResponse is the standard envelope returned by the API.
type platformResponse struct {
	Success bool             `json:"success"`
	Data    json.RawMessage  `json:"data"`
	Error   *platformErrInfo `json:"error,omitempty"`
}

type platformErrInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *platformClient) do(method, path string, body io.Reader, auth bool, extraHeaders map[string]string) (json.RawMessage, error) {
	if c.baseURL == "" {
		return nil, errors.New("platform URL not configured (set platform_url in config or HERMAI_PLATFORM_URL)")
	}
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		if c.apiKey == "" {
			return nil, errors.New("no platform API key configured — run 'hermai registry login' first")
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	var env platformResponse
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decoding response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 || !env.Success {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}
	return env.Data, nil
}

func newRegistryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Interact with the hermai schema registry",
		Long: `The registry command pulls schemas from the hosted hermai platform,
pushes new ones, and manages the API key used to authenticate.`,
	}
	cmd.AddCommand(newRegistryLoginCmd())
	cmd.AddCommand(newRegistryPullCmd())
	cmd.AddCommand(newRegistryPushCmd())
	cmd.AddCommand(newRegistryListCmd())
	return cmd
}

func newRegistryLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Save a platform API key to the hermai config",
		Long: `login walks you through pasting an API key from the dashboard.

Sign in at https://hermai.dev with your GitHub account, copy the API key from
the dashboard, and paste it here. The key is stored in ~/.hermai/config.yaml
with 0600 permissions.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			fmt.Printf("1. Open the dashboard:\n")
			fmt.Printf("   %s\n\n", dashboardURL(cfg.Platform.URL))
			fmt.Printf("2. Sign in with GitHub and copy your API key.\n\n")
			fmt.Printf("Paste your API key (starts with hm_sk_): ")

			reader := bufio.NewReader(os.Stdin)
			key, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("reading input: %w", err)
			}
			key = strings.TrimSpace(key)
			if key == "" {
				return errors.New("no key provided")
			}
			if !strings.HasPrefix(key, "hm_sk_") {
				return fmt.Errorf("API key must start with 'hm_sk_' (got %q...)", truncate(key, 12))
			}

			if err := config.SavePlatformKey(key); err != nil {
				return fmt.Errorf("saving key: %w", err)
			}
			fmt.Printf("\nSaved to %s\n", config.ConfigFilePath())
			fmt.Println("Run 'hermai registry pull <site>' to fetch a schema.")
			return nil
		},
	}
}

func newRegistryPullCmd() *cobra.Command {
	var (
		version string
		out     string
		intent  string
	)
	cmd := &cobra.Command{
		Use:   "pull <site>",
		Short: "Download a schema package from the registry",
		Long: `pull fetches a schema package from the Hermai registry for the given site.

--intent is required: a natural-language explanation of why the agent is
requesting this schema. Include the target site, the goal, key parameters,
and any context. The intent is used by Hermai to match the most relevant
schema, improve quality through analytics, and provide cross-site
recommendations.

Example:
  hermai registry pull airbnb.com --intent "Planning a family Tokyo trip \
    May 1-5, 3 adults 2 kids, looking for homes near $200/night"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			intent = strings.TrimSpace(intent)
			if intent == "" {
				return errors.New("--intent is required: describe the target site, your goal, key parameters, and any context " +
					"(e.g. --intent \"planning a Tokyo family trip May 1-5, 3 adults 2 kids, ~$200/night\")")
			}

			cfg := config.Load()
			client := newPlatformClient(cfg.Platform)
			site := strings.ToLower(strings.TrimSpace(args[0]))

			path := fmt.Sprintf("/v1/schemas/%s/package", url.PathEscape(site))
			if version != "" {
				path += "?version=" + url.QueryEscape(version)
			}

			// Intent goes via header — avoids URL length limits on long intents
			// and keeps it out of access logs that record query strings.
			data, err := client.do("GET", path, nil, true, map[string]string{
				"X-Hermai-Intent": intent,
			})
			if err != nil {
				return err
			}

			outPath := out
			if outPath == "" {
				outPath = filepath.Join(".", site+".schema.json")
			}
			if err := os.WriteFile(outPath, append(data, '\n'), 0o644); err != nil {
				return fmt.Errorf("writing %s: %w", outPath, err)
			}
			fmt.Printf("Wrote %s\n", outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "Pin a specific version hash")
	cmd.Flags().StringVarP(&out, "out", "o", "", "Output file path (default: ./<site>.schema.json)")
	cmd.Flags().StringVar(&intent, "intent", "", "Natural-language explanation of why you need this schema (required)")
	_ = cmd.MarkFlagRequired("intent")
	return cmd
}

func newRegistryPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push <schema-file>",
		Short: "Upload a schema to the registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			client := newPlatformClient(cfg.Platform)

			body, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("reading schema file: %w", err)
			}

			data, err := client.do("POST", "/v1/schemas", bytes.NewReader(body), true, nil)
			if err != nil {
				return err
			}

			var resp struct {
				VersionHash string `json:"version_hash"`
				Site        string `json:"site"`
				Created     bool   `json:"created"`
			}
			if err := json.Unmarshal(data, &resp); err != nil {
				return fmt.Errorf("decoding push response: %w", err)
			}
			if resp.Created {
				fmt.Printf("Pushed new version for %s\n  hash: %s\n", resp.Site, resp.VersionHash)
			} else {
				fmt.Printf("Already published for %s (no changes)\n  hash: %s\n", resp.Site, resp.VersionHash)
			}
			return nil
		},
	}
}

func newRegistryListCmd() *cobra.Command {
	var (
		category string
		query    string
		sort     string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List schemas in the public registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			client := newPlatformClient(cfg.Platform)

			q := url.Values{}
			if category != "" {
				q.Set("category", category)
			}
			if query != "" {
				q.Set("q", query)
			}
			if sort != "" {
				q.Set("sort", sort)
			}
			path := "/v1/schemas"
			if encoded := q.Encode(); encoded != "" {
				path += "?" + encoded
			}

			data, err := client.do("GET", path, nil, false, nil)
			if err != nil {
				return err
			}

			var items []struct {
				Site           string `json:"site"`
				IntentCategory string `json:"intent_category"`
				VersionHash    string `json:"version_hash"`
			}
			if err := json.Unmarshal(data, &items); err != nil {
				return fmt.Errorf("decoding list response: %w", err)
			}
			if len(items) == 0 {
				fmt.Println("No schemas found.")
				return nil
			}
			for _, it := range items {
				fmt.Printf("%-30s  %-20s  %s\n", it.Site, it.IntentCategory, truncate(it.VersionHash, 12))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&category, "category", "", "Filter by intent category")
	cmd.Flags().StringVarP(&query, "query", "q", "", "Free-text site search")
	cmd.Flags().StringVar(&sort, "sort", "", "Sort: trending | recently_verified | recent")
	return cmd
}

func dashboardURL(apiURL string) string {
	// Map api.* → app.* (or hermai.dev) for the dashboard. Best-effort heuristic.
	if strings.HasPrefix(apiURL, "https://api.") {
		return "https://" + strings.TrimPrefix(apiURL, "https://api.") + "/dashboard"
	}
	return strings.TrimRight(apiURL, "/") + "/dashboard"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
