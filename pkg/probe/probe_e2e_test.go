package probe_test

import (
	"context"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/probe"
)

// E2E tests hit real websites. Skip with -short.
// Run: go test ./pkg/probe/ -run TestE2E -v -count=1

func probeURL(t *testing.T, url string, stealth bool) *probe.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := probe.Probe(ctx, url, probe.Options{
		Timeout: 10 * time.Second,
		Stealth: stealth,
	})
	if err != nil {
		t.Fatalf("probe %s: %v", url, err)
	}
	return result
}

// ── known_site strategy ────────────────────────────────────────────

func TestE2E_Probe_YouTube(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}
	result := probeURL(t, "https://www.youtube.com/watch?v=dQw4w9WgXcQ", false)

	if result.Strategy == "" {
		t.Fatal("no strategy found")
	}
	t.Logf("strategy: %s", result.Strategy)
	t.Logf("candidates: %d", len(result.Candidates))

	if len(result.Candidates) == 0 {
		t.Fatal("no candidates")
	}

	found := false
	for _, c := range result.Candidates {
		t.Logf("  %s (score=%d)", c.Strategy, c.Score)
		if c.Schema != nil {
			t.Logf("    endpoints: %d, url_template: %s", len(c.Schema.Endpoints), c.Schema.Endpoints[0].URLTemplate)
		}
		if c.Strategy == "known_site:youtube_video" {
			found = true
		}
	}
	if !found {
		t.Error("expected known_site:youtube_video candidate")
	}
}

func TestE2E_Probe_GitHub(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}
	result := probeURL(t, "https://github.com/golang/go", false)

	if result.Strategy == "" {
		t.Fatal("no strategy found")
	}
	t.Logf("strategy: %s", result.Strategy)
	t.Logf("candidates: %d", len(result.Candidates))

	for _, c := range result.Candidates {
		t.Logf("  %s (score=%d)", c.Strategy, c.Score)
		if c.Schema != nil && len(c.Schema.Endpoints) > 0 {
			t.Logf("    url_template: %s", c.Schema.Endpoints[0].URLTemplate)
		}
	}

	found := false
	for _, c := range result.Candidates {
		if c.Strategy == "known_site:github_repo" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected known_site:github_repo candidate")
	}
}

func TestE2E_Probe_HackerNews(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}
	result := probeURL(t, "https://news.ycombinator.com/item?id=1", false)

	if len(result.Candidates) == 0 {
		t.Fatal("no candidates")
	}
	t.Logf("strategy: %s", result.Strategy)

	found := false
	for _, c := range result.Candidates {
		t.Logf("  %s (score=%d)", c.Strategy, c.Score)
		if c.Strategy == "known_site:hn_item" {
			found = true
		}
	}
	if !found {
		t.Error("expected known_site:hn_item candidate")
	}
}

func TestE2E_Probe_Wikipedia(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}
	result := probeURL(t, "https://en.wikipedia.org/wiki/Go_(programming_language)", false)

	if len(result.Candidates) == 0 {
		t.Fatal("no candidates")
	}
	t.Logf("strategy: %s", result.Strategy)

	found := false
	for _, c := range result.Candidates {
		t.Logf("  %s (score=%d)", c.Strategy, c.Score)
		if c.Strategy == "known_site:wikipedia_page" {
			found = true
		}
	}
	if !found {
		t.Error("expected known_site:wikipedia_page candidate")
	}
}

func TestE2E_Probe_NPM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}
	result := probeURL(t, "https://www.npmjs.com/package/express", false)

	if len(result.Candidates) == 0 {
		t.Fatal("no candidates")
	}
	t.Logf("strategy: %s", result.Strategy)

	for _, c := range result.Candidates {
		t.Logf("  %s (score=%d)", c.Strategy, c.Score)
	}
}

func TestE2E_Probe_PyPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}
	result := probeURL(t, "https://pypi.org/project/requests/", false)

	if len(result.Candidates) == 0 {
		t.Fatal("no candidates")
	}
	t.Logf("strategy: %s", result.Strategy)

	found := false
	for _, c := range result.Candidates {
		t.Logf("  %s (score=%d)", c.Strategy, c.Score)
		if c.Strategy == "known_site:pypi_package" {
			found = true
		}
	}
	if !found {
		t.Error("expected known_site:pypi_package candidate")
	}
}

func TestE2E_Probe_YahooFinance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}
	result := probeURL(t, "https://finance.yahoo.com/quote/AAPL/", false)

	t.Logf("strategy: %s", result.Strategy)
	t.Logf("candidates: %d", len(result.Candidates))
	for _, c := range result.Candidates {
		t.Logf("  %s (score=%d)", c.Strategy, c.Score)
	}

	// Yahoo Finance has a known_site pattern but also may hit anti-bot
	if result.HTMLBody != "" {
		t.Logf("got HTML fallback (%d bytes)", len(result.HTMLBody))
	}
}

// ── json_suffix strategy ───────────────────────────────────────────

func TestE2E_Probe_Reddit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}
	result := probeURL(t, "https://www.reddit.com/r/golang/", true)

	t.Logf("strategy: %s", result.Strategy)
	t.Logf("candidates: %d", len(result.Candidates))
	t.Logf("stealth_required: %v", result.RequiresStealth)

	for _, c := range result.Candidates {
		t.Logf("  %s (score=%d)", c.Strategy, c.Score)
		if c.Schema != nil && len(c.Schema.Endpoints) > 0 {
			t.Logf("    url_template: %s", c.Schema.Endpoints[0].URLTemplate)
		}
	}

	if result.HTMLBody != "" {
		t.Logf("HTML fallback: %d bytes", len(result.HTMLBody))
	}

	// Reddit should be discoverable via .json suffix
	foundJSON := false
	for _, c := range result.Candidates {
		if c.Strategy == "json_suffix" {
			foundJSON = true
			break
		}
	}
	if !foundJSON && result.HTMLBody == "" && len(result.Candidates) == 0 {
		t.Error("expected json_suffix candidate or HTML fallback for Reddit")
	}
}

// ── wp_json strategy ───────────────────────────────────────────────

func TestE2E_Probe_WordPress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}
	// TechCrunch runs on WordPress
	result := probeURL(t, "https://techcrunch.com/", false)

	t.Logf("strategy: %s", result.Strategy)
	t.Logf("candidates: %d", len(result.Candidates))

	for _, c := range result.Candidates {
		t.Logf("  %s (score=%d)", c.Strategy, c.Score)
		if c.Schema != nil && len(c.Schema.Endpoints) > 0 {
			t.Logf("    url_template: %s", c.Schema.Endpoints[0].URLTemplate)
		}
	}

	if result.HTMLBody != "" {
		t.Logf("HTML fallback: %d bytes", len(result.HTMLBody))
	}

	foundWP := false
	for _, c := range result.Candidates {
		if c.Strategy == "wp_json" {
			foundWP = true
			break
		}
	}
	if foundWP {
		t.Log("WordPress REST API discovered")
	}
}

// ── stealth escalation ─────────────────────────────────────────────

func TestE2E_Probe_StealthEscalation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}

	// Amazon often blocks plain requests — probe should escalate to stealth
	result := probeURL(t, "https://www.amazon.com/dp/B0CX23V2ZK", false)

	t.Logf("strategy: %s", result.Strategy)
	t.Logf("stealth_required: %v", result.RequiresStealth)
	t.Logf("candidates: %d", len(result.Candidates))

	if result.HTMLBody != "" {
		t.Logf("got HTML (%d bytes)", len(result.HTMLBody))
	} else if len(result.Candidates) == 0 {
		t.Error("expected either HTML body or candidates from Amazon")
	}

	for _, c := range result.Candidates {
		t.Logf("  %s (score=%d)", c.Strategy, c.Score)
	}
}

// ── HTML fallback sites ────────────────────────────────────────────

func TestE2E_Probe_LinkedIn_HTMLFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}

	// LinkedIn has no JSON API for profiles — should fall back to HTML
	result := probeURL(t, "https://www.linkedin.com/in/williamhgates", true)

	t.Logf("strategy: %s", result.Strategy)
	t.Logf("candidates: %d", len(result.Candidates))

	if result.HTMLBody == "" && len(result.Candidates) == 0 {
		t.Error("expected HTML fallback for LinkedIn")
	}
	if result.HTMLBody != "" {
		t.Logf("HTML body: %d bytes", len(result.HTMLBody))
	}
}

// ── full pipeline: probe → extract ─────────────────────────────────

func TestE2E_Probe_FullDiscoveryPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}

	// YouTube: probe finds oEmbed API + HTML body has ytInitialData
	result := probeURL(t, "https://www.youtube.com/watch?v=jNQXAC9IVRw", false)

	t.Logf("strategy: %s", result.Strategy)

	if len(result.Candidates) == 0 {
		t.Fatal("no candidates from probe")
	}

	// Check that the best candidate has a usable schema
	best := result.Candidates[0]
	t.Logf("best candidate: %s (score=%d)", best.Strategy, best.Score)
	if best.Schema == nil {
		t.Fatal("best candidate has no schema")
	}
	if len(best.Schema.Endpoints) == 0 {
		t.Fatal("best candidate schema has no endpoints")
	}

	ep := best.Schema.Endpoints[0]
	t.Logf("endpoint: %s %s", ep.Method, ep.URLTemplate)
	t.Logf("variables: %d", len(ep.Variables))
	for _, v := range ep.Variables {
		t.Logf("  %s (source=%s)", v.Name, v.Source)
	}

	if ep.Method != "GET" {
		t.Errorf("expected GET, got %s", ep.Method)
	}
	if ep.URLTemplate == "" {
		t.Error("empty url_template")
	}
}
