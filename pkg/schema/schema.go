package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Schema type constants distinguish API schemas from CSS selector schemas.
const (
	SchemaTypeAPI = "api"
	SchemaTypeCSS = "css_selector"

	SchemaCoveragePartial  = "partial"
	SchemaCoverageComplete = "complete"
)

// Schema represents a discovered API schema for a domain's URL pattern.
type Schema struct {
	ID string `json:"id"`
	// Site is the canonical domain key the registry validator reads.
	// Set this on every new schema. Domain is retained for backward
	// compatibility with pre-2026-04 schemas that used it as the site
	// key, but any new code should populate Site. The hermai-api
	// validator accepts either and normalizes to Site.
	Site            string           `json:"site,omitempty"`
	Domain          string           `json:"domain,omitempty"`
	URLPattern      string           `json:"url_pattern"`
	SchemaType      string           `json:"schema_type,omitempty"` // "api" or "css_selector"
	Coverage        string           `json:"coverage,omitempty"`    // partial or complete
	Version         int              `json:"version"`
	CreatedAt       time.Time        `json:"created_at"`
	DiscoveredFrom  string           `json:"discovered_from"`
	Endpoints       []Endpoint       `json:"endpoints"`
	Actions         []Action         `json:"actions,omitempty"`
	ExtractionRules *ExtractionRules `json:"extraction_rules,omitempty"`
	Session         *SessionConfig   `json:"session,omitempty"`
	Runtime         *Runtime         `json:"runtime,omitempty"`
	RequiresStealth bool             `json:"requires_stealth,omitempty"`
}

// IsAPISchema returns true if this schema contains API endpoints.
// Handles legacy schemas without SchemaType by checking Endpoints.
func (s Schema) IsAPISchema() bool {
	if s.SchemaType == SchemaTypeAPI {
		return true
	}
	if s.SchemaType == "" && len(s.Endpoints) > 0 && s.ExtractionRules == nil {
		return true
	}
	return false
}

// IsCSSSchema returns true if this schema contains CSS extraction rules.
func (s Schema) IsCSSSchema() bool {
	if s.SchemaType == SchemaTypeCSS {
		return true
	}
	return s.SchemaType == "" && s.ExtractionRules != nil
}

// SessionConfig describes how to bootstrap a session before fetching data.
// Some APIs require cookies or tokens obtained from an initial page load.
// The fetcher hits the BootstrapURL first, extracts the specified cookies
// and headers, then carries them into subsequent data API calls.
type SessionConfig struct {
	// BootstrapURL is hit before data fetches to obtain session cookies/tokens.
	BootstrapURL    string `json:"bootstrap_url"`
	BootstrapMethod string `json:"bootstrap_method"` // GET or POST
	// CaptureHeaders lists response headers to extract and forward (e.g. X-CSRF-Token).
	CaptureHeaders []string `json:"capture_headers,omitempty"`
	// CaptureCookies lists cookie names to extract from Set-Cookie and forward.
	CaptureCookies []string `json:"capture_cookies,omitempty"`
	// StaticHeaders are fixed headers that don't change between sessions.
	StaticHeaders map[string]string `json:"static_headers,omitempty"`
	// ClearanceCookies are cookies obtained from a browser-based anti-bot
	// challenge solve. Persisted so subsequent requests can skip the browser.
	ClearanceCookies map[string]string `json:"clearance_cookies,omitempty"`
}

// Endpoint represents a single API endpoint within a schema.
type Endpoint struct {
	Name            string            `json:"name"`
	Description     string            `json:"description,omitempty"`
	Method          string            `json:"method"`
	URLTemplate     string            `json:"url_template"`
	Headers         map[string]string `json:"headers"`
	QueryParams     []Param           `json:"query_params,omitempty"`
	Body            *BodyTemplate     `json:"body,omitempty"`
	Variables       []Variable        `json:"variables"`
	IsPrimary       bool              `json:"is_primary"`
	Confidence      float64           `json:"confidence,omitempty"`
	ResponseMapping map[string]string `json:"response_mapping,omitempty"`
	ResponseSchema  *ResponseSchema   `json:"response_schema,omitempty"`
}

// ResponseSchema describes the structure of an endpoint's JSON response.
// Inferred automatically during validation by sampling the response body.
type ResponseSchema struct {
	Type   string        `json:"type"`             // "object", "array", "string", "number", "boolean"
	Fields []FieldSchema `json:"fields,omitempty"` // for object type
	Items  *FieldSchema  `json:"items,omitempty"`  // for array type: schema of each element
}

// FieldSchema describes a single field within a response object.
type FieldSchema struct {
	Name   string        `json:"name"`
	Type   string        `json:"type"`             // "string", "number", "boolean", "object", "array", "null"
	Fields []FieldSchema `json:"fields,omitempty"` // for nested objects
	Items  *FieldSchema  `json:"items,omitempty"`  // for arrays: schema of each element
}

// Variable represents a dynamic variable in a URL template.
type Variable struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Pattern string `json:"pattern"`
}

// Param represents a query parameter.
type Param struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Required bool   `json:"required"`
}

// BodyTemplate represents a request body template.
type BodyTemplate struct {
	ContentType string `json:"content_type"`
	Template    string `json:"template"`
}

var (
	numericPattern      = regexp.MustCompile(`^\d+$`)
	uuidPattern         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	longAlphanumPattern = regexp.MustCompile(`^[a-zA-Z0-9]{16,}$`)
)

// isDynamicSegment returns true if the segment looks like a dynamic ID.
func isDynamicSegment(segment string) bool {
	if numericPattern.MatchString(segment) {
		return true
	}
	if uuidPattern.MatchString(segment) {
		return true
	}
	if longAlphanumPattern.MatchString(segment) {
		return true
	}
	return false
}

// NormalizePathStructure replaces dynamic URL segments with {} placeholders
// and strips query strings.
func NormalizePathStructure(path string) string {
	pathOnly := strings.SplitN(path, "?", 2)[0]

	if pathOnly == "/" {
		return "/"
	}

	segments := strings.Split(strings.Trim(pathOnly, "/"), "/")
	normalized := make([]string, len(segments))

	for i, seg := range segments {
		if isDynamicSegment(seg) {
			normalized[i] = "{}"
		} else {
			normalized[i] = seg
		}
	}

	return "/" + strings.Join(normalized, "/")
}

// GenerateID produces a deterministic ID from domain and raw path.
// Uses SHA256 of "domain:normalizedPath", returns first 8 bytes hex-encoded.
func GenerateID(domain, rawPath string) string {
	normalized := NormalizePathStructure(rawPath)
	input := domain + ":" + normalized
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:8])
}

// ExtractDomain extracts the host from a raw URL string.
func ExtractDomain(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL: %w", err)
	}

	if parsed.Host == "" {
		return "", fmt.Errorf("no host found in URL: %s", rawURL)
	}

	return parsed.Host, nil
}

// ToJSON serializes the schema to indented JSON bytes.
func (s Schema) ToJSON() ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal schema: %w", err)
	}
	return data, nil
}

// FromJSON deserializes JSON bytes into a Schema.
func FromJSON(data []byte) (*Schema, error) {
	var s Schema
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to unmarshal schema: %w", err)
	}
	return &s, nil
}
