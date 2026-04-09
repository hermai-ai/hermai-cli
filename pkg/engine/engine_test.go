package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/browser"
	"github.com/hermai-ai/hermai-cli/pkg/fetcher"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// --- Mock implementations ---

type mockBrowser struct {
	captureResult *browser.CaptureResult
	captureErr    error
	captureCalls  int
	closed        bool
}

func (m *mockBrowser) Capture(_ context.Context, _ string, _ browser.CaptureOpts) (*browser.CaptureResult, error) {
	m.captureCalls++
	return m.captureResult, m.captureErr
}

func (m *mockBrowser) Close() error {
	m.closed = true
	return nil
}

type mockAnalyzer struct {
	schema       *schema.Schema
	analyzeErr   error
	analyzeCalls int
	receivedHAR  *browser.HARLog
	suggestCalls int
}

func (m *mockAnalyzer) Analyze(_ context.Context, har *browser.HARLog, _ string, _ string) (*schema.Schema, error) {
	m.analyzeCalls++
	m.receivedHAR = har
	return m.schema, m.analyzeErr
}

func (m *mockAnalyzer) Suggest(_ context.Context, _ string, _ string) (*schema.Schema, error) {
	m.suggestCalls++
	return m.schema, m.analyzeErr
}

func (m *mockAnalyzer) AnalyzeHTML(_ context.Context, _ string, _ string) (*schema.ExtractionRules, error) {
	return nil, fmt.Errorf("mock: AnalyzeHTML not configured")
}

func (m *mockAnalyzer) AnalyzeNextDataPaths(_ context.Context, _ map[string]any, _ string) (map[string]string, error) {
	return nil, nil
}

func (m *mockAnalyzer) Ask(_ context.Context, _ string, _ string) (string, error) {
	return "mock answer", nil
}

type mockCache struct {
	stored      map[string]*schema.Schema
	lookupFn    func(url string) *schema.Schema
	storeCalls  int
	invalidated []string
}

func newMockCache() *mockCache {
	return &mockCache{
		stored: make(map[string]*schema.Schema),
	}
}

func (m *mockCache) Lookup(_ context.Context, targetURL string) (*schema.Schema, error) {
	if m.lookupFn != nil {
		if s := m.lookupFn(targetURL); s != nil {
			return s, nil
		}
	}
	for _, s := range m.stored {
		if s.DiscoveredFrom == targetURL {
			return s, nil
		}
	}
	return nil, nil
}

func (m *mockCache) LookupAll(_ context.Context, targetURL string) (*schema.Schema, *schema.Schema, error) {
	if m.lookupFn != nil {
		if s := m.lookupFn(targetURL); s != nil {
			if s.IsAPISchema() {
				return s, nil, nil
			}
			return nil, s, nil
		}
	}
	for _, s := range m.stored {
		if s.DiscoveredFrom != targetURL {
			continue
		}
		if s.IsAPISchema() {
			return s, nil, nil
		}
		return nil, s, nil
	}
	return nil, nil, nil
}

func (m *mockCache) Store(_ context.Context, s *schema.Schema) error {
	m.storeCalls++
	m.stored[s.ID] = s
	return nil
}

func (m *mockCache) StoreIfNoAPI(ctx context.Context, s *schema.Schema) error {
	return m.Store(ctx, s)
}

func (m *mockCache) Invalidate(_ context.Context, schemaID string) error {
	m.invalidated = append(m.invalidated, schemaID)
	return nil
}

type mockFetcher struct {
	results []*fetcher.Result
	errors  []error
	idx     int
}

func (m *mockFetcher) Fetch(_ context.Context, _ *schema.Schema, _ string, _ fetcher.FetchOpts) (*fetcher.Result, error) {
	i := m.idx
	m.idx++
	if i < len(m.errors) && m.errors[i] != nil {
		return nil, m.errors[i]
	}
	if i < len(m.results) {
		return m.results[i], nil
	}
	return nil, fmt.Errorf("mockFetcher: no result at index %d", i)
}

// --- Helpers ---

func testSchema() *schema.Schema {
	return &schema.Schema{
		ID:         "test-schema-id",
		Domain:     "example.com",
		URLPattern: "/api/data",
		Version:    1,
		Endpoints: []schema.Endpoint{
			{
				Name:        "getData",
				Method:      "GET",
				URLTemplate: "http://localhost:0/api/data",
				IsPrimary:   true,
			},
		},
	}
}

func testCaptureResult() *browser.CaptureResult {
	return &browser.CaptureResult{
		HAR: &browser.HARLog{
			Entries: []browser.HAREntry{
				{
					Request: browser.HARRequest{
						Method: "GET",
						URL:    "https://example.com/api/data",
					},
					Response: browser.HARResponse{
						Status:      200,
						ContentType: "application/json",
						Body:        `{"key":"value"}`,
					},
				},
			},
		},
		DOMSnapshot:  "<html>test</html>",
		RenderedHTML: "<html><head><title>Test</title></head><body><p>Some test content here to pass the threshold</p></body></html>",
	}
}

func testAPIResult(source string, cacheHit bool) *fetcher.Result {
	return &fetcher.Result{
		Data: any(map[string]any{"key": "value"}),
		Metadata: fetcher.ResultMetadata{
			SchemaID:        "test-schema-id",
			SchemaVersion:   1,
			Source:          source,
			CacheHit:        cacheHit,
			EndpointsCalled: 1,
			TotalLatencyMs:  100,
		},
	}
}

// --- Tests ---

func TestCacheHit(t *testing.T) {
	cached := testSchema()
	mc := newMockCache()
	mc.lookupFn = func(_ string) *schema.Schema { return cached }

	mf := &mockFetcher{
		results: []*fetcher.Result{testAPIResult("", false)},
	}

	eng := New(&mockBrowser{}, &mockAnalyzer{}, mf, mc)

	result, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Source != fetcher.SourceAPI {
		t.Errorf("expected Source=%q, got %q", fetcher.SourceAPI, result.Source)
	}
	if result.Metadata.Source != "cache" {
		t.Errorf("expected metadata source=cache, got %q", result.Metadata.Source)
	}
	if !result.Metadata.CacheHit {
		t.Error("expected CacheHit=true")
	}
	if result.URL != "https://example.com/page" {
		t.Errorf("expected URL set, got %q", result.URL)
	}
}

func TestCacheMissDiscoverAndFetch(t *testing.T) {
	// With the async architecture, a cache miss through the browser path
	// returns HTML extraction immediately. API discovery runs in background.
	mb := &mockBrowser{captureResult: testCaptureResult()}
	ma := &mockAnalyzer{schema: testSchema()}
	mc := newMockCache()
	mf := &mockFetcher{
		results: []*fetcher.Result{testAPIResult("", false)},
	}

	eng := New(mb, ma, mf, mc)

	result, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Immediate result should be HTML extraction
	if result.Source != fetcher.SourceHTMLExtraction {
		t.Errorf("expected Source=%q, got %q", fetcher.SourceHTMLExtraction, result.Source)
	}
	if result.Content == nil {
		t.Error("expected Content to be populated")
	}
	if result.Metadata.APISchemaStatus != fetcher.SchemaStatusDiscovering {
		t.Errorf("expected APISchemaStatus=%q, got %q", fetcher.SchemaStatusDiscovering, result.Metadata.APISchemaStatus)
	}
	if mb.captureCalls != 1 {
		t.Errorf("expected 1 browser capture call, got %d", mb.captureCalls)
	}

	// Wait for background API discovery to complete
	eng.WaitBackground()

	// Background should have cached the schema
	if mc.storeCalls == 0 {
		t.Error("expected background API discovery to cache schema")
	}
}

func TestAuthRequired(t *testing.T) {
	mb := &mockBrowser{
		captureErr: fmt.Errorf("%w: https://example.com", browser.ErrAuthWall),
	}
	mc := newMockCache()

	eng := New(mb, &mockAnalyzer{}, &mockFetcher{}, mc)

	_, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, browser.ErrAuthWall) {
		t.Errorf("expected ErrAuthWall, got: %v", err)
	}
}

func TestRetryOnBrokenSchema(t *testing.T) {
	cached := testSchema()
	mc := newMockCache()
	mc.lookupFn = func(_ string) *schema.Schema { return cached }

	mb := &mockBrowser{captureResult: testCaptureResult()}
	newSchema := testSchema()
	newSchema.ID = "new-schema-id"
	newSchema.Version = 2
	ma := &mockAnalyzer{schema: newSchema}

	mf := &mockFetcher{
		results: []*fetcher.Result{nil, testAPIResult("", false)},
		errors:  []error{fmt.Errorf("%w: endpoint returned 404", fetcher.ErrSchemaBroken), nil},
	}

	eng := New(mb, ma, mf, mc)

	result, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{
		RetryOnBrokenSchema: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After retry, the browser path returns HTML extraction immediately
	// (background API discovery runs async)
	if result.Source != fetcher.SourceHTMLExtraction {
		t.Errorf("expected Source=%q after retry, got %q", fetcher.SourceHTMLExtraction, result.Source)
	}
	if len(mc.invalidated) != 1 || mc.invalidated[0] != "test-schema-id" {
		t.Errorf("expected invalidation of test-schema-id, got %v", mc.invalidated)
	}
	if mb.captureCalls != 1 {
		t.Errorf("expected 1 browser capture for re-discovery, got %d", mb.captureCalls)
	}

	eng.WaitBackground()
}

func TestNoRetryWhenDisabled(t *testing.T) {
	cached := testSchema()
	mc := newMockCache()
	mc.lookupFn = func(_ string) *schema.Schema { return cached }

	mf := &mockFetcher{
		errors: []error{fmt.Errorf("%w: endpoint returned 404", fetcher.ErrSchemaBroken)},
	}

	eng := New(&mockBrowser{}, &mockAnalyzer{}, mf, mc)

	_, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{
		RetryOnBrokenSchema: false,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, fetcher.ErrSchemaBroken) {
		t.Errorf("expected fetcher.ErrSchemaBroken, got: %v", err)
	}
}

func TestDiscoverCalledOncePerMiss(t *testing.T) {
	mb := &mockBrowser{captureResult: testCaptureResult()}
	ma := &mockAnalyzer{schema: testSchema()}
	mc := newMockCache()
	mf := &mockFetcher{
		results: []*fetcher.Result{testAPIResult("", false)},
	}

	eng := New(mb, ma, mf, mc)

	_, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mb.captureCalls != 1 {
		t.Errorf("expected exactly 1 browser capture call, got %d", mb.captureCalls)
	}

	// Analyzer is now called in background goroutine
	eng.WaitBackground()
	if ma.analyzeCalls != 1 {
		t.Errorf("expected exactly 1 analyzer call, got %d", ma.analyzeCalls)
	}
}

func TestFilterHARIsCalled(t *testing.T) {
	rawHAR := &browser.HARLog{
		Entries: []browser.HAREntry{
			{
				Request: browser.HARRequest{
					Method: "GET",
					URL:    "https://example.com/api/data",
				},
				Response: browser.HARResponse{
					Status:      200,
					ContentType: "application/json",
					Body:        `{"key":"value"}`,
				},
			},
			{
				Request: browser.HARRequest{
					Method: "GET",
					URL:    "https://example.com/analytics/track",
				},
				Response: browser.HARResponse{
					Status:      200,
					ContentType: "application/json",
					Body:        `{}`,
				},
			},
		},
	}

	mb := &mockBrowser{
		captureResult: &browser.CaptureResult{
			HAR:          rawHAR,
			DOMSnapshot:  "<html>test</html>",
			RenderedHTML: "<html><head><title>Test</title></head><body><p>Content here for threshold</p></body></html>",
		},
	}
	ma := &mockAnalyzer{schema: testSchema()}
	mc := newMockCache()
	mf := &mockFetcher{
		results: []*fetcher.Result{testAPIResult("", false)},
	}

	eng := New(mb, ma, mf, mc)

	_, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait for background goroutine that calls analyzer
	eng.WaitBackground()

	if ma.receivedHAR == nil {
		t.Fatal("analyzer should have received a HAR log")
	}

	for _, entry := range ma.receivedHAR.Entries {
		if entry.Request.URL == "https://example.com/analytics/track" {
			t.Error("expected analytics URL to be filtered out, but it was passed to analyzer")
		}
	}

	if len(ma.receivedHAR.Entries) != 1 {
		t.Errorf("expected 1 filtered entry, got %d", len(ma.receivedHAR.Entries))
	}
}

func TestNoBrowserMode(t *testing.T) {
	suggestSchema := testSchema()
	suggestSchema.ID = "suggest-schema"
	ma := &mockAnalyzer{schema: suggestSchema}
	mc := newMockCache()
	mf := &mockFetcher{
		results: []*fetcher.Result{testAPIResult("", false)},
	}
	mb := &mockBrowser{}

	eng := New(mb, ma, mf, mc)

	result, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{
		NoBrowser: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mb.captureCalls != 0 {
		t.Errorf("expected 0 browser capture calls in no-browser mode, got %d", mb.captureCalls)
	}
	if result.Source != fetcher.SourceAPI {
		t.Errorf("expected Source=%q, got %q", fetcher.SourceAPI, result.Source)
	}
	if mc.storeCalls == 0 {
		t.Error("expected schema to be cached")
	}
}

func TestOnSchemaDiscoveredSync(t *testing.T) {
	suggestSchema := testSchema()
	suggestSchema.ID = "suggest-schema"
	ma := &mockAnalyzer{schema: suggestSchema}
	mc := newMockCache()
	mf := &mockFetcher{
		results: []*fetcher.Result{testAPIResult("", false)},
	}

	eng := New(&mockBrowser{}, ma, mf, mc)

	var discoveries []SchemaDiscovery
	result, err := eng.Fetch(context.Background(), "http://127.0.0.1:1/page", FetchOpts{
		NoBrowser: true,
		OnSchemaDiscovered: func(d SchemaDiscovery) {
			discoveries = append(discoveries, d)
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Source != fetcher.SourceAPI {
		t.Fatalf("expected Source=%q, got %q", fetcher.SourceAPI, result.Source)
	}
	if len(discoveries) != 1 {
		t.Fatalf("expected 1 discovery callback, got %d", len(discoveries))
	}
	if discoveries[0].SchemaID != suggestSchema.ID {
		t.Errorf("expected schema ID %q, got %q", suggestSchema.ID, discoveries[0].SchemaID)
	}
	if discoveries[0].Async {
		t.Error("expected sync discovery callback")
	}
}

func TestNoBrowserModeUsesCache(t *testing.T) {
	cached := testSchema()
	mc := newMockCache()
	mc.lookupFn = func(_ string) *schema.Schema { return cached }

	mf := &mockFetcher{
		results: []*fetcher.Result{testAPIResult("", false)},
	}

	eng := New(&mockBrowser{}, &mockAnalyzer{}, mf, mc)

	result, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{
		NoBrowser: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Source != fetcher.SourceAPI {
		t.Errorf("expected Source=%q, got %q", fetcher.SourceAPI, result.Source)
	}
	if result.Metadata.Source != "cache" {
		t.Errorf("expected metadata source=cache, got %q", result.Metadata.Source)
	}
	if !result.Metadata.CacheHit {
		t.Error("expected CacheHit=true")
	}
}

func TestBrowserNoAPICalls_FallsToHTMLExtraction(t *testing.T) {
	mb := &mockBrowser{
		captureResult: &browser.CaptureResult{
			HAR:          &browser.HARLog{Entries: nil},
			RenderedHTML: "<html><head><title>Test Page</title></head><body><p>Some content here that is long enough</p></body></html>",
		},
	}
	mc := newMockCache()

	eng := New(mb, &mockAnalyzer{}, &mockFetcher{}, mc)

	result, err := eng.Fetch(context.Background(), "https://example.com/ssr-page", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Source != fetcher.SourceHTMLExtraction {
		t.Errorf("expected Source=%q, got %q", fetcher.SourceHTMLExtraction, result.Source)
	}
	if result.Metadata.Source != fetcher.SourceHTMLExtract {
		t.Errorf("expected metadata source=%s, got %q", fetcher.SourceHTMLExtract, result.Metadata.Source)
	}
	if mb.captureCalls != 1 {
		t.Errorf("expected 1 browser capture call, got %d", mb.captureCalls)
	}

	content, ok := result.Content.(map[string]any)
	if !ok {
		t.Fatalf("expected Content as map[string]any, got %T", result.Content)
	}
	if content["title"] != "Test Page" {
		t.Errorf("expected title='Test Page', got %q", content["title"])
	}
}

func TestNoBrowserSuggestFails_FallsToProbeHTML(t *testing.T) {
	ma := &mockAnalyzer{analyzeErr: fmt.Errorf("no suggestions")}
	mc := newMockCache()

	eng := New(nil, ma, &mockFetcher{}, mc)

	probeHTML := "<html><head><title>Probe Page</title></head><body><p>Content from probe</p></body></html>"
	result, err := eng.fallbackNoBrowser(context.Background(), "https://example.com/page", probeHTML, timeNow(), FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Source != fetcher.SourceHTMLExtraction {
		t.Errorf("expected Source=%q, got %q", fetcher.SourceHTMLExtraction, result.Source)
	}
	if result.Metadata.Source != fetcher.SourceHTMLExtract {
		t.Errorf("expected metadata source=%s, got %q", fetcher.SourceHTMLExtract, result.Metadata.Source)
	}
}

func TestExtractFromDOM_WithJSONLD(t *testing.T) {
	mc := newMockCache()
	eng := New(nil, &mockAnalyzer{}, &mockFetcher{}, mc)

	htmlWithJSONLD := `<html><head>
		<title>Article</title>
		<script type="application/ld+json">{"@type":"Article","name":"Test","author":"Bob"}</script>
	</head><body><p>Article body</p></body></html>`

	result, err := eng.extractFromDOM(context.Background(), "https://example.com/article", htmlWithJSONLD, timeNow())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Source != fetcher.SourceHTMLExtraction {
		t.Errorf("expected Source=%q, got %q", fetcher.SourceHTMLExtraction, result.Source)
	}
	if result.Metadata.Source != fetcher.SourceHTMLExtract {
		t.Errorf("expected metadata source=%s, got %q", fetcher.SourceHTMLExtract, result.Metadata.Source)
	}

	content, ok := result.Content.(map[string]any)
	if !ok {
		t.Fatalf("expected Content as map[string]any, got %T", result.Content)
	}
	if content["title"] != "Article" {
		t.Errorf("expected title='Article', got %v", content["title"])
	}
}

func TestAsyncDiscovery_PopulatesCache(t *testing.T) {
	// Verify: browser path returns HTML immediately, background populates cache
	mb := &mockBrowser{captureResult: testCaptureResult()}
	ma := &mockAnalyzer{schema: testSchema()}
	mc := newMockCache()
	mf := &mockFetcher{
		results: []*fetcher.Result{testAPIResult("", false)},
	}

	eng := New(mb, ma, mf, mc)

	// First fetch: returns HTML extraction, background discovers API
	result, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Source != fetcher.SourceHTMLExtraction {
		t.Errorf("first fetch: expected Source=%q, got %q", fetcher.SourceHTMLExtraction, result.Source)
	}

	// Wait for background work
	eng.WaitBackground()

	// Verify schema was cached
	if mc.storeCalls == 0 {
		t.Fatal("expected background to cache API schema")
	}

	// Second fetch: should hit cache and return API data
	mc.lookupFn = func(_ string) *schema.Schema {
		for _, s := range mc.stored {
			return s
		}
		return nil
	}

	eng2 := New(mb, ma, mf, mc)
	result2, err := eng2.Fetch(context.Background(), "https://example.com/page", FetchOpts{})
	if err != nil {
		t.Fatalf("second fetch: unexpected error: %v", err)
	}
	if result2.Source != fetcher.SourceAPI {
		t.Errorf("second fetch: expected Source=%q, got %q", fetcher.SourceAPI, result2.Source)
	}
	if !result2.Metadata.CacheHit {
		t.Error("second fetch: expected CacheHit=true")
	}
}

func TestResultPayload(t *testing.T) {
	apiData := "api_value"
	apiResult := &fetcher.Result{
		Source: fetcher.SourceAPI,
		Data:   apiData,
	}
	if apiResult.Payload() != apiData {
		t.Error("API result Payload() should return Data")
	}

	htmlContent := "html_content"
	htmlResult := &fetcher.Result{
		Source:  fetcher.SourceHTMLExtraction,
		Content: htmlContent,
	}
	if htmlResult.Payload() != htmlContent {
		t.Error("HTML result Payload() should return Content")
	}
}

func TestClose(t *testing.T) {
	mb := &mockBrowser{}
	eng := New(mb, &mockAnalyzer{}, &mockFetcher{}, newMockCache())

	if err := eng.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mb.closed {
		t.Error("expected browser.Close() to be called")
	}
}

func TestCloseNilBrowser(t *testing.T) {
	eng := New(nil, &mockAnalyzer{}, &mockFetcher{}, newMockCache())

	if err := eng.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

type mockAnalyzerTracking struct {
	mockAnalyzer
	analyzeHTMLCalls int
}

func (m *mockAnalyzerTracking) AnalyzeHTML(_ context.Context, _ string, _ string) (*schema.ExtractionRules, error) {
	m.analyzeHTMLCalls++
	return &schema.ExtractionRules{
		PageType:        "listing",
		ContentSelector: "main",
	}, nil
}

func TestCSSDiscoverySkippedWhenAPISucceeds(t *testing.T) {
	ma := &mockAnalyzerTracking{
		mockAnalyzer: mockAnalyzer{schema: testSchema()},
	}
	mb := &mockBrowser{captureResult: testCaptureResult()}
	mc := newMockCache()
	mf := &mockFetcher{results: []*fetcher.Result{testAPIResult("", false)}}

	eng := New(mb, ma, mf, mc)

	_, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	eng.WaitBackground()

	if ma.analyzeCalls != 1 {
		t.Errorf("expected 1 Analyze call (HAR), got %d", ma.analyzeCalls)
	}
	if ma.analyzeHTMLCalls != 0 {
		t.Errorf("expected 0 AnalyzeHTML calls (CSS discovery should be skipped when API succeeds), got %d", ma.analyzeHTMLCalls)
	}
}

func TestOnSchemaDiscoveredBackground(t *testing.T) {
	ma := &mockAnalyzer{schema: testSchema()}
	mb := &mockBrowser{captureResult: testCaptureResult()}
	mc := newMockCache()
	mf := &mockFetcher{results: []*fetcher.Result{testAPIResult("", false)}}

	eng := New(mb, ma, mf, mc)

	var (
		mu          sync.Mutex
		discoveries []SchemaDiscovery
	)
	_, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{
		OnSchemaDiscovered: func(d SchemaDiscovery) {
			mu.Lock()
			discoveries = append(discoveries, d)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	eng.WaitBackground()

	mu.Lock()
	defer mu.Unlock()
	if len(discoveries) != 1 {
		t.Fatalf("expected 1 discovery callback, got %d", len(discoveries))
	}
	if discoveries[0].SchemaID != testSchema().ID {
		t.Errorf("expected schema ID %q, got %q", testSchema().ID, discoveries[0].SchemaID)
	}
	if !discoveries[0].Async {
		t.Error("expected async discovery callback")
	}
}

func TestCSSDiscoveryRunsWhenAPIFails(t *testing.T) {
	ma := &mockAnalyzerTracking{
		mockAnalyzer: mockAnalyzer{analyzeErr: fmt.Errorf("no endpoints")},
	}
	mb := &mockBrowser{captureResult: testCaptureResult()}
	mc := newMockCache()
	mf := &mockFetcher{results: []*fetcher.Result{testAPIResult("", false)}}

	eng := New(mb, ma, mf, mc)

	_, err := eng.Fetch(context.Background(), "https://example.com/page", FetchOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	eng.WaitBackground()

	if ma.analyzeCalls != 1 {
		t.Errorf("expected 1 Analyze call (HAR), got %d", ma.analyzeCalls)
	}
	if ma.analyzeHTMLCalls != 1 {
		t.Errorf("expected 1 AnalyzeHTML call (CSS fallback after API fail), got %d", ma.analyzeHTMLCalls)
	}
}

func timeNow() time.Time {
	return time.Now()
}
