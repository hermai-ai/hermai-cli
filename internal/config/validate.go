package config

import (
	"fmt"
	"net/url"
	"strings"
)

// Validate checks that the configuration is internally consistent.
// Returns nil if valid, or a descriptive error for the first problem found.
func (c Config) Validate() error {
	if err := c.LLM.validate(); err != nil {
		return fmt.Errorf("LLM config: %w", err)
	}
	if err := c.Cache.validate(); err != nil {
		return fmt.Errorf("cache config: %w", err)
	}
	if c.Proxy != "" {
		u, err := url.Parse(c.Proxy)
		if err != nil {
			return fmt.Errorf("proxy URL invalid: %w", err)
		}
		if u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("proxy URL must include scheme and host (e.g., http://proxy:8080)")
		}
	}
	if c.Timeout < 0 {
		return fmt.Errorf("timeout must not be negative")
	}
	return nil
}

func (c LLMConfig) validate() error {
	if c.APIKey != "" && c.BaseURL == "" {
		return fmt.Errorf("base_url is required when api_key is set")
	}
	if c.BaseURL != "" {
		if !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
			return fmt.Errorf("base_url must start with http:// or https://")
		}
		if _, err := url.Parse(c.BaseURL); err != nil {
			return fmt.Errorf("base_url is not a valid URL: %w", err)
		}
	}
	if c.Model == "" && c.APIKey != "" {
		return fmt.Errorf("model is required when api_key is set")
	}
	return nil
}

func (c CacheConfig) validate() error {
	if c.TTL < 0 {
		return fmt.Errorf("TTL must not be negative")
	}
	if c.Dir == "" {
		return fmt.Errorf("cache directory must not be empty")
	}
	return nil
}
