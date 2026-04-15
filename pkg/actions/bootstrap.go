package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

// BootstrapRequest describes a session bootstrap: which URL to warm, what
// cookies must appear, and where to save the result.
type BootstrapRequest struct {
	// Site is the registry key, e.g. "tiktok.com". Used for storage path.
	Site string
	// BootstrapURL is the warm-up URL the browser navigates to first.
	BootstrapURL string
	// RequiredCookies is the list of cookie names the caller expects to see
	// set after navigation. Bootstrap keeps waiting (up to Timeout) until all
	// of them are present, so TLS-clients can replay with a valid session.
	RequiredCookies []string
	// Timeout caps the whole navigate + wait operation. Defaults to 45s.
	Timeout time.Duration
	// BrowserPath overrides the Chrome binary if set; otherwise rod picks.
	BrowserPath string
	// Headless runs Chrome without a visible window. Default true. Some
	// sites detect classic headless more aggressively — flip to false for
	// the toughest targets at the cost of a visible Chrome window.
	Headless bool
	// StorageDir is the parent directory where per-site cookie jars live.
	// Typically ~/.hermai/sessions. BootstrapSession writes to
	// {StorageDir}/{Site}/cookies.json.
	StorageDir string
}

// BootstrapResult summarizes a successful bootstrap.
type BootstrapResult struct {
	Site          string
	CookieCount   int
	RequiredFound []string // which required_cookies were actually set
	RequiredMiss  []string // required_cookies that never appeared
	StoragePath   string   // absolute path to the saved cookies.json
	Duration      time.Duration
}

// CookieFile is the persistence format for session cookies. Values are kept
// on the user's disk only; they never leave the local machine.
type CookieFile struct {
	Site      string            `json:"site"`
	SavedAt   time.Time         `json:"saved_at"`
	Domain    string            `json:"domain"`
	Cookies   map[string]string `json:"cookies"`
	Required  []string          `json:"required_cookies,omitempty"`
}

// ErrBootstrapTimeout is returned when required_cookies never appear before
// the deadline. The partial cookie set may still be useful for debugging;
// call BootstrapSession with Headless=false to watch what the browser is
// doing if this keeps firing.
var ErrBootstrapTimeout = errors.New("bootstrap timed out waiting for required cookies")

// BootstrapSession warms a browser page at req.BootstrapURL, waits for the
// cookies named in req.RequiredCookies to appear, then dumps every cookie
// scoped to the target domain to {StorageDir}/{Site}/cookies.json. The
// cookie file is the handoff surface: other Hermai CLI commands (and any
// Go/Python client) can read it and attach the cookies to their own
// HTTPS requests via a Chrome-TLS client.
//
// This is the entry point for the `hermai session bootstrap <site>` flow.
// It models the same shape as browserClearance() in clearance.go but with
// a named site key, explicit required-cookie wait, and persistent storage.
func BootstrapSession(ctx context.Context, req BootstrapRequest) (*BootstrapResult, error) {
	if req.Site == "" {
		return nil, errors.New("bootstrap: Site is required")
	}
	if req.BootstrapURL == "" {
		return nil, errors.New("bootstrap: BootstrapURL is required")
	}
	if req.StorageDir == "" {
		return nil, errors.New("bootstrap: StorageDir is required")
	}
	if req.Timeout <= 0 {
		req.Timeout = 45 * time.Second
	}

	start := time.Now()

	l := launcher.New().Headless(req.Headless).Leakless(false).
		Set("disable-blink-features", "AutomationControlled")
	if req.BrowserPath != "" {
		l = l.Bin(req.BrowserPath)
	}
	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: launch browser: %w", err)
	}
	defer l.Kill()

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("bootstrap: connect: %w", err)
	}
	defer browser.Close()

	page, err := stealth.Page(browser)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: stealth page: %w", err)
	}
	defer page.Close()

	navCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	page = page.Context(navCtx)

	if err := page.Navigate(req.BootstrapURL); err != nil {
		return nil, fmt.Errorf("bootstrap: navigate %s: %w", req.BootstrapURL, err)
	}

	// Wait for DOM to stabilize so anti-bot scripts finish their cookie writes.
	_ = page.WaitStable(3 * time.Second)

	// Poll for required_cookies with a short backoff. Most schemas set their
	// cookies in the first 1-5 seconds; harder ones (webmssdk, PerimeterX) can
	// take up to 20 seconds. Give up at req.Timeout.
	found, missing := waitForRequiredCookies(navCtx, page, req.RequiredCookies, req.Timeout)

	// Dump the full cookie jar regardless of whether all required cookies
	// appeared — even a partial set is often useful for debugging, and
	// downstream tls-clients can replay what we got.
	allCookies, err := proto.NetworkGetAllCookies{}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: read cookies: %w", err)
	}
	domain := domainFromBootstrapURL(req.BootstrapURL)
	cookies := make(map[string]string)
	for _, c := range allCookies.Cookies {
		cookieDomain := strings.TrimPrefix(c.Domain, ".")
		if domainMatches(cookieDomain, domain) {
			cookies[c.Name] = c.Value
		}
	}

	// Persist to {StorageDir}/{Site}/cookies.json
	siteDir := filepath.Join(req.StorageDir, req.Site)
	if err := os.MkdirAll(siteDir, 0700); err != nil {
		return nil, fmt.Errorf("bootstrap: mkdir %s: %w", siteDir, err)
	}
	storagePath := filepath.Join(siteDir, "cookies.json")
	file := CookieFile{
		Site:     req.Site,
		SavedAt:  time.Now().UTC(),
		Domain:   domain,
		Cookies:  cookies,
		Required: req.RequiredCookies,
	}
	body, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("bootstrap: marshal cookie file: %w", err)
	}
	if err := os.WriteFile(storagePath, body, 0600); err != nil {
		return nil, fmt.Errorf("bootstrap: write %s: %w", storagePath, err)
	}

	res := &BootstrapResult{
		Site:          req.Site,
		CookieCount:   len(cookies),
		RequiredFound: found,
		RequiredMiss:  missing,
		StoragePath:   storagePath,
		Duration:      time.Since(start),
	}
	if len(missing) > 0 && len(req.RequiredCookies) > 0 {
		return res, fmt.Errorf("%w: missing %v", ErrBootstrapTimeout, missing)
	}
	return res, nil
}

// waitForRequiredCookies polls the page until every name in required is set,
// or the context deadline fires. Returns the two disjoint sets.
func waitForRequiredCookies(ctx context.Context, page *rod.Page, required []string, timeout time.Duration) (found, missing []string) {
	if len(required) == 0 {
		return nil, nil
	}
	needed := make(map[string]struct{}, len(required))
	for _, n := range required {
		needed[n] = struct{}{}
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		all, err := proto.NetworkGetAllCookies{}.Call(page)
		if err != nil {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		have := make(map[string]struct{})
		for _, c := range all.Cookies {
			have[c.Name] = struct{}{}
		}
		foundAll := true
		for name := range needed {
			if _, ok := have[name]; !ok {
				foundAll = false
				break
			}
		}
		if foundAll {
			for name := range needed {
				found = append(found, name)
			}
			return found, nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Deadline hit — report what we saw vs what was expected.
	all, err := proto.NetworkGetAllCookies{}.Call(page)
	if err == nil {
		have := make(map[string]struct{})
		for _, c := range all.Cookies {
			have[c.Name] = struct{}{}
		}
		for name := range needed {
			if _, ok := have[name]; ok {
				found = append(found, name)
			} else {
				missing = append(missing, name)
			}
		}
	} else {
		for name := range needed {
			missing = append(missing, name)
		}
	}
	return found, missing
}

// LoadCookieFile reads a previously-stored cookie jar for a site. Returns
// nil, nil if the file doesn't exist (i.e. the site has never been
// bootstrapped). Intended for hermai-cli commands that want to attach a
// warm session to their HTTPS requests.
func LoadCookieFile(storageDir, site string) (*CookieFile, error) {
	path := filepath.Join(storageDir, site, "cookies.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var file CookieFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &file, nil
}

func domainFromBootstrapURL(u string) string {
	s := strings.TrimPrefix(u, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "www.")
}

func domainMatches(cookieDomain, targetDomain string) bool {
	cookieDomain = strings.TrimPrefix(cookieDomain, "www.")
	if cookieDomain == targetDomain {
		return true
	}
	// Suffix match either direction (e.g. tiktok.com ⇔ www.tiktok.com).
	if strings.HasSuffix(cookieDomain, "."+targetDomain) {
		return true
	}
	if strings.HasSuffix(targetDomain, "."+cookieDomain) {
		return true
	}
	return false
}
