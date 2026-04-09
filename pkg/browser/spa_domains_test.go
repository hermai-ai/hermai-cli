package browser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsRenderedHTMLThin(t *testing.T) {
	tests := []struct {
		name string
		html string
		want bool
	}{
		{
			name: "empty",
			html: "",
			want: true,
		},
		{
			name: "SPA shell only",
			html: `<html><head><title>App</title></head><body><div id="root"></div></body></html>`,
			want: true,
		},
		{
			name: "SPA shell with noscript",
			html: `<html><head><title>App</title></head><body><div id="root"></div><noscript>Enable JS</noscript></body></html>`,
			want: true,
		},
		{
			name: "real content",
			html: `<html><head><title>Blog</title></head><body><main><article><h1>Hello World</h1><p>` +
				string(make([]byte, 600)) + `</p></article></main></body></html>`,
			want: false,
		},
		{
			name: "no body tag",
			html: `<html><p>short</p></html>`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRenderedHTMLThin(tt.html)
			if got != tt.want {
				t.Errorf("isRenderedHTMLThin() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSPADomainCache_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "spa_domains.txt")

	// Create fresh cache — file doesn't exist yet
	cache := newSPADomainCache(path)
	if cache.contains("example.com") {
		t.Fatal("empty cache should not contain example.com")
	}

	// Record a domain
	cache.record("example.com")
	if !cache.contains("example.com") {
		t.Fatal("cache should contain example.com after record")
	}

	// Verify file was written
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read spa_domains.txt: %v", err)
	}
	if string(data) != "example.com\n" {
		t.Errorf("unexpected file content: %q", string(data))
	}

	// Load a new cache from the same file — should find the domain
	cache2 := newSPADomainCache(path)
	if !cache2.contains("example.com") {
		t.Fatal("reloaded cache should contain example.com")
	}
}

func TestSPADomainCache_DuplicateRecord(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "spa_domains.txt")

	cache := newSPADomainCache(path)
	cache.record("spa.example.com")
	cache.record("spa.example.com") // duplicate

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(data) != "spa.example.com\n" {
		t.Errorf("duplicate should not be written, got: %q", string(data))
	}
}

func TestSPADomainCache_NilSafe(t *testing.T) {
	var cache *spaDomainCache
	// These should not panic
	if cache.contains("anything") {
		t.Error("nil cache should not contain anything")
	}
	cache.record("anything") // should be a no-op
}

func TestSPADomainCache_CommentsIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "spa_domains.txt")

	os.WriteFile(path, []byte("# This is a comment\nspa.example.com\n# Another comment\n"), 0644)

	cache := newSPADomainCache(path)
	if !cache.contains("spa.example.com") {
		t.Fatal("should contain spa.example.com")
	}
	if cache.contains("# This is a comment") {
		t.Fatal("should not contain comment lines")
	}
}

func TestDomainFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://www.imdb.com/chart/top/", "www.imdb.com"},
		{"https://nba.com/", "nba.com"},
		{"http://localhost:8080/test", "localhost"},
		{"not-a-url", ""},
	}

	for _, tt := range tests {
		got := domainFromURL(tt.url)
		if got != tt.want {
			t.Errorf("domainFromURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestStripHTMLTags(t *testing.T) {
	input := `<div><h1>Hello</h1><p>World <strong>bold</strong></p></div>`
	got := stripHTMLTags(input)
	want := "HelloWorld bold"
	if got != want {
		t.Errorf("stripHTMLTags() = %q, want %q", got, want)
	}
}
