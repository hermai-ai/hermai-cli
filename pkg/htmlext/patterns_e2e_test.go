package htmlext_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/htmlext"
	"github.com/hermai-ai/hermai-cli/pkg/probe"
)

// E2E tests hit real websites. Skip with -short.
// Run: go test ./pkg/htmlext/ -run TestE2E -v -count=1

func fetchPage(t *testing.T, url string, stealth bool) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := probe.NewClient(probe.Options{
		Timeout: 10 * time.Second,
		Stealth: stealth,
	})

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	probe.SetBrowserHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("fetch %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("fetch %s: HTTP %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

func TestE2E_YouTube_Watch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}

	rawHTML := fetchPage(t, "https://www.youtube.com/watch?v=dQw4w9WgXcQ", true)
	t.Logf("fetched %d bytes", len(rawHTML))

	page := htmlext.Extract(rawHTML, "https://www.youtube.com/watch?v=dQw4w9WgXcQ")

	if page.Title == "" {
		t.Error("empty page title")
	} else {
		t.Logf("title: %s", page.Title)
	}

	if len(page.EmbeddedScripts) == 0 {
		t.Fatal("no embedded scripts found")
	}

	ytData, ok := page.EmbeddedScripts["ytInitialData"]
	if !ok {
		t.Error("missing ytInitialData")
	} else if m, ok := ytData.(map[string]any); !ok {
		t.Errorf("ytInitialData is %T, not map", ytData)
	} else {
		t.Logf("ytInitialData has %d top-level keys", len(m))
		if _, ok := m["contents"]; !ok {
			t.Error("ytInitialData missing 'contents' key")
		}
	}

	playerData, ok := page.EmbeddedScripts["ytInitialPlayerResponse"]
	if !ok {
		t.Error("missing ytInitialPlayerResponse")
	} else if m, ok := playerData.(map[string]any); !ok {
		t.Errorf("ytInitialPlayerResponse is %T, not map", playerData)
	} else {
		t.Logf("ytInitialPlayerResponse has %d top-level keys", len(m))
		if _, ok := m["videoDetails"]; !ok {
			t.Error("ytInitialPlayerResponse missing 'videoDetails' key")
		}
	}

	t.Logf("patterns found: %d", len(page.EmbeddedScripts))
	for name := range page.EmbeddedScripts {
		t.Logf("  - %s", name)
	}
}

func TestE2E_YouTube_Search(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}

	rawHTML := fetchPage(t, "https://www.youtube.com/results?search_query=golang+tutorial", true)
	t.Logf("fetched %d bytes", len(rawHTML))

	page := htmlext.Extract(rawHTML, "https://www.youtube.com/results?search_query=golang+tutorial")
	if len(page.EmbeddedScripts) == 0 {
		t.Fatal("no embedded scripts found")
	}

	ytData, ok := page.EmbeddedScripts["ytInitialData"]
	if !ok {
		t.Fatal("missing ytInitialData on search page")
	}

	m := ytData.(map[string]any)
	t.Logf("ytInitialData has %d top-level keys", len(m))

	if _, ok := m["contents"]; !ok {
		t.Error("ytInitialData missing 'contents'")
	}
}

func TestE2E_LinkedIn_Profile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}

	rawHTML := fetchPage(t, "https://www.linkedin.com/in/williamhgates", true)
	t.Logf("fetched %d bytes", len(rawHTML))

	page := htmlext.Extract(rawHTML, "https://www.linkedin.com/in/williamhgates")
	if page.Title == "" {
		t.Error("empty page title")
	} else {
		t.Logf("title: %s", page.Title)
	}

	if len(page.JSONLD) == 0 {
		t.Log("no JSON-LD found (may require login)")
	} else {
		t.Logf("JSON-LD blocks: %d", len(page.JSONLD))
	}

	t.Logf("embedded patterns found: %d", len(page.EmbeddedScripts))
	for name := range page.EmbeddedScripts {
		t.Logf("  - %s", name)
	}
}

func TestE2E_Amazon_Product(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}

	rawHTML := fetchPage(t, "https://www.amazon.com/dp/B0CX23V2ZK", true)
	t.Logf("fetched %d bytes", len(rawHTML))

	page := htmlext.Extract(rawHTML, "https://www.amazon.com/dp/B0CX23V2ZK")
	if page.Title == "" {
		t.Error("empty page title (possible CAPTCHA)")
	} else {
		t.Logf("title: %s", page.Title)
	}

	if len(page.JSONLD) > 0 {
		t.Logf("JSON-LD blocks: %d", len(page.JSONLD))
	}

	t.Logf("embedded patterns found: %d", len(page.EmbeddedScripts))
	for name := range page.EmbeddedScripts {
		t.Logf("  - %s", name)
	}
}

func TestE2E_ProbeExtractPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}

	rawHTML := fetchPage(t, "https://www.youtube.com/watch?v=jNQXAC9IVRw", true)
	page := htmlext.Extract(rawHTML, "https://www.youtube.com/watch?v=jNQXAC9IVRw")

	t.Logf("title: %v", page.Title)
	t.Logf("has json_ld: %v", len(page.JSONLD) > 0)
	t.Logf("has open_graph: %v", len(page.OpenGraph) > 0)
	t.Logf("has embedded_scripts: %v", len(page.EmbeddedScripts) > 0)

	if len(page.EmbeddedScripts) == 0 {
		t.Fatal("pipeline produced no embedded scripts")
	}
	if _, ok := page.EmbeddedScripts["ytInitialData"]; !ok {
		t.Error("pipeline missing ytInitialData")
	}
}

func TestE2E_ExtractFromFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test")
	}

	rawHTML := fetchPage(t, "https://www.youtube.com/watch?v=dQw4w9WgXcQ", true)

	tmpFile, err := os.CreateTemp("", "hermai-e2e-*.html")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(rawHTML); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	tmpFile.Close()

	data, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}

	page := htmlext.Extract(string(data), "")
	if len(page.EmbeddedScripts) == 0 {
		t.Fatal("no patterns found from file")
	}

	if _, ok := page.EmbeddedScripts["ytInitialData"]; !ok {
		t.Error("missing ytInitialData from file extraction")
	}
	if _, ok := page.EmbeddedScripts["ytInitialPlayerResponse"]; !ok {
		t.Error("missing ytInitialPlayerResponse from file extraction")
	}

	t.Logf("file extraction found %d patterns", len(page.EmbeddedScripts))
}
