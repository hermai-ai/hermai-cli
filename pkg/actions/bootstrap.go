package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
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
	// PersistentProfileDir is the Chrome user-data-dir to reuse across
	// bootstraps. Empty defaults to ~/.hermai/chrome-profile. Reusing the
	// same dir makes the browser look like a returning user to anti-bot
	// sensors (accumulated TLS tickets, history, IndexedDB). Tests pass a
	// temp dir to isolate state.
	PersistentProfileDir string
}

// BootstrapResult summarizes a successful bootstrap.
type BootstrapResult struct {
	Site              string
	CookieCount       int
	RequiredFound     []string // which required_cookies were actually set
	RequiredMiss      []string // required_cookies that never appeared
	AkamaiUnvalidated bool     // _abck was present but never reached ~-1~ validated state
	StoragePath       string   // absolute path to the saved cookies.json
	Duration          time.Duration
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

// ErrBootstrapAkamaiUnvalidated is returned when Akamai's _abck cookie was
// set but its value never reached the validated state (`~-1~` marker). This
// happens when the bootstrap runs headless, or headful but the user never
// moved the mouse / clicked during the wait window — Akamai's sensor keeps
// the cookie in "still collecting telemetry" state forever. Remedy: rerun
// with Headless=false and interact with the window during the wait.
var ErrBootstrapAkamaiUnvalidated = errors.New("bootstrap: _abck cookie captured but still in unvalidated state; re-run with --headful and move the mouse / click during the wait window")

// akamaiValidatedPattern matches the validated form of Akamai's _abck cookie
// value. The value has the shape "<version>~<sensor-score>~<hit-count>~...".
// Hit count `-1` means the sensor has accepted the client as human; `0` or
// other positive values mean telemetry is still being collected. Per-sensor
// docs + live inspection on united.com and every other Akamai-protected
// site confirm: without `~-1~` the cookie replays as a stale sensor and
// the downstream API returns 403 / challenge HTML.
var akamaiValidatedPattern = regexp.MustCompile(`~-1~`)

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

	// Pivot from rod's bundled Chromium + ephemeral temp profile to the
	// user's real Chrome binary + a persistent user-data-dir. Rationale:
	//
	// Anti-bot sensors (Akamai Bot Manager Premier, DataDome HUMAN,
	// PerimeterX HUMAN) fingerprint the browser process they see: Chrome
	// version, TLS session tickets, storage state, browsing history depth,
	// and a handful of canvas / WebGL signals. Launching ephemeral
	// `HeadlessChrome` with zero history fails the "returning user" check
	// on the very first frame — which is why CDP-level mouse humanization
	// can't recover the session. By contrast, spawning the user's own
	// installed Chrome with a persistent profile means that after a few
	// bootstraps the profile has real TLS resumption data, indexeddb
	// content, and navigation history that sensors treat as normal.
	//
	// This matches bb-browser's approach (packages/cli/src/cdp-discovery.ts
	// `launchManagedBrowser`): find the system Chrome, point at a persistent
	// dir under the tool's home, and attach. The only difference is we still
	// terminate the browser at end of bootstrap — keeping it alive between
	// invocations is a future optimization.
	persistentDir := req.PersistentProfileDir
	if persistentDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			persistentDir = filepath.Join(home, ".hermai", "chrome-profile")
		}
	}
	if persistentDir != "" {
		if err := os.MkdirAll(persistentDir, 0700); err != nil {
			return nil, fmt.Errorf("bootstrap: prepare persistent profile dir %s: %w", persistentDir, err)
		}
	}

	chromeBin := req.BrowserPath
	if chromeBin == "" {
		if env := strings.TrimSpace(os.Getenv("HERMAI_BROWSER")); env != "" {
			chromeBin = env
		}
	}
	if chromeBin == "" {
		chromeBin = findSystemChromiumBinary()
	}
	if chromeBin == "" {
		return nil, ErrNoChromiumBrowser
	}

	l := launcher.New().Headless(req.Headless).Leakless(false).
		Set("disable-blink-features", "AutomationControlled").
		// Suppress the "Chrome is being controlled by automated test software"
		// infobar so the window looks like a normal Chrome startup to both
		// anti-bot scripts and any curious user watching the bootstrap.
		Set("disable-infobars").
		Set("no-first-run").
		Set("no-default-browser-check")
	if chromeBin != "" {
		l = l.Bin(chromeBin)
	}
	if persistentDir != "" {
		l = l.UserDataDir(persistentDir)
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

	// Drive mouse jitter, scroll, and click sequences in a goroutine so anti-bot
	// sensors (Akamai _abck, DataDome, PerimeterX) receive the behavioral
	// telemetry they require to validate the session. Without this the sensor
	// score stays in "still collecting" state forever and the cookie the caller
	// replays downstream returns 403. Runs concurrently with the cookie poll
	// so the loop exits as soon as the required set is satisfied.
	humanizeCtx, cancelHumanize := context.WithCancel(navCtx)
	defer cancelHumanize()
	go humanizePage(humanizeCtx, page)

	// Poll for required_cookies with a short backoff. Most schemas set their
	// cookies in the first 1-5 seconds; harder ones (webmssdk, PerimeterX) can
	// take up to 20 seconds. Give up at req.Timeout. Also watch for Akamai's
	// `_abck` cookie to reach validated state — name-only checks pass while
	// the value is still an unvalidated sensor, which then fails downstream.
	found, missing, akamaiUnvalidated := waitForRequiredCookies(navCtx, page, req.RequiredCookies, req.Timeout)
	cancelHumanize()

	// Dump the full cookie jar regardless of whether all required cookies
	// appeared — even a partial set is useful for debugging, and downstream
	// tls-clients can replay what we got. Use a fresh short-lived context
	// for this read: navCtx may have expired during the required-cookie
	// poll, and the page.Context(navCtx) binding would make NetworkGetAllCookies
	// fail with "context deadline exceeded" and lose the partial jar.
	readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRead()
	allCookies, err := proto.NetworkGetAllCookies{}.Call(page.Context(readCtx))
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
		Site:              req.Site,
		CookieCount:       len(cookies),
		RequiredFound:     found,
		RequiredMiss:      missing,
		AkamaiUnvalidated: akamaiUnvalidated,
		StoragePath:       storagePath,
		Duration:          time.Since(start),
	}
	if len(missing) > 0 && len(req.RequiredCookies) > 0 {
		return res, fmt.Errorf("%w: missing %v (captured %d of %d required; try --headful + move the mouse / click during the wait window for Akamai/PerimeterX/Kasada sites)",
			ErrBootstrapTimeout, missing, len(found), len(req.RequiredCookies))
	}
	if akamaiUnvalidated {
		return res, ErrBootstrapAkamaiUnvalidated
	}
	return res, nil
}

// humanizePage runs until ctx is canceled, continuously simulating the mouse
// movement, scrolling, and click telemetry that anti-bot sensors (Akamai,
// DataDome, PerimeterX, Kasada) require before flipping their cookies into
// validated state. Headless Chrome by default produces zero mouse/scroll
// events after navigation, so these sensors keep the session unvalidated
// indefinitely — this routine closes that gap without asking the user to
// move their mouse. All errors are swallowed: a failed movement doesn't
// halt bootstrap, and some page states (detached frame, navigation in
// flight) make the CDP calls transiently fail.
//
// The movement pattern is deliberately crude: a small number of random
// points inside a standard 1280x800 viewport, with sleeps in the 150-450ms
// range. Sophisticated sensors also look at trajectory curvature and
// event-timing jitter, but for the common anti-bot scripts on our 65
// session-gated schemas the baseline telemetry is what we need.
func humanizePage(ctx context.Context, page *rod.Page) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	viewportW, viewportH := 1280.0, 800.0
	x, y := viewportW/2, viewportH/2

	for {
		if ctx.Err() != nil {
			return
		}
		action := r.Intn(10)
		switch {
		case action < 6:
			// Mouse move — small steps so the trajectory looks human, not
			// a straight teleport. Sensor scripts track the number of
			// mousemove events, not just the final position.
			targetX := clampFloat(x+float64(r.Intn(400)-200), 0, viewportW)
			targetY := clampFloat(y+float64(r.Intn(300)-150), 0, viewportH)
			steps := 4 + r.Intn(6)
			for i := 1; i <= steps; i++ {
				if ctx.Err() != nil {
					return
				}
				px := x + (targetX-x)*float64(i)/float64(steps)
				py := y + (targetY-y)*float64(i)/float64(steps)
				_ = page.Mouse.MoveTo(proto.NewPoint(px, py))
				sleepCtx(ctx, time.Duration(20+r.Intn(40))*time.Millisecond)
			}
			x, y = targetX, targetY
		case action < 9:
			// Scroll. ScrollWheel's (x, y) are the origin point; (dx, dy)
			// are the deltas. Keep dx=0 and dy in the 100-400px range so
			// we mimic a few wheel ticks rather than a continuous drag.
			dy := float64(80 + r.Intn(320))
			if r.Intn(4) == 0 {
				dy = -dy // occasional scroll up so the pattern isn't monotonic
			}
			_ = page.Mouse.Scroll(0, dy, 1)
			sleepCtx(ctx, time.Duration(150+r.Intn(300))*time.Millisecond)
		default:
			// Rare benign click on the current mouse position. Useful for
			// press-and-hold style sensors; harmless on a page's background.
			_ = page.Mouse.Down(proto.InputMouseButtonLeft, 1)
			sleepCtx(ctx, time.Duration(40+r.Intn(80))*time.Millisecond)
			_ = page.Mouse.Up(proto.InputMouseButtonLeft, 1)
			sleepCtx(ctx, time.Duration(100+r.Intn(200))*time.Millisecond)
		}
	}
}

// sleepCtx sleeps up to d but returns early if ctx is canceled.
func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// clampFloat confines v to [lo, hi]. Keeps simulated mouse coordinates
// inside the viewport so CDP doesn't reject the move.
func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Suppress unused-import lints on the input package. Rod uses it for
// keyboard dispatch which future-humanize extensions may need.
var _ = input.Tab

// findSystemChromiumBinary returns the binary path of a Chromium-based browser
// installed on this host, preferring the user's OS-default browser if it is
// Chromium-family (Brave, Edge, Arc, Chrome, Chromium, Opera, Vivaldi). Falls
// back to a per-OS ordered search if the default can't be read or isn't
// Chromium. Returns an empty string if no Chromium-family browser is found —
// caller must surface a user-facing error rather than downloading anything.
//
// Why not just Chrome: users run Brave, Edge, Arc, etc. daily. The TLS/JA3
// fingerprint + user-agent + version of whatever browser they use all day is
// what anti-bot sensors expect from this host; forcing a different binary
// actually makes detection WORSE. This matches bb-browser's philosophy:
// "your browser is the API."
//
// Why not Firefox/Safari: they don't speak CDP — rod and every other
// automation library in the ecosystem relies on Chrome DevTools Protocol.
func findSystemChromiumBinary() string {
	if def := defaultChromiumBinary(); def != "" {
		return def
	}
	for _, p := range chromiumFallbackCandidates() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// defaultChromiumBinary reads the OS-level default browser setting and, if the
// default is a Chromium-based browser we recognize, returns its binary path.
// Returns "" when the default is Safari/Firefox/unknown — caller falls back
// to the ordered candidate list.
func defaultChromiumBinary() string {
	switch runtime.GOOS {
	case "darwin":
		return defaultChromiumBinaryDarwin()
	case "linux":
		return defaultChromiumBinaryLinux()
	case "windows":
		return defaultChromiumBinaryWindows()
	}
	return ""
}

// defaultChromiumBinaryDarwin reads LaunchServices's https handler bundle ID
// from the secure preferences plist and maps it to an on-disk app binary.
// Keys match what the `defaults read` CLI reports, so behavior is consistent
// with how System Settings > Default Web Browser displays the user's choice.
func defaultChromiumBinaryDarwin() string {
	out, err := exec.Command("defaults", "read",
		filepath.Join(os.Getenv("HOME"), "Library", "Preferences", "com.apple.LaunchServices", "com.apple.launchservices.secure"),
		"LSHandlers").Output()
	if err != nil {
		return ""
	}
	// The plist is a list of blocks. Find the block whose URLScheme is https
	// and grab its RoleAll bundle ID. Using string scanning avoids a plist
	// parsing dependency for a stable, well-documented format.
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if !strings.Contains(line, "LSHandlerURLScheme = https") {
			continue
		}
		// Role is one of the other keys in the same block — look backwards
		// and forwards for "LSHandlerRoleAll = <bundle-id>".
		for j := i - 4; j <= i+4 && j < len(lines); j++ {
			if j < 0 {
				continue
			}
			if strings.Contains(lines[j], "LSHandlerRoleAll") {
				bundleID := strings.Trim(strings.TrimSpace(strings.SplitN(lines[j], "=", 2)[1]), "\";,")
				if path := darwinBundleIDToBinary(bundleID); path != "" {
					return path
				}
			}
		}
	}
	return ""
}

// darwinBundleIDToBinary maps a macOS bundle identifier to the canonical
// binary path inside /Applications. The map covers every Chromium-based
// browser the hermai team has seen installed in practice; new entries are
// cheap to add. Returns "" for Safari, Firefox, or unknown IDs.
func darwinBundleIDToBinary(bundleID string) string {
	m := map[string]string{
		"com.google.chrome":          "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"com.brave.browser":          "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"com.microsoft.edgemac":      "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"company.thebrowser.browser": "/Applications/Arc.app/Contents/MacOS/Arc",
		"com.operasoftware.opera":    "/Applications/Opera.app/Contents/MacOS/Opera",
		"com.vivaldi.vivaldi":        "/Applications/Vivaldi.app/Contents/MacOS/Vivaldi",
		"org.chromium.chromium":      "/Applications/Chromium.app/Contents/MacOS/Chromium",
	}
	if p, ok := m[bundleID]; ok {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// defaultChromiumBinaryLinux reads xdg-settings's default-web-browser, which
// returns a .desktop file name like "brave-browser.desktop". The map below
// covers the common distro names; anything else falls through to the ordered
// fallback search.
func defaultChromiumBinaryLinux() string {
	out, err := exec.Command("xdg-settings", "get", "default-web-browser").Output()
	if err != nil {
		return ""
	}
	desktop := strings.TrimSpace(string(out))
	m := map[string][]string{
		"brave-browser.desktop":  {"brave-browser", "brave"},
		"google-chrome.desktop":  {"google-chrome", "google-chrome-stable"},
		"microsoft-edge.desktop": {"microsoft-edge", "microsoft-edge-stable"},
		"chromium.desktop":       {"chromium", "chromium-browser"},
		"opera.desktop":          {"opera"},
		"vivaldi.desktop":        {"vivaldi", "vivaldi-stable"},
	}
	if bins, ok := m[desktop]; ok {
		for _, bin := range bins {
			if p, err := exec.LookPath(bin); err == nil {
				return p
			}
		}
	}
	return ""
}

// defaultChromiumBinaryWindows reads the UserChoice registry key for https,
// maps ProgId to a known Chromium-family install path. Registry queries are
// cheap and don't require elevated permissions.
func defaultChromiumBinaryWindows() string {
	out, err := exec.Command("reg", "query",
		`HKCU\SOFTWARE\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice`,
		"/v", "ProgId").Output()
	if err != nil {
		return ""
	}
	progID := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "ProgId") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				progID = fields[len(fields)-1]
			}
		}
	}
	m := map[string]string{
		"ChromeHTML":     `C:\Program Files\Google\Chrome\Application\chrome.exe`,
		"BraveHTML":      `C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
		"MSEdgeHTM":      `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		"OperaStable":    `C:\Program Files\Opera\launcher.exe`,
		"VivaldiHTML":    `C:\Users\%USERNAME%\AppData\Local\Vivaldi\Application\vivaldi.exe`,
		"ChromiumHTM.F7": `C:\Program Files\Chromium\Application\chrome.exe`,
	}
	if p, ok := m[progID]; ok {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// chromiumFallbackCandidates lists every binary path we know how to find for
// Chromium-family browsers on the current OS, in preference order: Brave,
// Chrome, Edge, Arc, Chromium, Opera, Vivaldi. Order matters only when a host
// has multiple installed; whichever is first wins.
func chromiumFallbackCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Arc.app/Contents/MacOS/Arc",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Opera.app/Contents/MacOS/Opera",
			"/Applications/Vivaldi.app/Contents/MacOS/Vivaldi",
			"/Applications/Google Chrome Beta.app/Contents/MacOS/Google Chrome Beta",
			"/Applications/Google Chrome Dev.app/Contents/MacOS/Google Chrome Dev",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
		}
	case "linux":
		out := make([]string, 0, 8)
		for _, bin := range []string{
			"brave-browser", "brave",
			"google-chrome", "google-chrome-stable",
			"microsoft-edge", "microsoft-edge-stable",
			"chromium", "chromium-browser",
			"opera",
			"vivaldi", "vivaldi-stable",
		} {
			if p, err := exec.LookPath(bin); err == nil {
				out = append(out, p)
			}
		}
		return out
	case "windows":
		home := os.Getenv("USERPROFILE")
		return []string{
			`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Opera\launcher.exe`,
			filepath.Join(home, `AppData\Local\Vivaldi\Application\vivaldi.exe`),
		}
	}
	return nil
}

// ErrNoChromiumBrowser is surfaced when bootstrap runs on a host that has
// no Chromium-based browser installed anywhere we can find it. Never
// triggers rod's bundled-Chromium download.
var ErrNoChromiumBrowser = errors.New("hermai requires a Chromium-based browser (Chrome, Brave, Edge, Arc, Opera, Vivaldi, or Chromium). Install one and retry, or set HERMAI_BROWSER to the binary path")

// waitForRequiredCookies polls the page until every name in required is set
// (and, if `_abck` is among them, its value reaches Akamai's validated
// `~-1~` marker) or the context deadline fires. Returns the two disjoint
// name sets plus whether `_abck` was captured but never validated.
func waitForRequiredCookies(ctx context.Context, page *rod.Page, required []string, timeout time.Duration) (found, missing []string, akamaiUnvalidated bool) {
	if len(required) == 0 {
		return nil, nil, false
	}
	needed := make(map[string]struct{}, len(required))
	wantAbck := false
	for _, n := range required {
		needed[n] = struct{}{}
		if n == "_abck" {
			wantAbck = true
		}
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
		have := make(map[string]string)
		for _, c := range all.Cookies {
			have[c.Name] = c.Value
		}
		foundAll := true
		for name := range needed {
			if _, ok := have[name]; !ok {
				foundAll = false
				break
			}
		}
		abckOK := !wantAbck || akamaiValidatedPattern.MatchString(have["_abck"])
		if foundAll && abckOK {
			for name := range needed {
				found = append(found, name)
			}
			return found, nil, false
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Deadline hit — report what we saw vs what was expected, and flag the
	// Akamai-unvalidated case separately so callers can print a distinct
	// remediation hint (re-run with --headful + human interaction).
	all, err := proto.NetworkGetAllCookies{}.Call(page)
	if err == nil {
		have := make(map[string]string)
		for _, c := range all.Cookies {
			have[c.Name] = c.Value
		}
		for name := range needed {
			if _, ok := have[name]; ok {
				found = append(found, name)
			} else {
				missing = append(missing, name)
			}
		}
		if wantAbck {
			if v, ok := have["_abck"]; ok && !akamaiValidatedPattern.MatchString(v) {
				akamaiUnvalidated = true
			}
		}
	} else {
		for name := range needed {
			missing = append(missing, name)
		}
	}
	return found, missing, akamaiUnvalidated
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
