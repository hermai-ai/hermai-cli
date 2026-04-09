package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hermai-ai/hermai-cli/internal/config"
	"github.com/spf13/cobra"
)

func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage the schema cache",
	}

	cmd.AddCommand(newCacheListCmd())
	cmd.AddCommand(newCacheClearCmd())

	return cmd
}

func newCacheListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List cached domains and schema counts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			cacheDir := cfg.Cache.Dir

			entries, err := os.ReadDir(cacheDir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "No cached schemas found.")
					return nil
				}
				return fmt.Errorf("failed to read cache directory: %w", err)
			}

			found := false
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}

				domain := entry.Name()
				domainDir := filepath.Join(cacheDir, domain)

				schemaFiles, err := os.ReadDir(domainDir)
				if err != nil {
					return fmt.Errorf("failed to read domain directory %s: %w", domain, err)
				}

				count := 0
				for _, sf := range schemaFiles {
					if !sf.IsDir() && strings.HasSuffix(sf.Name(), ".json") {
						count++
					}
				}

				if count > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %d schema(s)\n", domain, count)
					found = true
				}
			}

			if !found {
				fmt.Fprintln(cmd.OutOrStdout(), "No cached schemas found.")
			}

			return nil
		},
	}
}

func newCacheClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear [domain]",
		Short: "Clear cached schemas (all or for a specific domain)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			cacheDir := cfg.Cache.Dir

			if len(args) == 1 {
				domain := args[0]
				if strings.ContainsAny(domain, `/\`) || strings.HasPrefix(domain, ".") {
					return fmt.Errorf("invalid domain name: %q", domain)
				}
				domainDir := filepath.Join(cacheDir, domain)
				absCache, _ := filepath.Abs(cacheDir)
				absDomain, _ := filepath.Abs(domainDir)
				if !strings.HasPrefix(absDomain, absCache+string(filepath.Separator)) {
					return fmt.Errorf("invalid domain name: %q", domain)
				}

				if err := os.RemoveAll(domainDir); err != nil {
					return fmt.Errorf("failed to clear cache for domain %s: %w", domain, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Cleared cache for %s\n", domain)
				return nil
			}

			entries, err := os.ReadDir(cacheDir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cache is already empty.")
					return nil
				}
				return fmt.Errorf("failed to read cache directory: %w", err)
			}

			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				domainDir := filepath.Join(cacheDir, entry.Name())
				if err := os.RemoveAll(domainDir); err != nil {
					return fmt.Errorf("failed to clear cache for %s: %w", entry.Name(), err)
				}
			}

			fmt.Fprintln(cmd.OutOrStdout(), "Cleared all cached schemas.")
			return nil
		},
	}
}
