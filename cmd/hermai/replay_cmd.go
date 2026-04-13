package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/probe"
	"github.com/spf13/cobra"
)

type replayRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

func newReplayCmd() *cobra.Command {
	var (
		stealth  bool
		proxyURL string
		timeout  string
		insecure bool
		format   string
		body     bool
	)

	cmd := &cobra.Command{
		Use:   "replay <request.json>",
		Short: "Replay a captured HTTP request with TLS fingerprinting",
		Long: `Replay reads a JSON request spec (method, URL, headers, body) and
executes it with optional Chrome TLS fingerprinting. Use this to verify
that a captured API call works standalone — outside the browser.

The JSON input can come from a file or stdin (e.g., from intercept output).

Examples:
  hermai replay request.json
  hermai replay --stealth request.json
  echo '{"method":"GET","url":"https://api.example.com/data"}' | hermai replay -
  hermai replay --body request.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var reqData []byte
			var err error

			if args[0] == "-" {
				reqData, err = io.ReadAll(io.LimitReader(os.Stdin, maxHTMLInputSize))
			} else {
				f, openErr := os.Open(args[0])
				if openErr != nil {
					return fmt.Errorf("failed to open %s: %w", args[0], openErr)
				}
				defer f.Close()
				reqData, err = io.ReadAll(io.LimitReader(f, maxHTMLInputSize))
			}
			if err != nil {
				return fmt.Errorf("failed to read request: %w", err)
			}

			var spec replayRequest
			if err := json.Unmarshal(reqData, &spec); err != nil {
				return fmt.Errorf("invalid request JSON: %w", err)
			}
			if spec.URL == "" {
				return fmt.Errorf("request JSON missing 'url' field")
			}
			if spec.Method == "" {
				spec.Method = "GET"
			}

			dur, err := parseTimeout(timeout, 10*time.Second)
			if err != nil {
				return err
			}
			opts := buildProbeOpts(proxyURL, stealth, insecure, dur)

			ctx, cancel := signalContext()
			defer cancel()

			client := probe.NewClient(opts)

			var reqBody io.Reader
			if spec.Body != "" {
				reqBody = bytes.NewBufferString(spec.Body)
			}

			req, err := http.NewRequestWithContext(ctx, spec.Method, spec.URL, reqBody)
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}

			// Apply browser defaults first, then overlay spec headers so
			// the replay faithfully matches what was captured.
			probe.SetBrowserHeaders(req)
			for k, v := range spec.Headers {
				req.Header.Set(k, v)
			}

			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLInputSize))
			if err != nil {
				return fmt.Errorf("failed to read response: %w", err)
			}

			if body {
				os.Stdout.Write(respBody)
				return nil
			}

			output := map[string]any{
				"status":       resp.StatusCode,
				"status_text":  resp.Status,
				"content_type": resp.Header.Get("Content-Type"),
				"size_bytes":   len(respBody),
			}

			headers := make(map[string]string)
			for k := range resp.Header {
				headers[k] = resp.Header.Get(k)
			}
			output["headers"] = headers

			// Try to parse body as JSON
			var jsonBody any
			if err := json.Unmarshal(respBody, &jsonBody); err == nil {
				output["body"] = jsonBody
			} else {
				output["body_text"] = string(respBody[:min(len(respBody), 2000)])
				if len(respBody) > 2000 {
					output["body_truncated"] = true
				}
			}

			return writeJSON(os.Stdout, output, format)
		},
	}

	cmd.Flags().BoolVar(&stealth, "stealth", false, "Use Chrome TLS fingerprinting")
	cmd.Flags().StringVar(&proxyURL, "proxy", "", "Proxy URL")
	cmd.Flags().StringVar(&timeout, "timeout", "10s", "Request timeout")
	cmd.Flags().BoolVarP(&insecure, "insecure", "k", false, "Skip TLS verification")
	cmd.Flags().BoolVar(&body, "body", false, "Output raw response body (for piping)")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or compact")

	return cmd
}
