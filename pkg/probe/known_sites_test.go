package probe

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestKnownSitePatternMatching(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantMatch string // expected site name, empty = no match
	}{
		// NPM
		{"npm package", "https://www.npmjs.com/package/express", "npm_package"},
		{"npm scoped package", "https://www.npmjs.com/package/@anthropic-ai/sdk", "npm_scoped_package"},
		{"npm homepage no match", "https://www.npmjs.com/", ""},

		// PyPI
		{"pypi package", "https://pypi.org/project/requests", "pypi_package"},
		{"pypi versioned", "https://pypi.org/project/requests/2.31.0/", "pypi_package"},
		{"pypi homepage no match", "https://pypi.org/", ""},

		// Yahoo Finance
		{"yahoo finance quote", "https://finance.yahoo.com/quote/AAPL", "yahoo_finance_quote"},
		{"yahoo finance quote trailing slash", "https://finance.yahoo.com/quote/TSLA/", "yahoo_finance_quote"},
		{"yahoo finance homepage no match", "https://finance.yahoo.com/", ""},

		// HN front page
		{"hn front page", "https://news.ycombinator.com/", "hn_front_page"},
		{"hn front page no slash", "https://news.ycombinator.com", "hn_front_page"},
		// hn_item should match /item paths, not front page
		{"hn item page", "https://news.ycombinator.com/item?id=12345", "hn_item"},

		// Existing sites still work
		{"github repo", "https://github.com/golang/go", "github_repo"},
		{"x tweet", "https://x.com/elonmusk/status/1234567890", "x_tweet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched := matchKnownSiteName(tt.url)
			if tt.wantMatch == "" {
				if matched != "" {
					t.Errorf("expected no match, got %q", matched)
				}
			} else if matched != tt.wantMatch {
				t.Errorf("got match %q, want %q", matched, tt.wantMatch)
			}
		})
	}
}

// matchKnownSiteName returns the name of the first matching known site pattern.
func matchKnownSiteName(targetURL string) string {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return ""
	}
	for _, site := range knownSites {
		if !site.HostPattern.MatchString(parsed.Hostname()) {
			continue
		}
		if !site.PathPattern.MatchString(parsed.Path) {
			continue
		}
		return site.Name
	}
	return ""
}

func TestKnownSiteBuildAPIURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantAPI string // substring that must appear in the built API URL
	}{
		{"npm express", "https://www.npmjs.com/package/express", "registry.npmjs.org/express"},
		{"pypi requests", "https://pypi.org/project/requests", "pypi.org/pypi/requests/json"},
		{"yahoo AAPL", "https://finance.yahoo.com/quote/AAPL", "chart/AAPL"},
		{"hn front page", "https://news.ycombinator.com/", "topstories.json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := url.Parse(tt.url)
			if err != nil {
				t.Fatalf("bad URL: %v", err)
			}

			for _, site := range knownSites {
				if !site.HostPattern.MatchString(parsed.Hostname()) || !site.PathPattern.MatchString(parsed.Path) {
					continue
				}

				vars := extractNamedGroups(site.HostPattern, parsed.Hostname())
				for k, v := range extractNamedGroups(site.PathPattern, parsed.Path) {
					vars[k] = v
				}
				for k, values := range parsed.Query() {
					if len(values) > 0 {
						vars[k] = values[0]
					}
				}
				segments := splitPathSegments(parsed.Path)
				for _, v := range site.Variables {
					if _, exists := vars[v.Name]; exists {
						continue
					}
					idx, idxErr := strconv.Atoi(v.Pattern)
					if idxErr != nil || idx < 0 || idx >= len(segments) {
						continue
					}
					switch v.Source {
					case "path":
						vars[v.Name] = segments[idx]
					case "path_rest":
						vars[v.Name] = strings.Join(segments[idx:], "/")
					}
				}

				apiURL := buildAPIURL(site.APIURLTemplate, vars)
				if !strings.Contains(apiURL, tt.wantAPI) {
					t.Errorf("API URL %q does not contain %q", apiURL, tt.wantAPI)
				}
				return
			}
			t.Errorf("no known site matched %s", tt.url)
		})
	}
}
