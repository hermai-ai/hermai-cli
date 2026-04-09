package actions

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/hermai-ai/hermai-cli/internal/httpclient"
	"github.com/hermai-ai/hermai-cli/pkg/cache"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// ClearanceResult holds cookies obtained through bootstrap or browser clearance.
type ClearanceResult struct {
	Cookies map[string]string // name → value
	Source  string            // "cache", "bootstrap", or "browser"
}

// cookieHeader formats the cookies map as a single Cookie header value.
func (cr *ClearanceResult) cookieHeader() string {
	if cr == nil || len(cr.Cookies) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(cr.Cookies))
	for k, v := range cr.Cookies {
		pairs = append(pairs, k+"="+v)
	}
	return strings.Join(pairs, "; ")
}

// obtainClearance tries to load cached clearance cookies, falling back to
// an HTTP bootstrap request against the domain homepage.
func obtainClearance(ctx context.Context, targetURL string, c cache.Service, opts HTTPOptions) *ClearanceResult {
	domain, err := schema.ExtractDomain(targetURL)
	if err != nil {
		return nil
	}

	// Step 1: try cached clearance cookies from schema SessionConfig
	if c != nil {
		if cached := loadCachedClearance(ctx, c, targetURL); cached != nil {
			return cached
		}
	}

	// Step 2: HTTP bootstrap — hit the homepage to collect session cookies
	homepageURL := "https://" + domain + "/"
	cookies := httpBootstrap(ctx, homepageURL, opts)
	if len(cookies) == 0 {
		return nil
	}

	return &ClearanceResult{Cookies: cookies, Source: "bootstrap"}
}

// loadCachedClearance reads ClearanceCookies from the cached schema's SessionConfig.
func loadCachedClearance(ctx context.Context, c cache.Service, targetURL string) *ClearanceResult {
	s, err := c.Lookup(ctx, targetURL)
	if err != nil || s == nil || s.Session == nil || len(s.Session.ClearanceCookies) == 0 {
		return nil
	}
	return &ClearanceResult{
		Cookies: s.Session.ClearanceCookies,
		Source:  "cache",
	}
}

// httpBootstrap performs a plain GET to the homepage and collects all Set-Cookie values.
func httpBootstrap(ctx context.Context, homepageURL string, opts HTTPOptions) map[string]string {
	// Keep concrete *http.Client here — we need Jar access for redirect-chain cookies.
	client := httpclient.New(httpclient.Options{
		ProxyURL: opts.ProxyURL,
		Insecure: opts.Insecure,
		WithJar:  true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, homepageURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	cookies := make(map[string]string)
	for _, c := range resp.Cookies() {
		cookies[c.Name] = c.Value
	}
	// Also check the jar — redirect chains may have set cookies earlier
	if parsed, parseErr := url.Parse(homepageURL); parseErr == nil && client.Jar != nil {
		for _, c := range client.Jar.Cookies(parsed) {
			if _, exists := cookies[c.Name]; !exists {
				cookies[c.Name] = c.Value
			}
		}
	}
	return cookies
}

// browserClearance launches a Rod browser with stealth to solve an anti-bot
// challenge, then extracts all cookies for the target domain.
func browserClearance(ctx context.Context, targetURL, browserPath string) (map[string]string, error) {
	l := launcher.New().Headless(true).Leakless(false)
	if browserPath != "" {
		l = l.Bin(browserPath)
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("clearance: failed to launch browser: %w", err)
	}

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		l.Kill()
		return nil, fmt.Errorf("clearance: failed to connect browser: %w", err)
	}
	defer func() {
		browser.Close()
		l.Kill()
	}()

	page, err := stealth.Page(browser)
	if err != nil {
		return nil, fmt.Errorf("clearance: failed to create stealth page: %w", err)
	}
	defer page.Close()

	clearCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	page = page.Context(clearCtx)

	if err := page.Navigate(targetURL); err != nil {
		return nil, fmt.Errorf("clearance: navigation failed: %w", err)
	}

	// Wait for the page to stabilize after initial load
	_ = page.WaitStable(2 * time.Second)

	// Some challenges auto-resolve (Akamai). For interactive challenges
	// (PerimeterX press-and-hold), try to find and interact with the button.
	solveInteractiveChallenge(page)

	// Wait again for the challenge to resolve after interaction
	_ = page.WaitStable(3 * time.Second)

	// Extract all cookies via CDP
	result, err := proto.NetworkGetAllCookies{}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("clearance: failed to get cookies: %w", err)
	}

	domain := domainFromTargetURL(targetURL)
	cookies := make(map[string]string)
	for _, c := range result.Cookies {
		// Only keep cookies scoped to the target domain
		if strings.Contains(domain, strings.TrimPrefix(c.Domain, ".")) ||
			strings.Contains(strings.TrimPrefix(c.Domain, "."), domain) {
			cookies[c.Name] = c.Value
		}
	}

	if len(cookies) == 0 {
		return nil, fmt.Errorf("clearance: browser returned no cookies for %s", domain)
	}
	return cookies, nil
}

// persistClearance saves clearance cookies into the schema's SessionConfig in cache.
func persistClearance(ctx context.Context, targetURL string, cookies map[string]string, c cache.Service) {
	if c == nil || len(cookies) == 0 {
		return
	}

	s, err := c.Lookup(ctx, targetURL)
	if err != nil || s == nil {
		// No existing schema — create a minimal one to hold the clearance cookies
		domain, domErr := schema.ExtractDomain(targetURL)
		if domErr != nil {
			return
		}
		parsed, parseErr := url.Parse(targetURL)
		if parseErr != nil {
			return
		}
		s = &schema.Schema{
			ID:         schema.GenerateID(domain, parsed.Path),
			Domain:     domain,
			URLPattern: parsed.Path,
			SchemaType: schema.SchemaTypeAPI,
			Version:    1,
			CreatedAt:  time.Now(),
			Session: &schema.SessionConfig{
				ClearanceCookies: cookies,
			},
		}
		_ = c.Store(ctx, s)
		return
	}

	if s.Session == nil {
		s.Session = &schema.SessionConfig{}
	}
	s.Session.ClearanceCookies = cookies
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	_ = c.Store(ctx, s)
}

// solveInteractiveChallenge attempts to interact with common anti-bot challenge
// elements. PerimeterX uses a "press & hold" button; Cloudflare uses a checkbox.
// This is best-effort — if the selectors don't match, we just continue and hope
// the challenge auto-resolves or was already solved.
func solveInteractiveChallenge(page *rod.Page) {
	// Check if we're on a challenge page by inspecting the HTML
	html, err := page.HTML()
	if err != nil {
		return
	}
	lower := strings.ToLower(html)

	// PerimeterX "press & hold" challenge
	if strings.Contains(lower, "press & hold") || strings.Contains(lower, "press and hold") {
		// The challenge button is typically inside an iframe or a specific element.
		// Try common PerimeterX selectors.
		for _, sel := range []string{
			"#px-captcha",
			"[data-testid='press-hold-btn']",
			"#challenge-container button",
			".challenge-button",
		} {
			el, findErr := page.Timeout(2 * time.Second).Element(sel)
			if findErr != nil {
				continue
			}
			// Press and hold for 8 seconds (PerimeterX requires ~7s hold)
			_ = el.WaitVisible()
			mouse := page.Mouse
			shape, shapeErr := el.Shape()
			if shapeErr != nil || len(shape.Quads) == 0 {
				continue
			}
			box := shape.Quads[0]
			centerX := (box[0] + box[2] + box[4] + box[6]) / 4
			centerY := (box[1] + box[3] + box[5] + box[7]) / 4
			_ = mouse.MoveTo(proto.NewPoint(centerX, centerY))
			_ = mouse.Down(proto.InputMouseButtonLeft, 1)
			time.Sleep(8 * time.Second)
			_ = mouse.Up(proto.InputMouseButtonLeft, 1)
			// Wait for redirect after successful challenge
			_ = page.WaitStable(3 * time.Second)
			return
		}
	}

	// Cloudflare Turnstile / checkbox challenge
	if strings.Contains(lower, "cf-turnstile") || strings.Contains(lower, "cf-browser-verification") {
		for _, sel := range []string{
			"input[type='checkbox']",
			".cf-turnstile iframe",
			"#challenge-form input",
		} {
			el, findErr := page.Timeout(2 * time.Second).Element(sel)
			if findErr != nil {
				continue
			}
			_ = el.Click(proto.InputMouseButtonLeft, 1)
			_ = page.WaitStable(3 * time.Second)
			return
		}
	}
}

// domainFromTargetURL extracts the hostname (without port) from a URL.
func domainFromTargetURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := parsed.Hostname()
	// Strip www. for domain matching
	return strings.TrimPrefix(host, "www.")
}
