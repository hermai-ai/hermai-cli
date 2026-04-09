package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

// proxyCredentials holds parsed proxy authentication details.
type proxyCredentials struct {
	Host     string // host:port for --proxy-server
	Username string
	Password string
}

// parseProxy extracts host and optional credentials from a proxy URL.
// Supports formats: "host:port", "http://host:port", "http://user:pass@host:port".
func parseProxy(rawProxy string) proxyCredentials {
	if rawProxy == "" {
		return proxyCredentials{}
	}

	// Add scheme if missing so url.Parse works
	if !strings.Contains(rawProxy, "://") {
		rawProxy = "http://" + rawProxy
	}

	parsed, err := url.Parse(rawProxy)
	if err != nil {
		return proxyCredentials{Host: rawProxy}
	}

	creds := proxyCredentials{Host: parsed.Host}
	if parsed.User != nil {
		creds.Username = parsed.User.Username()
		creds.Password, _ = parsed.User.Password()
	}
	return creds
}

const (
	defaultTimeout       = 60 * time.Second
	defaultWaitAfterLoad = 1500 * time.Millisecond

	// Default Lightpanda CDP endpoint (WSL2 or local)
	defaultLightpandaURL = "ws://127.0.0.1:9222/"

	// minRenderedBodyLen is the minimum body text length to consider a page
	// fully rendered. Below this threshold when using Lightpanda, we assume
	// the SPA failed to render and fallback to Chromium.
	minRenderedBodyLen = 500
)

// ErrAuthWall is returned when a page requires authentication.
var ErrAuthWall = fmt.Errorf("hermai: page requires authentication")

// RodBrowser implements the Service interface using CDP protocol.
// Supports two backends:
//   - Lightpanda (preferred): 9x less memory, 11x faster, instant startup
//   - Chromium via Rod launcher (fallback): full browser compatibility
//
// When Lightpanda returns thin/empty HTML (SPA not rendered), the browser
// automatically falls back to Chromium for a second capture attempt.
// Domains that required Chromium fallback are recorded in ~/.hermai/spa_domains.txt
// so subsequent visits skip Lightpanda entirely (avoiding double-latency).
type RodBrowser struct {
	browser     *rod.Browser
	launcher    *launcher.Launcher // nil when using external CDP (Lightpanda)
	backend     string             // "lightpanda" or "chromium"
	browserPath string             // stored for deferred Chromium fallback
	spaDomains  *spaDomainCache    // auto-learned domains that need Chromium
	proxyCreds  proxyCredentials   // proxy auth for CDP Fetch.AuthRequired
}

// NewRodBrowser creates a browser instance.
// Connection priority:
//  1. If cdpURL is set, connect to that CDP endpoint directly (Lightpanda or remote browser)
//  2. If Lightpanda is running on default port 9222, connect to it
//  3. Fall back to launching Chromium via Rod
func NewRodBrowser(browserPath string) (*RodBrowser, error) {
	// Try Lightpanda first (check if CDP server is running on default port)
	if b, err := connectCDP(defaultLightpandaURL); err == nil {
		b.browserPath = browserPath // store for Chromium fallback
		return b, nil
	}

	// Fall back to Chromium via Rod launcher (no proxy at launch time;
	// per-capture proxy is applied when Capture spawns a new instance).
	return launchChromium(browserPath, "")
}

// NewRodBrowserWithCDP connects to an explicit CDP WebSocket URL.
// Use this for Lightpanda, remote browsers, or Browserless.io.
// browserPath is stored for Chromium fallback when the CDP backend
// returns thin HTML (SPA not rendered).
func NewRodBrowserWithCDP(cdpURL, browserPath string) (*RodBrowser, error) {
	b, err := connectCDP(cdpURL)
	if err != nil {
		return nil, err
	}
	b.browserPath = browserPath
	return b, nil
}

// connectCDP tries to connect to a CDP server (Lightpanda, Scraping Browser, etc.).
func connectCDP(cdpURL string) (*RodBrowser, error) {
	isRemote := strings.Contains(cdpURL, "brd.superproxy.io") || strings.Contains(cdpURL, "browserless")
	backend := "lightpanda"
	if isRemote {
		backend = "scraping_browser"
	}

	// Skip health check for remote scraping browsers — they spin up
	// on-demand when the WebSocket connects and don't serve /json/version.
	if !isRemote {
		httpURL := strings.Replace(cdpURL, "ws://", "http://", 1)
		httpURL = strings.Replace(httpURL, "wss://", "https://", 1)
		httpURL = strings.TrimSuffix(httpURL, "/") + "/json/version"

		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(httpURL)
		if err != nil || resp.StatusCode != 200 {
			return nil, fmt.Errorf("CDP not available at %s", cdpURL)
		}
		resp.Body.Close()
	}

	browser := rod.New().ControlURL(cdpURL)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("hermai: failed to connect to CDP at %s: %w", cdpURL, err)
	}

	return &RodBrowser{
		browser: browser,
		backend: backend,
	}, nil
}

// launchChromium starts a local Chromium instance via Rod's launcher.
// If proxyURL is non-empty, Chrome is launched with --proxy-server so all
// traffic routes through the proxy (e.g. residential proxy for anti-bot bypass).
// Proxy auth credentials are stored and handled via CDP Fetch.AuthRequired
// during capture.
func launchChromium(browserPath, proxyURL string) (*RodBrowser, error) {
	proxy := parseProxy(proxyURL)

	l := launcher.New().Headless(true).Leakless(false)
	if browserPath != "" {
		l = l.Bin(browserPath)
	}
	if proxy.Host != "" {
		l = l.Proxy(proxy.Host)
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("hermai: failed to launch browser: %w", err)
	}

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		l.Kill()
		return nil, fmt.Errorf("hermai: failed to connect to browser: %w", err)
	}

	return &RodBrowser{
		browser:    browser,
		launcher:   l,
		backend:    "chromium",
		proxyCreds: proxy,
	}, nil
}

// Backend returns which browser engine is in use ("lightpanda" or "chromium").
func (rb *RodBrowser) Backend() string {
	return rb.backend
}

// SetSPADomainsFile enables auto-learning of SPA domains that require Chromium.
// The file records domains where Lightpanda returned thin HTML. On subsequent
// visits to these domains, Lightpanda is skipped entirely (saves 5-10s).
func (rb *RodBrowser) SetSPADomainsFile(path string) {
	rb.spaDomains = newSPADomainCache(path)
}

// Capture navigates to the target URL, captures network traffic as HAR, and
// extracts a simplified DOM snapshot. Uses CDP protocol events for passive
// network observation. Works with both Lightpanda and Chromium backends.
//
// When Lightpanda returns thin rendered HTML (SPA not rendered), Capture
// automatically retries with a temporary Chromium instance for full
// JavaScript execution. This ensures SPAs (React, Next.js, Vue) still work.
func (rb *RodBrowser) Capture(ctx context.Context, targetURL string, opts CaptureOpts) (*CaptureResult, error) {
	domain := domainFromURL(targetURL)

	// Fast path: if this domain previously needed Chromium, skip Lightpanda
	// entirely. Saves 5-10s of wasted Lightpanda time on known SPA sites.
	if rb.backend == "lightpanda" && rb.spaDomains.contains(domain) {
		chromium, err := launchChromium(rb.browserPath, opts.ProxyURL)
		if err == nil {
			defer chromium.Close()
			return chromium.captureOnce(ctx, targetURL, opts)
		}
		// Chromium unavailable — fall through to Lightpanda
	}

	// If the existing browser was launched without a proxy but this capture
	// needs one, spawn a temporary Chromium instance with the proxy.
	if rb.backend == "chromium" && opts.ProxyURL != "" && rb.proxyCreds.Host == "" {
		chromium, err := launchChromium(rb.browserPath, opts.ProxyURL)
		if err == nil {
			defer chromium.Close()
			return chromium.captureOnce(ctx, targetURL, opts)
		}
		// Proxy Chromium unavailable — fall through to existing browser
	}

	result, err := rb.captureOnce(ctx, targetURL, opts)
	if err != nil {
		return nil, err
	}

	// Lightpanda fallback: if rendered HTML is thin, the SPA likely didn't
	// execute. Retry with real Chromium for full JS rendering.
	if rb.backend == "lightpanda" && isRenderedHTMLThin(result.RenderedHTML) {
		chromium, chromErr := launchChromium(rb.browserPath, opts.ProxyURL)
		if chromErr != nil {
			return result, nil // Chromium unavailable — return Lightpanda result
		}
		defer chromium.Close()

		chromResult, chromCaptureErr := chromium.captureOnce(ctx, targetURL, opts)
		if chromCaptureErr != nil {
			return result, nil // Chromium capture failed — return Lightpanda result
		}

		// Chromium succeeded where Lightpanda failed — remember this domain
		rb.spaDomains.record(domain)

		return chromResult, nil
	}

	return result, nil
}

// captureOnce performs a single browser capture without fallback logic.
func (rb *RodBrowser) captureOnce(ctx context.Context, targetURL string, opts CaptureOpts) (*CaptureResult, error) {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	waitAfterLoad := opts.WaitAfterLoad
	if waitAfterLoad == 0 {
		waitAfterLoad = defaultWaitAfterLoad
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Use stealth mode for Chromium to bypass bot detection
	// (navigator.webdriver, headless fingerprint, etc.)
	// Lightpanda doesn't support stealth JS, use plain page.
	var page *rod.Page
	var pageErr error
	if rb.backend == "chromium" {
		page, pageErr = stealth.Page(rb.browser)
	} else {
		page, pageErr = rb.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	}
	if pageErr != nil {
		return nil, fmt.Errorf("hermai: failed to create page: %w", pageErr)
	}
	defer page.Close()

	page = page.Context(ctx)

	// If the browser was launched with an authenticated proxy, enable
	// CDP Fetch domain to intercept 407 Proxy-Auth challenges and
	// supply credentials automatically.
	if rb.proxyCreds.Username != "" {
		fetchEnable := proto.FetchEnable{HandleAuthRequests: true}
		if err := fetchEnable.Call(page); err != nil {
			return nil, fmt.Errorf("hermai: failed to enable fetch for proxy auth: %w", err)
		}
		username := rb.proxyCreds.Username
		password := rb.proxyCreds.Password
		go page.EachEvent(func(e *proto.FetchAuthRequired) {
			_ = proto.FetchContinueWithAuth{
				RequestID: e.RequestID,
				AuthChallengeResponse: &proto.FetchAuthChallengeResponse{
					Response: proto.FetchAuthChallengeResponseResponseProvideCredentials,
					Username: username,
					Password: password,
				},
			}.Call(page)
		})()
	}

	// Inject user-provided cookies before navigation so the initial page
	// load (and any XHR/fetch calls it triggers) carry them. This enables
	// authenticated capture — e.g. LinkedIn Voyager API discovery.
	if len(opts.Cookies) > 0 {
		domain := domainFromURL(targetURL)
		var cookieParams []*proto.NetworkCookieParam
		for _, raw := range opts.Cookies {
			name, value, ok := strings.Cut(raw, "=")
			if !ok || name == "" {
				continue
			}
			cookieParams = append(cookieParams, &proto.NetworkCookieParam{
				Name:   name,
				Value:  value,
				Domain: "." + domain,
				Path:   "/",
			})
		}
		if len(cookieParams) > 0 {
			if err := page.SetCookies(cookieParams); err != nil {
				return nil, fmt.Errorf("hermai: failed to set cookies: %w", err)
			}
		}
	}

	networkEnable := proto.NetworkEnable{}
	if err := networkEnable.Call(page); err != nil {
		return nil, fmt.Errorf("hermai: failed to enable network monitoring: %w", err)
	}

	var mu sync.Mutex
	requests := make(map[proto.NetworkRequestID]*HARRequest)
	responses := make(map[proto.NetworkRequestID]*HARResponse)

	// EachEvent returns a wait function that IS the event loop.
	// Must call it (in a goroutine) for events to actually fire.
	wait := page.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) {
			mu.Lock()
			defer mu.Unlock()

			headers := make(map[string]string)
			for k, v := range e.Request.Headers {
				headers[k] = v.Str()
			}
			requests[e.RequestID] = &HARRequest{
				Method:  e.Request.Method,
				URL:     e.Request.URL,
				Headers: headers,
				Body:    e.Request.PostData,
			}
		},
		func(e *proto.NetworkResponseReceived) {
			mu.Lock()
			defer mu.Unlock()

			headers := make(map[string]string)
			for k, v := range e.Response.Headers {
				headers[k] = v.Str()
			}
			responses[e.RequestID] = &HARResponse{
				Status:      e.Response.Status,
				ContentType: e.Response.MIMEType,
				Headers:     headers,
			}
		},
	)
	go wait()

	if err := page.Navigate(targetURL); err != nil {
		return nil, fmt.Errorf("hermai: target website unreachable (%s): %w", targetURL, err)
	}

	// Non-fatal — some pages never fully stabilize (e.g. LinkedIn chat widgets,
	// real-time feeds). Cap WaitStable to half the remaining timeout so we always
	// have time left for DOM/HTML extraction.
	waitDeadline := time.Now().Add(timeout / 2)
	waitCtx, waitCancel := context.WithDeadline(ctx, waitDeadline)
	_ = page.Context(waitCtx).WaitStable(waitAfterLoad)
	waitCancel()
	page = page.Context(ctx) // restore full timeout for HTML extraction

	domSnapshot, err := extractDOMSnapshot(page)
	if err != nil {
		domSnapshot = ""
	}

	renderedHTML, err := page.HTML()
	if err != nil {
		renderedHTML = ""
	}

	mu.Lock()
	for reqID, resp := range responses {
		if !isJSONContentType(resp.ContentType) {
			continue
		}
		body, bodyErr := proto.NetworkGetResponseBody{RequestID: reqID}.Call(page)
		if bodyErr != nil {
			continue
		}
		resp.Body = body.Body
	}

	entries := buildHAREntries(requests, responses)
	mu.Unlock()

	// Get final URL for auth detection
	var finalURL string
	info, infoErr := page.Info()
	if infoErr == nil {
		finalURL = info.URL
	} else {
		finalURL = targetURL
	}

	navigationStatus := 0
	for _, entry := range entries {
		if entry.Request.URL == finalURL {
			navigationStatus = entry.Response.Status
			break
		}
	}

	// Skip auth detection when user explicitly provided cookies — they are
	// intentionally authenticating, and the page may still contain "Sign In"
	// links that would trigger a false positive.
	if len(opts.Cookies) == 0 && DetectAuth(AuthSignals{
		HTTPStatus:  navigationStatus,
		FinalURL:    finalURL,
		DOMSnapshot: domSnapshot,
	}) {
		return nil, fmt.Errorf("%w: %s", ErrAuthWall, targetURL)
	}

	return &CaptureResult{
		HAR:          &HARLog{Entries: entries},
		DOMSnapshot:  domSnapshot,
		RenderedHTML: renderedHTML,
	}, nil
}

// isRenderedHTMLThin returns true if the rendered HTML has too little body
// text content. This indicates the SPA framework didn't execute (e.g.,
// React/Vue app returning just <div id="root"></div>).
func isRenderedHTMLThin(html string) bool {
	if html == "" {
		return true
	}

	// Extract body content
	lower := strings.ToLower(html)
	bodyStart := strings.Index(lower, "<body")
	if bodyStart == -1 {
		return len(html) < minRenderedBodyLen
	}

	// Find end of opening body tag
	bodyTagEnd := strings.Index(html[bodyStart:], ">")
	if bodyTagEnd == -1 {
		return true
	}

	bodyContent := html[bodyStart+bodyTagEnd+1:]

	// Find closing body tag
	bodyEnd := strings.Index(strings.ToLower(bodyContent), "</body")
	if bodyEnd != -1 {
		bodyContent = bodyContent[:bodyEnd]
	}

	// Strip HTML tags to get text content
	text := stripHTMLTags(bodyContent)
	return len(strings.TrimSpace(text)) < minRenderedBodyLen
}

// stripHTMLTags removes all HTML tags from a string, returning only text content.
func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// Close shuts down the browser and launcher (if any).
func (rb *RodBrowser) Close() error {
	if rb.browser != nil {
		if err := rb.browser.Close(); err != nil {
			return fmt.Errorf("hermai: failed to close browser: %w", err)
		}
	}
	if rb.launcher != nil {
		rb.launcher.Kill()
	}
	return nil
}

// buildHAREntries pairs captured requests with their responses.
func buildHAREntries(
	requests map[proto.NetworkRequestID]*HARRequest,
	responses map[proto.NetworkRequestID]*HARResponse,
) []HAREntry {
	var entries []HAREntry

	for reqID, req := range requests {
		resp, ok := responses[reqID]
		if !ok {
			resp = &HARResponse{}
		}
		entries = append(entries, HAREntry{
			Request:  *req,
			Response: *resp,
		})
	}

	if entries == nil {
		return []HAREntry{}
	}

	return entries
}

// isJSONContentType returns true if the content type indicates JSON or GraphQL.
func isJSONContentType(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "json") || strings.Contains(ct, "graphql")
}

// extractDOMSnapshot evaluates JavaScript to extract simplified DOM text.
func extractDOMSnapshot(page *rod.Page) (string, error) {
	result, err := page.Eval(`() => {
		function simplify(el, depth) {
			if (depth > 5 || !el || !el.tagName) return '';
			const tag = el.tagName.toLowerCase();
			if (['script','style','noscript','svg','iframe'].includes(tag)) return '';
			let text = '';
			for (const child of el.childNodes) {
				if (child.nodeType === 3) {
					const t = child.textContent.trim();
					if (t) text += t + ' ';
				} else if (child.nodeType === 1) {
					text += simplify(child, depth + 1);
				}
			}
			if (!text.trim()) return '';
			return '<' + tag + '>' + text.trim() + '</' + tag + '>\n';
		}
		return simplify(document.body, 0);
	}`)
	if err != nil {
		return "", fmt.Errorf("hermai: failed to extract DOM snapshot: %w", err)
	}

	return result.Value.String(), nil
}
