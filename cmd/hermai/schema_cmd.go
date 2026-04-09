package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/hermai-ai/hermai-cli/pkg/config"
	"github.com/hermai-ai/hermai-cli/pkg/cache"
	"github.com/spf13/cobra"
)

func newSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema <url>",
		Short: "Show the cached schema for a URL",
		Long:  `Looks up the cached API schema for the given URL and displays it as indented JSON.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetURL := args[0]
			cfg := config.Load()

			c := cache.NewFileCache(cfg.Cache.Dir, cfg.Cache.TTL)

			s, err := c.Lookup(context.Background(), targetURL)
			if err != nil {
				return fmt.Errorf("schema lookup failed: %w", err)
			}

			if s == nil {
				fmt.Fprintln(os.Stderr, "No cached schema found for this URL.")
				return nil
			}

			output, err := json.MarshalIndent(s, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal schema: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), string(output))
			return nil
		},
	}
}
