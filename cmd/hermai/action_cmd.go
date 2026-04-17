package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
	"github.com/hermai-ai/hermai-cli/pkg/actionrunner"
	"github.com/hermai-ai/hermai-cli/pkg/config"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
	"github.com/spf13/cobra"
)

// newActionCmd implements `hermai action <site> <action>` — the
// runtime-side counterpart to `hermai registry pull` + `hermai registry
// push`. It loads a schema (from a local file or the local registry
// cache), resolves session state via the schema's bootstrap JS, runs
// the schema's signer JS per request, and fires the result.
//
// The CLI has NO per-site knowledge. All of x.com / tiktok.com / etc.
// lives in schema JSON. Adding a new site is a registry push, not a
// CLI release.
func newActionCmd() *cobra.Command {
	var (
		argsFlags  []string
		schemaFile string
		timeout    time.Duration
		dryRun     bool
	)

	cmd := &cobra.Command{
		Use:   "action <site> <action>",
		Short: "Call an authenticated endpoint on a site via its schema",
		Long: `Invokes a schema-defined action on a site, handling session resolution,
per-request signing, and Chrome-TLS-fingerprinted HTTP transport.

The schema defines everything: URL template, method, headers, body,
bootstrap JS (for computing session state like X's animation_key),
and signer JS (for per-request headers like x-client-transaction-id).

Schema sources (checked in order):
  --schema <file>                   Explicit local schema JSON
  ~/.hermai/schemas/<site>.json     Registry-pulled schema cache

Cookies are read from ~/.hermai/sessions/<site>/cookies.json. If absent,
Hermai reads them from your browser (first run triggers the OS keychain
prompt). Set-Cookie values from each response are rotated back.

Examples:
  hermai action x.com CreateDraftTweet --arg text="drafted by hermai"
  hermai action x.com CreateTweet --arg text="hi" --dry-run
  hermai action x.com GetProfile --schema ./schemas/x.com.json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			site := strings.ToLower(strings.TrimSpace(args[0]))
			actionName := strings.TrimSpace(args[1])

			sch, err := loadSchema(site, schemaFile)
			if err != nil {
				return err
			}
			if sch.Domain == "" {
				sch.Domain = site
			}

			parsedArgs, err := parseArgFlags(argsFlags)
			if err != nil {
				return err
			}

			ctx, cancel := signalContext(timeout)
			defer cancel()

			doer := httpclient.NewStealthOrFallback(httpclient.Options{Timeout: 30 * time.Second})

			result, err := actionrunner.Run(ctx, actionrunner.Request{
				Schema:      sch,
				ActionName:  actionName,
				Args:        parsedArgs,
				SessionsDir: sessionsDir(config.Load()),
				HTTPClient:  doer,
				DryRun:      dryRun,
			})
			if err != nil {
				return err
			}

			if dryRun {
				return printDryRunRequest(result.SignedReq, cmd.OutOrStdout())
			}

			if result.Bootstraps > 0 {
				fmt.Fprintln(os.Stderr, "bootstrapped session state (was missing or stale)")
			}

			return renderResult(result, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringArrayVar(&argsFlags, "arg", nil,
		"Action argument, repeatable: --arg key=value")
	cmd.Flags().StringVar(&schemaFile, "schema", "",
		"Path to a schema JSON file (overrides the registry cache)")
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Second,
		"Overall deadline for session resolve + sign + HTTP call")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print the signed request that would be sent, don't hit the network")

	return cmd
}

// loadSchema resolves a schema from --schema or the local registry cache.
// Returns an error that names both paths so users know where to look.
func loadSchema(site, override string) (*schema.Schema, error) {
	if override != "" {
		return readSchemaFile(override)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		cached := filepath.Join(home, ".hermai", "schemas", site+".json")
		if _, err := os.Stat(cached); err == nil {
			return readSchemaFile(cached)
		}
	}
	return nil, fmt.Errorf("no schema for %s — pass --schema <file> or run `hermai registry pull %s`", site, site)
}

func readSchemaFile(path string) (*schema.Schema, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema %s: %w", path, err)
	}
	var s schema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse schema %s: %w", path, err)
	}
	return &s, nil
}

// parseArgFlags turns ["text=hello", "lang=en"] into a flat map. The
// value may contain "=" — only the first one splits.
func parseArgFlags(args []string) (map[string]string, error) {
	out := map[string]string{}
	for _, a := range args {
		i := strings.Index(a, "=")
		if i <= 0 {
			return nil, fmt.Errorf("--arg must be key=value, got %q", a)
		}
		out[strings.TrimSpace(a[:i])] = a[i+1:]
	}
	return out, nil
}

// renderResult pretty-prints JSON responses; passes non-JSON through verbatim.
func renderResult(r *actionrunner.Result, out io.Writer) error {
	if r.Status >= 400 {
		fmt.Fprintf(os.Stderr, "HTTP %d\n", r.Status)
		fmt.Fprintln(os.Stderr, truncate(string(r.Body), 500))
		return fmt.Errorf("http %d", r.Status)
	}
	var pretty any
	if json.Unmarshal(r.Body, &pretty) == nil {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(pretty)
	}
	_, err := out.Write(r.Body)
	return err
}

// printDryRunRequest shows a curl-comparable rendering with sensitive
// values redacted so humans can eyeball the request shape.
func printDryRunRequest(req *http.Request, out io.Writer) error {
	fmt.Fprintf(out, "%s %s\n", req.Method, req.URL.String())
	for k, v := range req.Header {
		val := strings.Join(v, ", ")
		if k == "Cookie" || k == "Authorization" {
			val = redactMost(val)
		}
		fmt.Fprintf(out, "%s: %s\n", k, val)
	}
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		if len(body) > 0 {
			fmt.Fprintln(out)
			fmt.Fprintln(out, string(body))
		}
	}
	return nil
}

func redactMost(s string) string {
	if len(s) <= 16 {
		return "<redacted>"
	}
	return s[:8] + "…" + s[len(s)-4:]
}
