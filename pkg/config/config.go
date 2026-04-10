package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for the hermai CLI.
type Config struct {
	LLM      LLMConfig
	Cache    CacheConfig
	Browser  BrowserConfig
	Platform PlatformConfig
	Proxy    string
	Timeout  time.Duration // overall operation timeout (0 = no limit)
	Verbose  bool
}

// PlatformConfig holds settings for talking to the hermai hosted platform
// (the registry API at api.hermai.ai). The CLI needs an API key to push
// schemas and to fetch from the registry over the public catalog endpoints.
type PlatformConfig struct {
	URL string // base URL of the hosted platform, e.g. https://api.hermai.ai
	Key string // API key issued by the platform (hm_sk_...)
}

// LLMConfig holds settings for the OpenAI-compatible LLM client.
type LLMConfig struct {
	BaseURL       string
	APIKey        string
	Model         string
	ClassifyModel string // fast/cheap model for HAR NOISE/CANDIDATE classification (optional, defaults to Model)
}

// CacheConfig holds settings for the file-based schema cache.
type CacheConfig struct {
	Dir string
	TTL time.Duration
}

// BrowserConfig holds settings for the headless browser.
type BrowserConfig struct {
	Path          string
	CDPURL        string // WebSocket URL for remote CDP (Lightpanda, browserless.io)
	Timeout       time.Duration
	WaitAfterLoad time.Duration
}

// configFile is the YAML-serializable config file structure.
type configFile struct {
	BaseURL       string `yaml:"base_url"`
	APIKey        string `yaml:"api_key"`
	Model         string `yaml:"model"`
	ClassifyModel string `yaml:"classify_model"`
	CacheDir      string `yaml:"cache_dir"`
	CacheTTL      string `yaml:"cache_ttl"`
	BrowserPath   string `yaml:"browser_path"`
	CDPURL        string `yaml:"cdp_url"`
	Proxy         string `yaml:"proxy"`
	Timeout       string `yaml:"timeout"`
	Verbose       bool   `yaml:"verbose"`
	PlatformURL   string `yaml:"platform_url"`
	PlatformKey   string `yaml:"platform_key"`
}

// Load reads configuration with priority: env vars > config file > defaults.
// Config file location: ~/.hermai/config.yaml
func Load() Config {
	return loadFromFile(ConfigFilePath())
}

// loadFromFile reads configuration from a specific config file path.
func loadFromFile(path string) Config {
	file := loadConfigFileFrom(path)

	return Config{
		LLM: LLMConfig{
			BaseURL:       resolve("HERMAI_BASE_URL", file.BaseURL, "https://api.openai.com/v1"),
			APIKey:        resolve("HERMAI_API_KEY", file.APIKey, ""),
			Model:         resolve("HERMAI_MODEL", file.Model, "gpt-4o-mini"),
			ClassifyModel: resolve("HERMAI_CLASSIFY_MODEL", file.ClassifyModel, ""),
		},
		Cache: CacheConfig{
			Dir: resolve("HERMAI_CACHE_DIR", file.CacheDir, defaultCacheDir()),
			TTL: resolveTTL(file.CacheTTL, 30*24*time.Hour),
		},
		Browser: BrowserConfig{
			Path:          resolve("HERMAI_BROWSER_PATH", file.BrowserPath, ""),
			CDPURL:        resolve("HERMAI_CDP_URL", file.CDPURL, ""),
			Timeout:       20 * time.Second,
			WaitAfterLoad: 1500 * time.Millisecond,
		},
		Platform: PlatformConfig{
			URL: resolve("HERMAI_PLATFORM_URL", file.PlatformURL, "https://api.hermai.ai"),
			Key: resolve("HERMAI_PLATFORM_KEY", file.PlatformKey, ""),
		},
		Proxy:   resolve("HERMAI_PROXY", file.Proxy, ""),
		Timeout: resolveTimeout("HERMAI_TIMEOUT", file.Timeout),
		Verbose: resolveBool("HERMAI_VERBOSE", file.Verbose),
	}
}

// SavePlatformKey persists the platform API key to the user's config file,
// preserving any existing fields. The directory and file are created with
// 0700/0600 permissions if they don't exist.
func SavePlatformKey(key string) error {
	path := ConfigFilePath()
	cf := loadConfigFileFrom(path)
	cf.PlatformKey = key
	return writeConfigFile(path, cf)
}

func writeConfigFile(path string, cf configFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := yaml.Marshal(cf)
	if err != nil {
		return fmt.Errorf("marshalling config: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// ConfigFilePath returns the path to the config file.
func ConfigFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".hermai", "config.yaml")
	}
	return filepath.Join(home, ".hermai", "config.yaml")
}

// loadConfigFileFrom reads and parses a YAML config file at the given path.
func loadConfigFileFrom(path string) configFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return configFile{}
	}

	var cf configFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return configFile{}
	}

	return cf
}

// resolve returns the first non-empty value from: env var, config file, default.
func resolve(envKey, fileValue, defaultValue string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	if fileValue != "" {
		return fileValue
	}
	return defaultValue
}

// resolveTTL parses the config file TTL string, falling back to the default.
func resolveTTL(fileTTL string, defaultTTL time.Duration) time.Duration {
	if fileTTL == "" {
		return defaultTTL
	}
	d, err := ParseTTL(fileTTL)
	if err != nil {
		return defaultTTL
	}
	return d
}

// resolveTimeout resolves a timeout from env var or config file. Returns 0 if unset.
func resolveTimeout(envKey, fileValue string) time.Duration {
	if v := os.Getenv(envKey); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	if fileValue != "" {
		d, err := time.ParseDuration(fileValue)
		if err == nil {
			return d
		}
	}
	return 0
}

// resolveBool resolves a boolean from env var or config file.
func resolveBool(envKey string, fileValue bool) bool {
	if v := os.Getenv(envKey); v != "" {
		return v == "1" || v == "true" || v == "yes"
	}
	return fileValue
}

// ParseTTL parses a duration string that supports day notation ("7d", "30d")
// in addition to standard Go duration strings ("24h", "1h30m").
func ParseTTL(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		dayStr := strings.TrimSuffix(s, "d")
		days, err := strconv.Atoi(dayStr)
		if err != nil {
			return 0, fmt.Errorf("invalid day count in TTL %q: %w", s, err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid TTL %q: %w", s, err)
	}

	return d, nil
}

// defaultCacheDir returns the default cache directory: ~/.hermai/schemas.
func defaultCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".hermai", "schemas")
	}
	return filepath.Join(home, ".hermai", "schemas")
}
