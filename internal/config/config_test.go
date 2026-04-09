package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Clear all HERMAI env vars to ensure defaults
	t.Setenv("HERMAI_BASE_URL", "")
	t.Setenv("HERMAI_API_KEY", "")
	t.Setenv("HERMAI_MODEL", "")
	t.Setenv("HERMAI_CACHE_DIR", "")
	t.Setenv("HERMAI_BROWSER_PATH", "")
	t.Setenv("HERMAI_PROXY", "")

	// Use nonexistent config file to test pure defaults
	cfg := loadFromFile("/nonexistent/config.yaml")

	if cfg.LLM.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("expected default BaseURL, got %q", cfg.LLM.BaseURL)
	}
	if cfg.LLM.APIKey != "" {
		t.Errorf("expected empty APIKey, got %q", cfg.LLM.APIKey)
	}
	if cfg.LLM.Model != "gpt-4o-mini" {
		t.Errorf("expected default Model, got %q", cfg.LLM.Model)
	}
	if cfg.Cache.TTL != 30*24*time.Hour {
		t.Errorf("expected 30 day TTL, got %v", cfg.Cache.TTL)
	}
	if cfg.Browser.Timeout != 20*time.Second {
		t.Errorf("expected 20s Timeout, got %v", cfg.Browser.Timeout)
	}
	if cfg.Browser.WaitAfterLoad != 1500*time.Millisecond {
		t.Errorf("expected 1.5s WaitAfterLoad, got %v", cfg.Browser.WaitAfterLoad)
	}
	if cfg.Browser.Path != "" {
		t.Errorf("expected empty browser path, got %q", cfg.Browser.Path)
	}
	if cfg.Proxy != "" {
		t.Errorf("expected empty proxy, got %q", cfg.Proxy)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv("HERMAI_BASE_URL", "https://custom.example.com/v1")
	t.Setenv("HERMAI_API_KEY", "sk-test-key-123")
	t.Setenv("HERMAI_MODEL", "gpt-4o")
	t.Setenv("HERMAI_CACHE_DIR", "/tmp/hermai-test")
	t.Setenv("HERMAI_BROWSER_PATH", "/usr/bin/chromium")
	t.Setenv("HERMAI_PROXY", "socks5://localhost:1080")

	cfg := Load()

	if cfg.LLM.BaseURL != "https://custom.example.com/v1" {
		t.Errorf("expected custom BaseURL, got %q", cfg.LLM.BaseURL)
	}
	if cfg.LLM.APIKey != "sk-test-key-123" {
		t.Errorf("expected custom APIKey, got %q", cfg.LLM.APIKey)
	}
	if cfg.LLM.Model != "gpt-4o" {
		t.Errorf("expected custom Model, got %q", cfg.LLM.Model)
	}
	if cfg.Cache.Dir != "/tmp/hermai-test" {
		t.Errorf("expected custom CacheDir, got %q", cfg.Cache.Dir)
	}
	if cfg.Browser.Path != "/usr/bin/chromium" {
		t.Errorf("expected custom BrowserPath, got %q", cfg.Browser.Path)
	}
	if cfg.Proxy != "socks5://localhost:1080" {
		t.Errorf("expected custom Proxy, got %q", cfg.Proxy)
	}
}

func TestParseTTLDays(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"7d", 7 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"1d", 24 * time.Hour},
	}

	for _, tc := range tests {
		d, err := ParseTTL(tc.input)
		if err != nil {
			t.Errorf("ParseTTL(%q) returned error: %v", tc.input, err)
			continue
		}
		if d != tc.expected {
			t.Errorf("ParseTTL(%q) = %v, want %v", tc.input, d, tc.expected)
		}
	}
}

func TestParseTTLStandardDurations(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"24h", 24 * time.Hour},
		{"1h30m", 90 * time.Minute},
		{"30m", 30 * time.Minute},
	}

	for _, tc := range tests {
		d, err := ParseTTL(tc.input)
		if err != nil {
			t.Errorf("ParseTTL(%q) returned error: %v", tc.input, err)
			continue
		}
		if d != tc.expected {
			t.Errorf("ParseTTL(%q) = %v, want %v", tc.input, d, tc.expected)
		}
	}
}

func TestParseTTLInvalid(t *testing.T) {
	_, err := ParseTTL("invalid")
	if err == nil {
		t.Error("expected error for invalid TTL, got nil")
	}
}

func TestLoadCacheDirDefault(t *testing.T) {
	t.Setenv("HERMAI_CACHE_DIR", "")

	cfg := Load()

	// Default should end with .hermai/schemas
	if cfg.Cache.Dir == "" {
		t.Error("expected non-empty default cache dir")
	}
}

func TestLoadConfigFile(t *testing.T) {
	// Clear env vars so config file values are used
	t.Setenv("HERMAI_BASE_URL", "")
	t.Setenv("HERMAI_API_KEY", "")
	t.Setenv("HERMAI_MODEL", "")
	t.Setenv("HERMAI_PROXY", "")

	// Write a temp config file
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	content := []byte(`base_url: https://openrouter.ai/api/v1
api_key: sk-or-v1-test
model: anthropic/claude-sonnet-4
proxy: http://proxy:8080
cache_ttl: 14d
`)
	if err := os.WriteFile(configPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	// Load using the temp config file
	cfg := loadFromFile(configPath)

	if cfg.LLM.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("BaseURL = %q, want openrouter", cfg.LLM.BaseURL)
	}
	if cfg.LLM.APIKey != "sk-or-v1-test" {
		t.Errorf("APIKey = %q", cfg.LLM.APIKey)
	}
	if cfg.LLM.Model != "anthropic/claude-sonnet-4" {
		t.Errorf("Model = %q", cfg.LLM.Model)
	}
	if cfg.Proxy != "http://proxy:8080" {
		t.Errorf("Proxy = %q", cfg.Proxy)
	}
	if cfg.Cache.TTL != 14*24*time.Hour {
		t.Errorf("TTL = %v, want 14 days", cfg.Cache.TTL)
	}
}

func TestEnvOverridesConfigFile(t *testing.T) {
	// Set env var that should override config file
	t.Setenv("HERMAI_MODEL", "gpt-4o")
	t.Setenv("HERMAI_BASE_URL", "")
	t.Setenv("HERMAI_API_KEY", "")
	t.Setenv("HERMAI_PROXY", "")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	content := []byte(`model: anthropic/claude-sonnet-4
base_url: https://openrouter.ai/api/v1
`)
	if err := os.WriteFile(configPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := loadFromFile(configPath)

	// Env var wins
	if cfg.LLM.Model != "gpt-4o" {
		t.Errorf("Model = %q, env var should override config file", cfg.LLM.Model)
	}
	// Config file used when env var empty
	if cfg.LLM.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("BaseURL = %q, config file should be used when env empty", cfg.LLM.BaseURL)
	}
}

func TestConfigFileMissing(t *testing.T) {
	// Config file doesn't exist — should fall back to defaults
	t.Setenv("HERMAI_BASE_URL", "")
	t.Setenv("HERMAI_API_KEY", "")
	t.Setenv("HERMAI_MODEL", "")

	cfg := loadFromFile("/nonexistent/path/config.yaml")

	if cfg.LLM.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("missing config file should use default, got %q", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Model != "gpt-4o-mini" {
		t.Errorf("missing config file should use default model, got %q", cfg.LLM.Model)
	}
}
