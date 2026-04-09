package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hermai-ai/hermai-cli/pkg/config"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the hermai config file",
		Long:  "Interactively creates ~/.hermai/config.yaml with your LLM provider settings.",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := config.ConfigFilePath()

			if _, err := os.Stat(configPath); err == nil {
				overwrite, err := prompt("Config file already exists at %s. Overwrite? [y/N] ", configPath)
				if err != nil {
					return err
				}
				if !strings.HasPrefix(strings.ToLower(overwrite), "y") {
					fmt.Println("Aborted.")
					return nil
				}
			}

			reader := bufio.NewReader(os.Stdin)

			baseURL, err := promptWithDefault(reader, "LLM base URL", "https://api.openai.com/v1")
			if err != nil {
				return err
			}

			apiKey, err := promptRequired(reader, "API key")
			if err != nil {
				return err
			}

			model, err := promptWithDefault(reader, "Model", "gpt-4o-mini")
			if err != nil {
				return err
			}

			proxyURL, err := promptOptional(reader, "Proxy URL (leave empty for none)")
			if err != nil {
				return err
			}

			// Build YAML content
			var b strings.Builder
			b.WriteString(fmt.Sprintf("base_url: %s\n", baseURL))
			b.WriteString(fmt.Sprintf("api_key: %s\n", apiKey))
			b.WriteString(fmt.Sprintf("model: %s\n", model))
			if proxyURL != "" {
				b.WriteString(fmt.Sprintf("proxy: %s\n", proxyURL))
			}

			// Create directory
			dir := filepath.Dir(configPath)
			if err := os.MkdirAll(dir, 0700); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			// Write file with restricted permissions (contains API key)
			if err := os.WriteFile(configPath, []byte(b.String()), 0600); err != nil {
				return fmt.Errorf("failed to write config file: %w", err)
			}

			fmt.Printf("\nConfig saved to %s\n", configPath)
			fmt.Println("Run 'hermai fetch <url>' to get started.")
			return nil
		},
	}
}

func prompt(format string, args ...any) (string, error) {
	fmt.Printf(format, args...)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

func promptWithDefault(reader *bufio.Reader, label, defaultVal string) (string, error) {
	fmt.Printf("%s [%s]: ", label, defaultVal)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return defaultVal, nil
	}
	return input, nil
}

func promptRequired(reader *bufio.Reader, label string) (string, error) {
	for {
		fmt.Printf("%s: ", label)
		input, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		input = strings.TrimSpace(input)
		if input != "" {
			return input, nil
		}
		fmt.Println("  This field is required.")
	}
}

func promptOptional(reader *bufio.Reader, label string) (string, error) {
	fmt.Printf("%s: ", label)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}
