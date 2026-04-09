package engine

import (
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/browser"
	"github.com/hermai-ai/hermai-cli/pkg/fetcher"
)

// SchemaDiscovery describes a brand-new schema persisted during a fetch.
type SchemaDiscovery struct {
	SchemaID   string
	SchemaType string
	Async      bool
}

// FetchOpts configures the behavior of an engine fetch operation.
type FetchOpts struct {
	ProxyURL            string
	Raw                 bool
	HeaderOverrides     map[string]string
	RetryOnBrokenSchema bool
	BrowserPath         string
	BrowserTimeout      time.Duration
	WaitAfterLoad       time.Duration
	NoBrowser           bool // skip browser, use probe + LLM only
	NoCache             bool // skip cache read/write, always fresh discovery
	Insecure            bool // skip TLS certificate verification
	CatalogMode         bool // continue enriching API coverage for catalog output
	Stealth             bool // use TLS+HTTP/2 fingerprinting (set automatically from cached schema)
	Cookies             []string // name=value cookies to inject into browser session
	OnSchemaDiscovered  func(SchemaDiscovery)
}

func (o FetchOpts) toCaptureOpts() browser.CaptureOpts {
	return browser.CaptureOpts{
		ProxyURL:      o.ProxyURL,
		BrowserPath:   o.BrowserPath,
		Timeout:       o.BrowserTimeout,
		WaitAfterLoad: o.WaitAfterLoad,
		Cookies:       o.Cookies,
	}
}

func (o FetchOpts) toFetchOpts() fetcher.FetchOpts {
	return fetcher.FetchOpts{
		ProxyURL:        o.ProxyURL,
		Raw:             o.Raw,
		HeaderOverrides: o.HeaderOverrides,
		Insecure:        o.Insecure,
		Stealth:         o.Stealth,
		Cookies:         o.Cookies,
	}
}
