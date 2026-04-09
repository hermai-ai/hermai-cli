package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
	"github.com/hermai-ai/hermai-cli/pkg/analyzer"
	"github.com/hermai-ai/hermai-cli/pkg/browser"
	"github.com/hermai-ai/hermai-cli/pkg/cache"
	"github.com/hermai-ai/hermai-cli/pkg/fetcher"
	"github.com/hermai-ai/hermai-cli/pkg/htmlext"
	"github.com/hermai-ai/hermai-cli/pkg/log"
	"github.com/hermai-ai/hermai-cli/pkg/probe"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// llmTimeout is the maximum time allowed for LLM analysis.
const llmTimeout = 120 * time.Second

// bgTimeout is the maximum time for background API discovery goroutines.
const bgTimeout = 5 * time.Minute

// Engine is the core orchestrator that wires together cache, browser,
// analyzer, and fetcher to produce structured data from any URL.
type Engine struct {
	browser  browser.Service
	analyzer analyzer.Service
	fetcher  fetcher.Service
	cache    cache.Service
	log      *log.Logger

	// noCache is set per-request to skip cache read/write.
	noCache bool

	// bg tracks in-flight background goroutines (API discovery, CSS selector analysis).
	bg *sync.WaitGroup
}

// New creates a new Engine with the given service dependencies.
func New(b browser.Service, a analyzer.Service, f fetcher.Service, c cache.Service) *Engine {
	return &Engine{
		browser:  b,
		analyzer: a,
		fetcher:  f,
		cache:    c,
		log:      log.Nop(),
		bg:       &sync.WaitGroup{},
	}
}

// WithLogger sets the engine's logger. Must be called before Fetch.
func (e *Engine) WithLogger(l *log.Logger) *Engine {
	return &Engine{
		browser:  e.browser,
		analyzer: e.analyzer,
		fetcher:  e.fetcher,
		cache:    e.cache,
		log:      l,
		bg:       e.bg, // share the same WaitGroup pointer
	}
}

// WaitBackground blocks until all background goroutines (API discovery,
// CSS selector analysis) have completed. Call this after Fetch returns
// to ensure cache is populated before process exit.
func (e *Engine) WaitBackground() {
	e.bg.Wait()
}

// startBackground launches a tracked background goroutine.
func (e *Engine) startBackground(fn func()) {
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		fn()
	}()
}

func (e *Engine) notifySchemaDiscovered(opts FetchOpts, s *schema.Schema, async bool) {
	if s == nil || opts.OnSchemaDiscovered == nil {
		return
	}
	opts.OnSchemaDiscovered(SchemaDiscovery{
		SchemaID:   s.ID,
		SchemaType: s.SchemaType,
		Async:      async,
	})
}

// Fetch retrieves structured data from the target URL.
// Cache-first with priority: API schema > CSS selectors > discovery pipeline.
func (e *Engine) Fetch(ctx context.Context, targetURL string, opts FetchOpts) (*fetcher.Result, error) {
	// Normalize platform-specific URLs (e.g., Zillow search filters)
	if normalized, err := probe.NormalizeZillowSearchURL(targetURL); err == nil {
		targetURL = normalized
	}

	e.noCache = opts.NoCache

	if !e.noCache {
		apiSchema, cssSchema, err := e.cache.LookupAll(ctx, targetURL)
		if err != nil {
			return nil, fmt.Errorf("cache lookup failed: %w", err)
		}

		// Priority 1: API schema (highest quality)
		if apiSchema != nil {
			if opts.CatalogMode && apiSchema.Coverage != schema.SchemaCoverageComplete {
				e.startCatalogEnrichment(targetURL, opts)
			}
			return e.fetchCached(ctx, targetURL, opts, apiSchema)
		}

		// Priority 2: CSS selector schema (better than generic extraction)
		if cssSchema != nil {
			if opts.CatalogMode {
				e.startCatalogEnrichment(targetURL, opts)
			}
			return e.fetchCached(ctx, targetURL, opts, cssSchema)
		}
	}

	return e.pipeline(ctx, targetURL, opts)
}

// fetchCached handles a cache hit: either an API schema or cached HTML
// extraction rules. If the API schema is broken and retry is enabled,
// invalidates the cache and re-runs the full pipeline.
func (e *Engine) fetchCached(ctx context.Context, targetURL string, opts FetchOpts, s *schema.Schema) (*fetcher.Result, error) {
	// Propagate stealth requirement from schema to opts
	if s.RequiresStealth {
		opts.Stealth = true
	}

	if s.IsHTMLExtraction() {
		return e.applyExtractionRules(ctx, targetURL, opts, s)
	}

	e.log.Info("cache hit for %s (schema=%s)", targetURL, s.ID)

	result, err := e.fetcher.Fetch(ctx, s, targetURL, opts.toFetchOpts())
	if err != nil {
		if errors.Is(err, fetcher.ErrSchemaBroken) && opts.RetryOnBrokenSchema {
			e.log.Info("cached schema broken, invalidating and re-discovering")
			if invErr := e.cache.Invalidate(ctx, s.ID); invErr != nil {
				return nil, fmt.Errorf("cache invalidation failed: %w", invErr)
			}
			return e.pipeline(ctx, targetURL, opts)
		}
		return nil, err
	}

	result.URL = targetURL
	result.Source = fetcher.SourceAPI
	result.Metadata.Source = "cache"
	result.Metadata.CacheHit = true
	result.Metadata.APISchemaStatus = fetcher.SchemaStatusCached
	return result, nil
}

func (e *Engine) startCatalogEnrichment(targetURL string, opts FetchOpts) {
	if opts.NoBrowser || e.browser == nil {
		return
	}

	captureOpts := opts.toCaptureOpts()
	noCache := e.noCache
	e.startBackground(func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), bgTimeout)
		defer cancel()

		capture, err := e.browser.Capture(bgCtx, targetURL, captureOpts)
		if err != nil || capture == nil {
			return
		}

		filtered := browser.FilterHAR(capture.HAR)
		apiFound := false
		if len(filtered.Entries) > 0 {
			apiFound = e.discoverAPIFromHAR(filtered, capture.DOMSnapshot, targetURL, noCache, opts)
		}

		if !apiFound && e.analyzer != nil && !noCache && capture.RenderedHTML != "" {
			page := htmlext.Extract(capture.RenderedHTML, targetURL)
			if len(page.JSONLD) == 0 {
				e.discoverSelectorsFromDOM(capture.RenderedHTML, targetURL, opts, true)
			}
		}
	})
}

func (e *Engine) storeAPISchema(ctx context.Context, targetURL string, s *schema.Schema, opts FetchOpts, async bool) error {
	if s == nil {
		return nil
	}
	s.SchemaType = schema.SchemaTypeAPI

	existingAPI, existingCSS, err := e.cache.LookupAll(ctx, targetURL)
	if err != nil {
		return err
	}
	isFirstSchema := existingAPI == nil && existingCSS == nil
	if existingAPI != nil && existingAPI.IsAPISchema() {
		s = schema.MergeAPISchemas(existingAPI, s)
	}
	if err := e.cache.Store(ctx, s); err != nil {
		return err
	}
	if isFirstSchema {
		e.notifySchemaDiscovered(opts, s, async)
	}
	return nil
}

func (e *Engine) storeCSSSchemaIfNoAPI(ctx context.Context, targetURL string, s *schema.Schema, opts FetchOpts, async bool) error {
	if s == nil {
		return nil
	}

	existingAPI, existingCSS, err := e.cache.LookupAll(ctx, targetURL)
	if err != nil {
		return err
	}
	isFirstSchema := existingAPI == nil && existingCSS == nil

	if err := e.cache.StoreIfNoAPI(ctx, s); err != nil {
		return err
	}

	if !isFirstSchema {
		return nil
	}

	_, cssAfter, err := e.cache.LookupAll(ctx, targetURL)
	if err != nil {
		return err
	}
	if cssAfter != nil && cssAfter.ID == s.ID {
		e.notifySchemaDiscovered(opts, s, async)
	}
	return nil
}

// pipeline runs the 2-layer discovery strategy:
//
//	Layer 1 — Probe (no browser, no LLM, ~200ms):
//	  - Known site JSON API -> validate -> cache -> fetch
//	  - Rich SSR HTML (title + body) -> deterministic extraction -> done
//	  - Thin HTML (SPA shell) -> escalate to Layer 2
//
//	Layer 2 — Browser (one pass captures HAR + rendered DOM):
//	  - Immediately return HTML extraction from rendered DOM (3-5s)
//	  - Background: HAR -> LLM classify -> validate -> cache API schema
//	  - Background: LLM CSS selector analysis -> cache selectors
func (e *Engine) pipeline(ctx context.Context, targetURL string, opts FetchOpts) (*fetcher.Result, error) {
	start := time.Now()

	// ── Layer 1: Probe ──────────────────────────────────────
	// Skip probe when user provided cookies — probe is unauthenticated HTTP
	// and will get a public/blocked page instead of the authenticated content.
	probeStart := time.Now()
	var probeResult *probe.Result
	var probeErr error
	if len(opts.Cookies) > 0 {
		e.log.Debug("skipping probe (cookies provided, going straight to browser)")
		probeErr = fmt.Errorf("skipped: cookies require browser")
	} else {
		probeResult, probeErr = probe.Probe(ctx, targetURL, probe.Options{
			ProxyURL: opts.ProxyURL,
			Timeout:  5 * time.Second,
			Insecure: opts.Insecure,
		})
	}
	e.log.Debug("probe completed in %dms", time.Since(probeStart).Milliseconds())
	if probeErr == nil && probeResult.RequiresStealth {
		e.log.Info("probe: anti-bot detected, stealth TLS bypass succeeded")
	}

	// 1a. Probe found JSON API candidates -> validate, cache, fetch
	if probeErr == nil && len(probeResult.Candidates) > 0 {
		bestCandidate := probeResult.Candidates[0]
		e.log.Info("probe: found %d JSON candidate(s), best=%s", len(probeResult.Candidates), bestCandidate.Strategy)
		if probeResult.RequiresStealth {
			bestCandidate.Schema.RequiresStealth = true
			opts.Stealth = true
		}
		validated, err := e.validateAndCache(ctx, bestCandidate.Schema, targetURL, validationOptions{
			requireSemanticMatch: true,
			stealth:              probeResult.RequiresStealth,
		}, opts)
		if err == nil {
			if opts.CatalogMode {
				e.startCatalogEnrichment(targetURL, opts)
			}
			return e.fetchViaSchema(ctx, validated, targetURL, opts, start, "probe:"+bestCandidate.Strategy)
		}
		e.log.Info("probe schema validation failed: %v, continuing", err)
	}

	// 1b. Probe got HTML
	var probeHTML string
	if probeErr == nil && probeResult.HTMLBody != "" {
		probeHTML = probeResult.HTMLBody

		// 1b-i. Try cached CSS selectors on probe HTML (higher quality than generic extraction)
		if !e.noCache {
			_, cssSchema, lookupErr := e.cache.LookupAll(ctx, targetURL)
			if lookupErr == nil && cssSchema != nil && len(cssSchema.ExtractionRules.EntityFields) > 0 {
				entityResult := htmlext.ApplyEntityExtraction(probeHTML, cssSchema.ExtractionRules)
				if len(entityResult) > 0 {
					e.log.Info("probe: applied cached CSS selectors to probe HTML")
					return &fetcher.Result{
						URL:     targetURL,
						Source:  fetcher.SourceHTMLExtraction,
						Content: entityResult,
						Metadata: fetcher.ResultMetadata{
							SchemaID:       cssSchema.ID,
							SchemaVersion:  cssSchema.Version,
							Source:         fetcher.SourceHTMLExtractCached,
							CacheHit:       true,
							TotalLatencyMs: time.Since(start).Milliseconds(),
						},
					}, nil
				}
			}
		}

		// 1b-ii. Rich SSR HTML — extract deterministically
		page := htmlext.Extract(probeHTML, targetURL)
		if contentIsUseful(page) {
			e.log.Info("probe: rich SSR HTML (%d chars body), extracting deterministically", len(page.BodyText))
			structured := htmlext.ExtractStructured(probeHTML, targetURL)

			// Cache __NEXT_DATA__ discovery so subsequent fetches skip probe.
			// If an analyzer is available, ask the LLM to identify named
			// extraction paths so subsequent fetches return targeted data.
			if !e.noCache && page.NextData != nil {
				e.log.Info("caching __NEXT_DATA__ schema for %s", targetURL)
				capturedStructured := structured
				e.startBackground(func() {
					bgCtx, cancel := context.WithTimeout(context.Background(), bgTimeout)
					defer cancel()
					var paths map[string]string
					if e.analyzer != nil {
						var analyzeErr error
						paths, analyzeErr = e.analyzer.AnalyzeNextDataPaths(bgCtx, capturedStructured, targetURL)
						if analyzeErr != nil {
							e.log.Debug("NextDataPaths analysis failed: %v", analyzeErr)
						} else if len(paths) > 0 {
							e.log.Info("discovered %d NextDataPaths for %s", len(paths), targetURL)
						}
					}
					s := schema.FromNextDataWithPaths(targetURL, paths)
					if err := e.storeCSSSchemaIfNoAPI(bgCtx, targetURL, s, opts, true); err != nil {
						e.log.Debug("__NEXT_DATA__ schema cache failed: %v", err)
					}
				})
			} else if e.analyzer != nil && !e.noCache && len(page.JSONLD) == 0 {
				// Background: discover CSS selectors so the 2nd visit is structured
				e.log.Info("starting background CSS selector discovery for %s", targetURL)
				html := probeHTML
				e.startBackground(func() {
					e.discoverSelectorsFromDOM(html, targetURL, opts, true)
				})
			} else {
				e.log.Debug("skipping background CSS discovery: analyzer=%v noCache=%v jsonLD=%d", e.analyzer != nil, e.noCache, len(page.JSONLD))
			}

			if opts.CatalogMode {
				e.startCatalogEnrichment(targetURL, opts)
			}

			return &fetcher.Result{
				URL:     targetURL,
				Source:  fetcher.SourceHTMLExtraction,
				Content: structured,
				Metadata: fetcher.ResultMetadata{
					Source:         fetcher.SourceHTMLExtract,
					TotalLatencyMs: time.Since(start).Milliseconds(),
				},
			}, nil
		}
		e.log.Debug("probe: thin HTML (%d chars body), escalating to browser", len(page.BodyText))
	}

	// ── Layer 2: Browser ────────────────────────────────────
	if opts.NoBrowser || e.browser == nil {
		return e.fallbackNoBrowser(ctx, targetURL, probeHTML, start, opts)
	}

	browserStart := time.Now()
	capture, captureErr := e.browser.Capture(ctx, targetURL, opts.toCaptureOpts())
	e.log.Debug("browser capture completed in %dms", time.Since(browserStart).Milliseconds())

	if captureErr != nil {
		// Auth walls and context errors are not recoverable
		if errors.Is(captureErr, browser.ErrAuthWall) ||
			errors.Is(captureErr, context.Canceled) ||
			errors.Is(captureErr, context.DeadlineExceeded) {
			return nil, captureErr
		}
		e.log.Info("browser capture failed: %v", captureErr)
		return e.fallbackNoBrowser(ctx, targetURL, probeHTML, start, opts)
	}

	var renderedHTML string
	if capture != nil {
		renderedHTML = capture.RenderedHTML
	}

	// Filter HAR for later background use
	filtered := browser.FilterHAR(capture.HAR)
	e.log.Debug("captured %d requests, %d after filtering",
		len(capture.HAR.Entries), len(filtered.Entries))

	// ── Cookies mode: extract API data directly from HAR ───
	// When the user provided cookies, the browser captured authenticated API
	// responses. Return the richest one directly instead of just HTML extraction.
	if len(opts.Cookies) > 0 && len(filtered.Entries) > 0 {
		// Debug: log how many entries have bodies
		bodied := 0
		for _, e := range filtered.Entries {
			if e.Response.Body != "" {
				bodied++
			}
		}
		e.log.Debug("HAR entries with bodies: %d/%d", bodied, len(filtered.Entries))

		if apiData := extractBestHARResponse(filtered, targetURL); apiData != nil {
			e.log.Info("extracted API data directly from browser HAR (%d bytes)", len(fmt.Sprintf("%v", apiData)))

			result := &fetcher.Result{
				URL:     targetURL,
				Source:  fetcher.SourceAPI,
				Data:    apiData,
				Metadata: fetcher.ResultMetadata{
					Source:         "browser:har_extract",
					TotalLatencyMs: time.Since(start).Milliseconds(),
				},
			}

			// Still run background API discovery for schema caching
			if e.analyzer != nil && !e.noCache {
				noCache := e.noCache
				domSnapshot := capture.DOMSnapshot
				e.startBackground(func() {
					e.discoverAPIFromHAR(filtered, domSnapshot, targetURL, noCache, opts)
				})
				result.Metadata.APISchemaStatus = fetcher.SchemaStatusDiscovering
			}

			return result, nil
		}
	}

	// ── Immediate: HTML extraction from rendered DOM ────────
	if renderedHTML != "" {
		structured := htmlext.ExtractStructured(renderedHTML, targetURL)

		// Check if we have cached CSS selectors for richer extraction
		if !e.noCache {
			if selectorSchema, lookupErr := e.cache.Lookup(ctx, targetURL); lookupErr == nil && selectorSchema != nil && selectorSchema.IsHTMLExtraction() {
				if len(selectorSchema.ExtractionRules.EntityFields) > 0 {
					entityResult := htmlext.ApplyEntityExtraction(renderedHTML, selectorSchema.ExtractionRules)
					if len(entityResult) > 0 {
						structured = entityResult
					}
				}
			}
		}

		result := &fetcher.Result{
			URL:     targetURL,
			Source:  fetcher.SourceHTMLExtraction,
			Content: structured,
			Metadata: fetcher.ResultMetadata{
				Source:         fetcher.SourceHTMLExtract,
				TotalLatencyMs: time.Since(start).Milliseconds(),
			},
		}

		// ── Background: API discovery from HAR, CSS discovery as fallback ──
		if len(filtered.Entries) > 0 || (e.analyzer != nil && !e.noCache) {
			noCache := e.noCache
			domSnapshot := capture.DOMSnapshot
			html := renderedHTML
			hasHAREntries := len(filtered.Entries) > 0
			hasAnalyzer := e.analyzer != nil

			e.startBackground(func() {
				apiFound := false
				if hasHAREntries {
					apiFound = e.discoverAPIFromHAR(filtered, domSnapshot, targetURL, noCache, opts)
				}

				// CSS selector discovery only if API discovery failed
				if !apiFound && hasAnalyzer && !noCache {
					page := htmlext.Extract(html, targetURL)
					if len(page.JSONLD) == 0 {
						e.discoverSelectorsFromDOM(html, targetURL, opts, true)
					}
				}
			})
			if hasHAREntries {
				result.Metadata.APISchemaStatus = fetcher.SchemaStatusDiscovering
			}
		}

		return result, nil
	}

	// Browser returned no rendered HTML — try probe HTML as last resort
	if probeHTML != "" {
		structured := htmlext.ExtractStructured(probeHTML, targetURL)
		if opts.CatalogMode {
			e.startCatalogEnrichment(targetURL, opts)
		}
		return &fetcher.Result{
			URL:     targetURL,
			Source:  fetcher.SourceHTMLExtraction,
			Content: structured,
			Metadata: fetcher.ResultMetadata{
				Source:         fetcher.SourceHTMLExtract,
				TotalLatencyMs: time.Since(start).Milliseconds(),
			},
		}, nil
	}

	return nil, ErrNoEndpoints
}

// discoverAPIFromHAR runs LLM classification on HAR entries, validates
// candidate endpoints, and caches any valid API schema. Runs in background.
func (e *Engine) discoverAPIFromHAR(filtered *browser.HARLog, domSnapshot, targetURL string, noCache bool, opts FetchOpts) bool {
	bgCtx, bgCancel := context.WithTimeout(context.Background(), bgTimeout)
	defer bgCancel()

	llmCtx, llmCancel := context.WithTimeout(bgCtx, llmTimeout)
	defer llmCancel()

	s, err := e.analyzer.Analyze(llmCtx, filtered, domSnapshot, targetURL)
	if err != nil {
		e.log.Debug("background API discovery: LLM analysis failed: %v", err)
		return false
	}
	if len(s.Endpoints) == 0 {
		e.log.Debug("background API discovery: no endpoints found")
		return false
	}

	// When user provided cookies, skip HTTP validation — the browser already
	// verified these endpoints (200 status + JSON response in the HAR).
	// Re-validating via HTTP fails for sites like LinkedIn that check TLS
	// fingerprints and kill sessions from non-browser clients.
	var validated []schema.Endpoint
	if len(opts.Cookies) > 0 {
		validated = s.Endpoints
		e.log.Info("background API discovery: trusting %d browser-verified endpoints (cookies mode)", len(validated))
	} else {
		validated = validateAllEndpoints(bgCtx, s, validationOptions{cookies: opts.Cookies})
	}
	if len(validated) == 0 {
		e.log.Info("background API discovery: none of %d candidates passed validation", len(s.Endpoints))
		return false
	}

	if len(opts.Cookies) == 0 {
		e.log.Info("background API discovery: %d of %d endpoints validated", len(validated), len(s.Endpoints))
	}
	validated[0].IsPrimary = true
	s.Endpoints = validated
	s.SchemaType = schema.SchemaTypeAPI
	s.Coverage = schema.SchemaCoverageComplete

	if !noCache {
		if err := e.storeAPISchema(bgCtx, targetURL, s, opts, true); err != nil {
			e.log.Debug("background API discovery: cache store failed: %v", err)
		}
	}
	return true
}

// discoverSelectorsFromDOM asks the LLM to identify CSS selectors for
// structured content extraction, and caches the rules. Runs in background.
func (e *Engine) discoverSelectorsFromDOM(renderedHTML, targetURL string, opts FetchOpts, async bool) {
	bgCtx, bgCancel := context.WithTimeout(context.Background(), bgTimeout)
	defer bgCancel()

	llmCtx, llmCancel := context.WithTimeout(bgCtx, llmTimeout)
	defer llmCancel()

	rules, err := e.analyzer.AnalyzeHTML(llmCtx, renderedHTML, targetURL)
	if err != nil {
		e.log.Debug("background selector discovery: LLM failed: %v", err)
		return
	}
	if rules == nil {
		return
	}

	hasEntityFields := len(rules.EntityFields) > 0
	hasTableRows := rules.TableRows != nil && len(rules.TableRows.Fields) > 0

	if !hasEntityFields && !hasTableRows {
		e.log.Debug("background selector discovery: no extraction rules found")
		return
	}

	if hasTableRows {
		e.log.Info("background selector discovery: table_rows strategy with %d fields (group_size=%d)", len(rules.TableRows.Fields), rules.TableRows.GroupSize)
	} else {
		e.log.Info("background selector discovery: %d entity fields identified", len(rules.EntityFields))
	}
	s := schema.FromExtractionRules(targetURL, rules)
	// Use StoreIfNoAPI to avoid overwriting a higher-quality API schema
	if err := e.storeCSSSchemaIfNoAPI(bgCtx, targetURL, s, opts, async); err != nil {
		e.log.Debug("background selector discovery: cache store failed: %v", err)
	}
}

// extractFromDOM extracts structured data from browser-rendered HTML.
// Used in NoBrowser fallback path where synchronous LLM is acceptable.
func (e *Engine) extractFromDOM(ctx context.Context, targetURL, renderedHTML string, start time.Time) (*fetcher.Result, error) {
	page := htmlext.Extract(renderedHTML, targetURL)

	// Has JSON-LD — richest structured data, no LLM needed
	if len(page.JSONLD) > 0 {
		e.log.Info("rendered DOM has JSON-LD, extracting deterministically")
		structured := htmlext.ExtractStructured(renderedHTML, targetURL)
		return &fetcher.Result{
			URL:     targetURL,
			Source:  fetcher.SourceHTMLExtraction,
			Content: structured,
			Metadata: fetcher.ResultMetadata{
				Source:         fetcher.SourceHTMLExtract,
				TotalLatencyMs: time.Since(start).Milliseconds(),
			},
		}, nil
	}

	// No structured data — ask LLM to generate CSS selectors (synchronous in NoBrowser mode)
	if e.analyzer != nil {
		e.log.Info("no JSON-LD in DOM, asking LLM for CSS selectors")
		llmCtx, llmCancel := context.WithTimeout(ctx, llmTimeout)
		defer llmCancel()

		rules, err := e.analyzer.AnalyzeHTML(llmCtx, renderedHTML, targetURL)
		if err == nil && rules != nil && len(rules.EntityFields) > 0 {
			e.log.Info("LLM identified %d entity fields, applying extraction", len(rules.EntityFields))
			structured := htmlext.ApplyEntityExtraction(renderedHTML, rules)
			if len(structured) > 0 {
				if !e.noCache {
					s := schema.FromExtractionRules(targetURL, rules)
					if storeErr := e.storeCSSSchemaIfNoAPI(ctx, targetURL, s, FetchOpts{}, false); storeErr != nil {
						e.log.Debug("failed to cache extraction rules: %v", storeErr)
					}
				}
				return &fetcher.Result{
					URL:     targetURL,
					Source:  fetcher.SourceHTMLExtraction,
					Content: structured,
					Metadata: fetcher.ResultMetadata{
						Source:         fetcher.SourceHTMLExtractLLM,
						TotalLatencyMs: time.Since(start).Milliseconds(),
					},
				}, nil
			}
		}
		if err != nil {
			e.log.Debug("LLM HTML analysis failed: %v", err)
		}
	}

	// Fallback: deterministic extraction (OG + body text)
	structured := htmlext.ExtractStructured(renderedHTML, targetURL)
	return &fetcher.Result{
		URL:     targetURL,
		Source:  fetcher.SourceHTMLExtraction,
		Content: structured,
		Metadata: fetcher.ResultMetadata{
			Source:         fetcher.SourceHTMLExtract,
			TotalLatencyMs: time.Since(start).Milliseconds(),
		},
	}, nil
}

// fallbackNoBrowser handles the case where browser is unavailable or disabled.
// Tries LLM suggest for known endpoints, then falls back to probe HTML.
func (e *Engine) fallbackNoBrowser(ctx context.Context, targetURL, probeHTML string, start time.Time, opts FetchOpts) (*fetcher.Result, error) {
	if e.analyzer != nil {
		llmCtx, llmCancel := context.WithTimeout(ctx, llmTimeout)
		defer llmCancel()

		s, err := e.analyzer.Suggest(llmCtx, targetURL, "no browser available")
		if err == nil && s != nil && len(s.Endpoints) > 0 {
			validated, valErr := e.validateAndCache(ctx, s, targetURL, validationOptions{requireSemanticMatch: true}, opts)
			if valErr == nil {
				return e.fetchViaSchema(ctx, validated, targetURL, opts, start, "llm_suggest")
			}
		}
	}

	if probeHTML != "" {
		structured := htmlext.ExtractStructured(probeHTML, targetURL)
		return &fetcher.Result{
			URL:     targetURL,
			Source:  fetcher.SourceHTMLExtraction,
			Content: structured,
			Metadata: fetcher.ResultMetadata{
				Source:         fetcher.SourceHTMLExtract,
				TotalLatencyMs: time.Since(start).Milliseconds(),
			},
		}, nil
	}

	return nil, ErrNoEndpoints
}

// fetchViaSchema fetches data using a validated API schema.
func (e *Engine) fetchViaSchema(ctx context.Context, s *schema.Schema, targetURL string, opts FetchOpts, start time.Time, metadataSource string) (*fetcher.Result, error) {
	result, err := e.fetcher.Fetch(ctx, s, targetURL, opts.toFetchOpts())
	if err != nil {
		return nil, err
	}
	result.URL = targetURL
	result.Source = fetcher.SourceAPI
	result.Metadata.Source = metadataSource
	result.Metadata.CacheHit = false
	result.Metadata.TotalLatencyMs = time.Since(start).Milliseconds()
	return result, nil
}

// validateAndCache validates ALL candidate endpoints, keeps only the ones
// that return valid JSON, and caches the result.
func (e *Engine) validateAndCache(ctx context.Context, s *schema.Schema, targetURL string, opts validationOptions, fetchOpts FetchOpts) (*schema.Schema, error) {
	if len(s.Endpoints) == 0 {
		return nil, ErrNoEndpoints
	}

	validated := validateAllEndpoints(ctx, s, opts)

	if len(validated) == 0 {
		e.log.Info("none of %d candidate endpoints passed validation", len(s.Endpoints))
		return nil, fmt.Errorf("%w: all %d candidates failed validation", fetcher.ErrSchemaBroken, len(s.Endpoints))
	}

	e.log.Info("%d of %d candidate endpoints validated successfully", len(validated), len(s.Endpoints))
	validated[0].IsPrimary = true
	s.Endpoints = validated
	if s.Coverage == "" {
		s.Coverage = schema.SchemaCoveragePartial
	}
	s.SchemaType = schema.SchemaTypeAPI

	if !e.noCache {
		if err := e.storeAPISchema(ctx, targetURL, s, fetchOpts, false); err != nil {
			return nil, fmt.Errorf("cache store failed: %w", err)
		}
	}

	return s, nil
}

// applyExtractionRules applies cached CSS selectors to extract content.
// If selectors no longer match (site redesign), invalidates cache and
// re-runs the pipeline.
func (e *Engine) applyExtractionRules(ctx context.Context, targetURL string, opts FetchOpts, s *schema.Schema) (*fetcher.Result, error) {
	start := time.Now()

	var rawHTML string
	var err error
	if s.RequiresStealth {
		stealthClient := httpclient.NewStealthOrFallback(httpclient.Options{
			ProxyURL: opts.ProxyURL,
			Insecure: opts.Insecure,
		})
		rawHTML, err = htmlext.FetchHTMLWithClient(ctx, stealthClient, targetURL)
	} else {
		rawHTML, err = htmlext.FetchHTML(ctx, targetURL, opts.ProxyURL, opts.Insecure)
	}
	if err != nil {
		return nil, fmt.Errorf("HTML fetch failed: %w", err)
	}

	// __NEXT_DATA__ strategy: re-extract from SSR HTML deterministically
	if s.IsNextDataSchema() {
		structured := htmlext.ExtractStructured(rawHTML, targetURL)
		if len(structured) <= 2 { // only title+type, no real __NEXT_DATA__ payload
			e.log.Info("cached __NEXT_DATA__ extraction returned empty, invalidating")
			if invErr := e.cache.Invalidate(ctx, s.ID); invErr != nil {
				e.log.Debug("cache invalidation failed: %v", invErr)
			}
			return e.pipeline(ctx, targetURL, opts)
		}

		// Apply named extraction paths if present — returns only the
		// targeted sub-trees instead of the full pageProps blob.
		if s.ExtractionRules.HasNextDataPaths() {
			if targeted := htmlext.ApplyNextDataPaths(structured, s.ExtractionRules.NextDataPaths); targeted != nil {
				e.log.Info("applied %d NextDataPaths, returning targeted extraction", len(targeted))
				structured = targeted
			}
		}

		return &fetcher.Result{
			URL:     targetURL,
			Source:  fetcher.SourceHTMLExtraction,
			Content: structured,
			Metadata: fetcher.ResultMetadata{
				SchemaID:       s.ID,
				SchemaVersion:  s.Version,
				Source:         fetcher.SourceHTMLExtractCached,
				CacheHit:       true,
				TotalLatencyMs: time.Since(start).Milliseconds(),
			},
		}, nil
	}

	// Route to the correct extraction strategy
	hasTableRows := s.ExtractionRules.TableRows != nil && len(s.ExtractionRules.TableRows.Fields) > 0
	hasEntityFields := len(s.ExtractionRules.EntityFields) > 0

	if hasTableRows || hasEntityFields {
		result := htmlext.ApplyEntityExtractionWithValidation(rawHTML, s.ExtractionRules)
		if len(result.Data) == 0 {
			e.log.Info("cached entity extraction returned empty, re-discovering")
			if invErr := e.cache.Invalidate(ctx, s.ID); invErr != nil {
				e.log.Debug("cache invalidation failed: %v", invErr)
			}
			return e.pipeline(ctx, targetURL, opts)
		}

		// If required fields are missing, trigger async re-discovery
		// but still return the partial result as a degraded response
		if len(result.MissingRequired) > 0 {
			e.log.Info("required fields missing: %v, triggering async re-discovery", result.MissingRequired)
			html := rawHTML
			e.startBackground(func() {
				e.discoverSelectorsFromDOM(html, targetURL, opts, true)
			})
		}

		return &fetcher.Result{
			URL:     targetURL,
			Source:  fetcher.SourceHTMLExtraction,
			Content: result.Data,
			Metadata: fetcher.ResultMetadata{
				SchemaID:       s.ID,
				SchemaVersion:  s.Version,
				Source:         fetcher.SourceHTMLExtractCached,
				CacheHit:       true,
				TotalLatencyMs: time.Since(start).Milliseconds(),
			},
		}, nil
	}

	clean, extractErr := htmlext.ExtractWithRules(rawHTML, targetURL, s.ExtractionRules)
	if extractErr != nil || clean == nil || clean.Content == "" {
		e.log.Info("cached selectors no longer match, re-discovering")
		if invErr := e.cache.Invalidate(ctx, s.ID); invErr != nil {
			e.log.Debug("cache invalidation failed: %v", invErr)
		}
		return e.pipeline(ctx, targetURL, opts)
	}

	return &fetcher.Result{
		URL:     targetURL,
		Source:  fetcher.SourceHTMLExtraction,
		Content: clean,
		Metadata: fetcher.ResultMetadata{
			SchemaID:       s.ID,
			SchemaVersion:  s.Version,
			Source:         fetcher.SourceHTMLExtractCached,
			CacheHit:       true,
			TotalLatencyMs: time.Since(start).Milliseconds(),
		},
	}, nil
}

// extractBestHARResponse finds the most relevant API response from captured HAR
// entries. It merges ALL `included` arrays from every JSON response into a single
// unified structure, and picks the largest `data` payload as the primary result.
//
// Why merge: LinkedIn Voyager and similar SPAs make many GraphQL calls per page,
// each returning a slice of the data. The old "pick one best" heuristic was
// non-deterministic — it would pick different responses across runs, giving
// inconsistent data. Merging all `included` arrays gives a unified view.
func extractBestHARResponse(har *browser.HARLog, targetURL string) any {
	if har == nil || len(har.Entries) == 0 {
		return nil
	}

	// Extract path segments from the target URL for relevance matching
	parsed, _ := url.Parse(targetURL)
	pathSegments := strings.Split(strings.Trim(parsed.Path, "/"), "/")

	var bestEntry *browser.HAREntry
	var bestScore int
	var mergedIncluded []any
	seenUrns := make(map[string]bool)

	for i := range har.Entries {
		entry := &har.Entries[i]
		if entry.Response.Status != 200 || entry.Response.Body == "" {
			continue
		}

		// Parse the JSON body
		var parsed any
		if err := json.Unmarshal([]byte(entry.Response.Body), &parsed); err != nil {
			continue
		}

		// If the response has an `included` array, add its entries to merged set
		if m, ok := parsed.(map[string]any); ok {
			if inc, ok := m["included"].([]any); ok {
				for _, item := range inc {
					if entityMap, ok := item.(map[string]any); ok {
						if urn, ok := entityMap["entityUrn"].(string); ok {
							if !seenUrns[urn] {
								seenUrns[urn] = true
								mergedIncluded = append(mergedIncluded, item)
							}
						} else {
							// No URN — just append (can't dedupe)
							mergedIncluded = append(mergedIncluded, item)
						}
					}
				}
			}
		}

		// Score for "best data payload" selection
		score := len(entry.Response.Body)
		urlLower := strings.ToLower(entry.Request.URL)
		for _, seg := range pathSegments {
			if seg != "" && len(seg) > 2 && strings.Contains(urlLower, strings.ToLower(seg)) {
				score += 50000
			}
		}
		if strings.Contains(urlLower, "graphql") || strings.Contains(urlLower, "/api/") {
			score += 10000
		}
		if strings.Contains(urlLower, "setting") || strings.Contains(urlLower, "messaging") ||
			strings.Contains(urlLower, "notification") || strings.Contains(urlLower, "policy") {
			score -= 30000
		}

		if score > bestScore {
			bestScore = score
			bestEntry = entry
		}
	}

	if bestEntry == nil && len(mergedIncluded) == 0 {
		return nil
	}

	// Parse the best entry as the primary payload
	var result any
	if bestEntry != nil {
		if err := json.Unmarshal([]byte(bestEntry.Response.Body), &result); err != nil {
			return nil
		}
	}

	// Overlay the merged `included` array so we have a complete view
	if len(mergedIncluded) > 0 {
		if m, ok := result.(map[string]any); ok {
			m["included"] = mergedIncluded
			result = m
		} else {
			result = map[string]any{"included": mergedIncluded}
		}
	}

	return result
}

// contentIsUseful returns true if the extracted page has enough semantic
// content to be worth returning without escalating to browser.
// Requires structural signals AND sufficient body text.
// Structural signals: <h1>, <article>, JSON-LD, or 20+ links (listing pages).
func contentIsUseful(page htmlext.PageContent) bool {
	hasH1 := false
	for _, h := range page.Headings {
		if h.Level == 1 {
			hasH1 = true
			break
		}
	}
	hasStructure := hasH1 || page.HasArticle || len(page.JSONLD) > 0 || len(page.Links) >= 20
	return hasStructure && len(page.BodyText) > 500
}

// Close releases resources held by the engine, specifically the browser.
// Waits for background goroutines to complete (with timeout) before closing.
func (e *Engine) Close() error {
	// Wait for background work with timeout
	done := make(chan struct{})
	go func() { e.bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		e.log.Info("background work timed out during close")
	}

	if e.browser == nil {
		return nil
	}
	return e.browser.Close()
}
