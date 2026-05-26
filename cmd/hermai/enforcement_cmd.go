package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/enforcement"
	"github.com/spf13/cobra"
)

func newEnforcementCmd() *cobra.Command {
	var (
		state   string
		limit   int
		timeout string
		delay   string
		format  string
	)

	cmd := &cobra.Command{
		Use:   "enforcement <root-url>",
		Short: "Extract state professional-license enforcement actions",
		Long: `Enforcement turns a state enforcement root URL into cited JSON records.

The first supported profile is Illinois IDFPR monthly disciplinary PDFs. It
discovers linked report PDFs, extracts page-aware enforcement actions, preserves
verbatim source text, and emits deterministic verification signals instead of
LLM confidence scores.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dur, err := parseTimeout(timeout, 2*time.Minute)
			if err != nil {
				return err
			}
			politeDelay, err := time.ParseDuration(delay)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), dur)
			defer cancel()

			client := &http.Client{Timeout: dur}
			switch strings.ToUpper(state) {
			case "IL":
				corpus, err := enforcement.FetchIllinoisCorpus(ctx, client, args[0], limit, politeDelay)
				if err != nil {
					return err
				}
				return writeJSON(os.Stdout, corpus, format)
			default:
				return errUnsupportedState(state)
			}
		},
	}

	cmd.Flags().StringVar(&state, "state", "IL", "State extractor to use")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum source PDFs to process; 0 means all")
	cmd.Flags().StringVar(&timeout, "timeout", "2m", "Overall extraction timeout")
	cmd.Flags().StringVar(&delay, "delay", "500ms", "Polite delay between source document downloads")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json (indented) or compact")
	return cmd
}

func errUnsupportedState(state string) error {
	return fmt.Errorf("unsupported enforcement state %s (supported: IL)", state)
}
