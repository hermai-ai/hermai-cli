package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidate_ValidConfig(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "sk-test",
			Model:   "gpt-4o-mini",
		},
		Cache: CacheConfig{
			Dir: "/tmp/hermai",
			TTL: 24 * time.Hour,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

func TestValidate_NoAPIKey(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			BaseURL: "https://api.openai.com/v1",
			Model:   "gpt-4o-mini",
		},
		Cache: CacheConfig{
			Dir: "/tmp/hermai",
			TTL: 24 * time.Hour,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config without API key should be valid, got: %v", err)
	}
}

func TestValidate_APIKeyWithoutBaseURL(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			APIKey: "sk-test",
			Model:  "gpt-4o-mini",
		},
		Cache: CacheConfig{
			Dir: "/tmp/hermai",
			TTL: 24 * time.Hour,
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for API key without base URL")
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Errorf("expected error about base_url, got: %v", err)
	}
}

func TestValidate_InvalidBaseURL(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			BaseURL: "not-a-url",
			APIKey:  "sk-test",
			Model:   "gpt-4o-mini",
		},
		Cache: CacheConfig{
			Dir: "/tmp/hermai",
			TTL: 24 * time.Hour,
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid base URL")
	}
}

func TestValidate_NegativeTTL(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "sk-test",
			Model:   "gpt-4o-mini",
		},
		Cache: CacheConfig{
			Dir: "/tmp/hermai",
			TTL: -1 * time.Hour,
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative TTL")
	}
}

func TestValidate_EmptyCacheDir(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "sk-test",
			Model:   "gpt-4o-mini",
		},
		Cache: CacheConfig{
			Dir: "",
			TTL: 24 * time.Hour,
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty cache dir")
	}
}

func TestValidate_NegativeTimeout(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "sk-test",
			Model:   "gpt-4o-mini",
		},
		Cache: CacheConfig{
			Dir: "/tmp/hermai",
			TTL: 24 * time.Hour,
		},
		Timeout: -5 * time.Second,
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative timeout")
	}
}
