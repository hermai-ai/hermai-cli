// Package browsercookies reads the user's locally-installed browser cookies
// and returns the subset that applies to a given site. It's the local
// alternative to running `hermai session bootstrap` — if the user has
// already logged in to a site in their default browser, we can skip the
// warm-up step entirely and just replay their existing session.
//
// Why this exists: actions on most sites (post a tweet, add to cart, RSVP)
// need the caller to be authenticated. Asking every user to go through a
// fresh login in a second browser window is friction. Reading cookies from
// the browser they actually use is zero-config — the first run shows an
// OS-level prompt (macOS Keychain / Windows DPAPI / libsecret) asking the
// user to grant access, after which Hermai can attach their existing
// session to outgoing requests.
//
// Privacy: the API always filters by domain. Asking for x.com cookies only
// ever reads cookies scoped to x.com. No cross-site reads, no generic cookie
// dumps, no credential handling on Hermai's side.
package browsercookies

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/browserutils/kooky"
	// Import all browser backends for side effects — this registers
	// Chrome, Firefox, Safari, Edge, Brave, Chromium, Opera, Vivaldi.
	_ "github.com/browserutils/kooky/browser/all"
)

// Source reads cookies from the user's locally-installed browsers.
type Source struct {
	// BrowserPreference, if set, restricts the search to the named browsers.
	// Values are kooky browser IDs: "chrome", "firefox", "safari", "edge",
	// "brave", "chromium", "opera", "vivaldi". When empty, every installed
	// browser is considered.
	BrowserPreference []string
}

// NewSource returns a Source that reads from every installed browser.
func NewSource() *Source {
	return &Source{}
}

// GetCookies returns cookies from the user's browsers that apply to the
// given site. The site must be a bare domain (e.g. "x.com", not
// "https://x.com/foo"); see NormalizeDomain.
//
// Results are domain-filtered at the source — no cookies for other sites
// are ever read into memory. When the same cookie exists in multiple
// browsers (e.g. the user is logged in on both Chrome and Firefox),
// the freshest value wins (most recent last-access or creation time).
//
// If no browsers are installed, or none contain cookies for the site,
// returns an empty slice and nil.
//
// First run on macOS surfaces a Keychain authorization prompt asking the
// user to allow hermai to read browser cookies. Windows uses DPAPI which
// is silent; Linux uses libsecret which may prompt depending on the
// user's keyring configuration.
func (s *Source) GetCookies(ctx context.Context, site string) ([]*http.Cookie, error) {
	domain, err := NormalizeDomain(site)
	if err != nil {
		return nil, err
	}

	// kooky's DomainHasSuffix matcher picks up both exact domain hits
	// ("x.com") and subdomain hits (".x.com", "api.x.com"). We want both.
	filters := []kooky.Filter{
		kooky.Valid,
		kooky.DomainHasSuffix(domain),
	}

	// TraverseCookies yields (cookie, err) pairs across every registered
	// backend. Per-store errors — "Edge not installed", "Safari container
	// locked", "Chromium Local State missing" — are expected on any given
	// machine and non-fatal. Skip them and keep accumulating hits; only
	// surface an error if EVERY backend failed and we collected nothing.
	var cookies []*kooky.Cookie
	var lastErr error
	for c, err := range kooky.TraverseCookies(ctx, filters...) {
		if err != nil {
			lastErr = err
			continue
		}
		if c != nil {
			cookies = append(cookies, c)
		}
	}
	if len(cookies) == 0 && lastErr != nil {
		// Representative error — likely "no browsers installed" or
		// permission-denied on all installed browsers. Caller decides how
		// to surface this; wrap it so they can inspect.
		return nil, fmt.Errorf("no browser cookies readable: %w", lastErr)
	}
	return s.dedupeAndConvert(cookies, domain), nil
}

// Count returns the number of applicable cookies without returning the
// values. Useful for a dry-run "do we have a session for this site?"
// check before the actual request.
func (s *Source) Count(ctx context.Context, site string) (int, error) {
	cookies, err := s.GetCookies(ctx, site)
	if err != nil {
		return 0, err
	}
	return len(cookies), nil
}

// dedupeAndConvert:
//  1. Drops cookies that don't apply to the target domain (defensive — the
//     filter should already have caught them, but a misbehaving backend
//     could leak).
//  2. When a cookie name appears in multiple browsers, keeps the one with
//     the most recent Expires/LastAccess (whichever the backend populated).
//  3. Converts kooky's Cookie into the stdlib http.Cookie shape so callers
//     don't need to know about kooky.
func (s *Source) dedupeAndConvert(in []*kooky.Cookie, domain string) []*http.Cookie {
	type entry struct {
		cookie *kooky.Cookie
		score  time.Time
	}
	best := make(map[string]entry)
	for _, c := range in {
		if c == nil {
			continue
		}
		if !cookieMatchesDomain(c.Domain, domain) {
			continue
		}
		score := c.Expires
		if c.Creation.After(score) {
			score = c.Creation
		}
		prev, seen := best[c.Name]
		if !seen || score.After(prev.score) {
			best[c.Name] = entry{cookie: c, score: score}
		}
	}

	out := make([]*http.Cookie, 0, len(best))
	for _, e := range best {
		c := e.cookie
		hc := &http.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Expires:  c.Expires,
			Secure:   c.Secure,
			HttpOnly: c.HttpOnly,
		}
		out = append(out, hc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NormalizeDomain reduces a schema "site" field or arbitrary URL string to
// the bare registrable domain used for cookie matching.
//
//	"x.com"              -> "x.com"
//	"https://x.com/path" -> "x.com"
//	"www.x.com"          -> "x.com"
//
// The normalization intentionally does NOT do eTLD+1 resolution via
// publicsuffix — cookies for a subdomain-only service ("api.example.com")
// should match that subdomain, not the parent. We rely on the registry
// schema to carry the correct cookie domain.
func NormalizeDomain(site string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(site))
	if s == "" {
		return "", fmt.Errorf("domain is required")
	}
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "www.")
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return "", fmt.Errorf("invalid domain: %q", site)
	}
	return s, nil
}

// cookieMatchesDomain decides whether a cookie stored for cookieDomain is
// applicable when the user asked for targetDomain.
//
// Rules (matching browser cookie semantics):
//   - A cookie for "x.com" or ".x.com" matches any x.com subdomain.
//   - A cookie for "api.x.com" only matches api.x.com (or its subdomains).
//   - Leading dots are treated as equivalent to no dot for matching purposes.
func cookieMatchesDomain(cookieDomain, targetDomain string) bool {
	cd := strings.TrimPrefix(strings.ToLower(cookieDomain), ".")
	td := strings.ToLower(targetDomain)
	if cd == td {
		return true
	}
	// Cookie set for a parent domain is sent to subdomains.
	if strings.HasSuffix(td, "."+cd) {
		return true
	}
	// Asking for the parent when the cookie is on a subdomain: NOT sent.
	return false
}
