package fetcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// ErrSchemaBroken is returned when an API endpoint returns a non-2xx response,
// indicating the cached schema no longer works and re-discovery may be needed.
var ErrSchemaBroken = errors.New("hermai: cached schema no longer works")

// ErrTransient wraps transient HTTP errors (429, 502, 503, 504) that are retryable.
var ErrTransient = errors.New("hermai: transient error")

// IsTransient returns true if the error is a retryable transient error.
func IsTransient(err error) bool {
	return errors.Is(err, ErrTransient)
}

// TransientStatusCode returns true for HTTP status codes that indicate
// a transient/retryable failure.
func TransientStatusCode(code int) bool {
	switch code {
	case 429, 502, 503, 504:
		return true
	default:
		return false
	}
}

// WrapTransient wraps an error as transient if the status code indicates it.
func WrapTransient(endpoint string, statusCode int) error {
	if TransientStatusCode(statusCode) {
		return fmt.Errorf("%w: %s returned HTTP %d", ErrTransient, endpoint, statusCode)
	}
	return fmt.Errorf("%w: %s returned HTTP %d", ErrSchemaBroken, endpoint, statusCode)
}

// Service defines the interface for fetching data using a discovered schema.
type Service interface {
	Fetch(ctx context.Context, s *schema.Schema, targetURL string, opts FetchOpts) (*Result, error)
}

// FetchOpts configures the behavior of a fetch operation.
type FetchOpts struct {
	ProxyURL        string
	Raw             bool
	HeaderOverrides map[string]string
	Insecure        bool     // skip TLS certificate verification
	Stealth         bool     // use TLS+HTTP/2 fingerprinting to bypass anti-bot
	Cookies         []string // name=value cookies to include in requests
}

// Top-level source values for Result.Source.
const (
	SourceAPI            = "api"
	SourceHTMLExtraction = "html_extraction"
)

// Fine-grained source constants for ResultMetadata.Source.
const (
	SourceHTMLExtract       = "html_extract"       // raw HTML extraction, no LLM
	SourceHTMLExtractLLM    = "html_extract_llm"   // first visit, LLM identified selectors
	SourceHTMLExtractCached = "html_extract_cached" // cached CSS selectors applied
)

// API schema status constants for ResultMetadata.APISchemaStatus.
const (
	SchemaStatusDiscovering = "discovering"
	SchemaStatusCached      = "cached"
	SchemaStatusFailed      = "failed"
)

// Result contains the fetched data and metadata.
//
// When Source is "api", Data contains the raw API JSON response.
// When Source is "html_extraction", Content contains structured HTML extraction.
type Result struct {
	URL      string         `json:"url"`
	Source   string         `json:"source"`
	Content  any            `json:"content,omitempty"` // populated for html_extraction
	Data     any            `json:"data,omitempty"`    // populated for api
	Raw      []RawResponse  `json:"raw,omitempty"`
	Metadata ResultMetadata `json:"metadata"`
}

// Payload returns the meaningful data payload regardless of source.
// For API results, returns Data. For HTML extraction, returns Content.
func (r *Result) Payload() any {
	if r.Source == SourceAPI {
		return r.Data
	}
	return r.Content
}

// DataMap returns Data as a map[string]any, or nil if Data is not an object.
func (r *Result) DataMap() map[string]any {
	if m, ok := r.Data.(map[string]any); ok {
		return m
	}
	return nil
}

// RawResponse captures the full HTTP response from a single endpoint call.
type RawResponse struct {
	EndpointName string            `json:"endpoint_name"`
	StatusCode   int               `json:"status_code"`
	Headers      map[string]string `json:"headers"`
	Body         json.RawMessage   `json:"body"`
}

// ResultMetadata provides information about how the result was obtained.
type ResultMetadata struct {
	SchemaID        string `json:"schema_id,omitempty"`
	SchemaVersion   int    `json:"schema_version,omitempty"`
	Source          string `json:"source"`
	CacheHit        bool   `json:"cache_hit"`
	EndpointsCalled int    `json:"endpoints_called,omitempty"`
	TotalLatencyMs  int64  `json:"total_latency_ms"`
	APISchemaStatus string `json:"api_schema_status,omitempty"`
}
