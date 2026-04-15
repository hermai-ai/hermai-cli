package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/hermai-ai/hermai-cli/pkg/htmlext"
	"github.com/spf13/cobra"
)

func newExtractCmd() *cobra.Command {
	var (
		pattern      string
		listPatterns bool
		baseURL      string
		format       string
	)

	cmd := &cobra.Command{
		Use:   "extract [file]",
		Short: "Extract embedded data patterns from HTML",
		Long: `Extract scans HTML for known embedded data patterns — SSR state,
JSON-LD, __NEXT_DATA__, YouTube's ytInitialData, TikTok's hydration data,
and 10+ other patterns that websites embed as structured data in their pages.

Reads from a file path or stdin. Outputs JSON with all found patterns.

No API key or network access required — purely deterministic HTML parsing.

Examples:
  hermai extract page.html
  curl -s https://example.com | hermai extract
  hermai extract --pattern ytInitialData page.html
  hermai extract --list-patterns`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if listPatterns {
				return writeJSON(os.Stdout, htmlext.ListPatterns(), format)
			}

			rawHTML, err := readHTMLInput(args)
			if err != nil {
				return err
			}

			if pattern != "" {
				data := htmlext.ExtractSinglePattern(rawHTML, pattern)
				if data == nil {
					return fmt.Errorf("pattern %q not found in HTML", pattern)
				}
				return writeJSON(os.Stdout, data, format)
			}

			// Full extraction: Extract() already runs the pattern library internally.
			page := htmlext.Extract(rawHTML, baseURL)
			output := buildExtractOutput(page)
			return writeJSON(os.Stdout, output, format)
		},
	}

	cmd.Flags().StringVar(&pattern, "pattern", "", "Extract only a specific pattern (e.g. ytInitialData)")
	cmd.Flags().BoolVar(&listPatterns, "list-patterns", false, "List all known embedded data patterns")
	cmd.Flags().StringVar(&baseURL, "url", "", "Base URL for resolving relative links")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json (indented) or compact")

	return cmd
}

// maxHTMLInputSize caps file/stdin reads to avoid unbounded memory use.
const maxHTMLInputSize = 50 * 1024 * 1024 // 50 MB

func readHTMLInput(args []string) (string, error) {
	if len(args) > 0 {
		f, err := os.Open(args[0])
		if err != nil {
			return "", fmt.Errorf("failed to open %s: %w", args[0], err)
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxHTMLInputSize))
		if err != nil {
			return "", fmt.Errorf("failed to read %s: %w", args[0], err)
		}
		return string(data), nil
	}

	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return "", fmt.Errorf("no input: provide a file path or pipe HTML via stdin")
	}

	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxHTMLInputSize))
	if err != nil {
		return "", fmt.Errorf("failed to read stdin: %w", err)
	}
	return string(data), nil
}

func buildExtractOutput(page htmlext.PageContent) map[string]any {
	output := make(map[string]any)

	if page.Title != "" {
		output["title"] = page.Title
	}
	if page.Description != "" {
		output["description"] = page.Description
	}
	if page.Language != "" {
		output["language"] = page.Language
	}
	if page.Canonical != "" {
		output["canonical"] = page.Canonical
	}
	if len(page.OpenGraph) > 0 {
		output["open_graph"] = page.OpenGraph
	}
	if len(page.Meta) > 0 {
		output["meta"] = page.Meta
	}
	if len(page.JSONLD) > 0 {
		output["json_ld"] = page.JSONLD
	}
	// Unify __NEXT_DATA__ with the other embedded-script patterns under a
	// single `patterns` key. Agents read one field to find any SSR/hydration
	// payload regardless of framework.
	if page.NextData != nil || len(page.EmbeddedScripts) > 0 {
		patterns := make(map[string]any, len(page.EmbeddedScripts)+1)
		for k, v := range page.EmbeddedScripts {
			patterns[k] = v
		}
		if page.NextData != nil {
			patterns["__NEXT_DATA__"] = page.NextData
		}
		output["patterns"] = patterns
	}
	if len(page.Forms) > 0 {
		output["forms"] = page.Forms
	}

	return output
}

func writeJSON(w io.Writer, data any, format string) error {
	var out []byte
	var err error
	if format == "compact" {
		out, err = json.Marshal(data)
	} else {
		out, err = json.MarshalIndent(data, "", "  ")
	}
	if err != nil {
		return fmt.Errorf("failed to marshal output: %w", err)
	}
	out = append(out, '\n')
	_, err = w.Write(out)
	return err
}
