package browser

import (
	"bufio"
	"net/url"
	"os"
	"strings"
	"sync"
)

// spaDomainCache tracks domains where Lightpanda returned thin HTML
// (SPA not rendered). On subsequent visits, the browser skips Lightpanda
// and goes directly to Chromium, saving 5-10s of wasted Lightpanda time.
//
// File format: one domain per line in ~/.hermai/spa_domains.txt
// Lines starting with # are comments.
type spaDomainCache struct {
	mu      sync.RWMutex
	domains map[string]bool
	path    string
}

func newSPADomainCache(path string) *spaDomainCache {
	cache := &spaDomainCache{
		domains: make(map[string]bool),
		path:    path,
	}
	cache.load()
	return cache
}

func (c *spaDomainCache) load() {
	if c.path == "" {
		return
	}

	f, err := os.Open(c.path)
	if err != nil {
		return // file doesn't exist yet — that's fine
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		domain := strings.TrimSpace(scanner.Text())
		if domain != "" && !strings.HasPrefix(domain, "#") {
			c.domains[domain] = true
		}
	}
}

func (c *spaDomainCache) contains(domain string) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.domains[domain]
}

func (c *spaDomainCache) record(domain string) {
	if c == nil || domain == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.domains[domain] {
		return // already known
	}
	c.domains[domain] = true

	if c.path == "" {
		return
	}

	// Append to file (create if needed)
	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return // best-effort — don't fail the capture
	}
	defer f.Close()
	f.WriteString(domain + "\n")
}

// domainFromURL extracts the hostname from a URL string.
func domainFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}
