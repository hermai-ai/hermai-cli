package cache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// FileCache implements Service by storing schemas as JSON files on disk.
// Storage layout: {baseDir}/{domain}/{schemaID}.json
type FileCache struct {
	baseDir string
	ttl     time.Duration
}

// Compile-time check that FileCache implements Service.
var _ Service = (*FileCache)(nil)

// NewFileCache creates a FileCache that stores schemas under baseDir
// and considers entries stale after ttl has elapsed since their CreatedAt.
func NewFileCache(baseDir string, ttl time.Duration) *FileCache {
	return &FileCache{
		baseDir: baseDir,
		ttl:     ttl,
	}
}

// loadSchemas reads all non-expired cached schemas for a domain.
func (fc *FileCache) loadSchemas(targetURL string) ([]schema.Schema, error) {
	domain, err := schema.ExtractDomain(targetURL)
	if err != nil {
		return nil, fmt.Errorf("cache lookup: %w", err)
	}

	domainDir, err := fc.safeDomainDir(domain)
	if err != nil {
		return nil, fmt.Errorf("cache lookup: %w", err)
	}

	entries, err := os.ReadDir(domainDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cache lookup: reading domain dir: %w", err)
	}

	schemas := make([]schema.Schema, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(domainDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("cache lookup: reading schema file %s: %w", entry.Name(), err)
		}

		s, err := schema.FromJSON(data)
		if err != nil {
			return nil, fmt.Errorf("cache lookup: parsing schema file %s: %w", entry.Name(), err)
		}

		if time.Since(s.CreatedAt) > fc.ttl {
			os.Remove(filepath.Join(domainDir, entry.Name()))
			continue
		}

		schemas = append(schemas, *s)
	}

	return schemas, nil
}

// Lookup returns the best matching cached schema. API schemas take priority
// over CSS selector schemas.
func (fc *FileCache) Lookup(ctx context.Context, targetURL string) (*schema.Schema, error) {
	apiSchema, cssSchema, err := fc.LookupAll(ctx, targetURL)
	if err != nil {
		return nil, err
	}
	if apiSchema != nil {
		return apiSchema, nil
	}
	return cssSchema, nil
}

// LookupAll returns both the API schema and CSS schema for a URL, if they exist.
// API schema is always returned separately from CSS schema so the caller can
// implement priority logic.
func (fc *FileCache) LookupAll(_ context.Context, targetURL string) (*schema.Schema, *schema.Schema, error) {
	all, err := fc.loadSchemas(targetURL)
	if err != nil {
		return nil, nil, err
	}

	var apiSchemas, cssSchemas []schema.Schema
	for _, s := range all {
		if s.IsCSSSchema() {
			cssSchemas = append(cssSchemas, s)
		} else {
			// API schemas, legacy schemas, and untyped schemas
			// all go into the API bucket (default)
			apiSchemas = append(apiSchemas, s)
		}
	}

	var apiMatch, cssMatch *schema.Schema
	if m := schema.MatchURL(targetURL, apiSchemas); m != nil {
		apiMatch = m
	}
	if m := schema.MatchURL(targetURL, cssSchemas); m != nil {
		cssMatch = m
	}

	return apiMatch, cssMatch, nil
}

// Store writes a schema as indented JSON to {baseDir}/{domain}/{schemaID}.json,
// creating directories as needed.
func (fc *FileCache) Store(_ context.Context, s *schema.Schema) error {
	domainDir, err := fc.safeDomainDir(s.Domain)
	if err != nil {
		return fmt.Errorf("cache store: %w", err)
	}

	if err := os.MkdirAll(domainDir, 0o755); err != nil {
		return fmt.Errorf("cache store: creating directory: %w", err)
	}

	data, err := s.ToJSON()
	if err != nil {
		return fmt.Errorf("cache store: serializing schema: %w", err)
	}

	filePath := filepath.Join(domainDir, s.ID+".json")

	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return fmt.Errorf("cache store: writing file: %w", err)
	}

	return nil
}

// StoreIfNoAPI stores a CSS selector schema only if no API schema already
// exists for the same URL. Prevents CSS selectors from overwriting API schemas.
func (fc *FileCache) StoreIfNoAPI(ctx context.Context, s *schema.Schema) error {
	apiSchema, _, err := fc.LookupAll(ctx, s.DiscoveredFrom)
	if err != nil {
		return fmt.Errorf("cache store-if-no-api: lookup failed: %w", err)
	}
	if apiSchema != nil {
		return nil // API schema exists, skip CSS storage
	}
	return fc.Store(ctx, s)
}

// Invalidate removes {schemaID}.json from all domain directories.
// Returns nil if the schema file is not found.
func (fc *FileCache) Invalidate(_ context.Context, schemaID string) error {
	entries, err := os.ReadDir(fc.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cache invalidate: reading base dir: %w", err)
	}

	fileName := schemaID + ".json"

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		filePath := filepath.Join(fc.baseDir, entry.Name(), fileName)

		err := os.Remove(filePath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cache invalidate: removing %s: %w", filePath, err)
		}
	}

	return nil
}

// safeDomainDir returns a validated path under baseDir for the given domain.
// Prevents path traversal attacks (e.g., domain="../../etc").
func (fc *FileCache) safeDomainDir(domain string) (string, error) {
	if strings.ContainsAny(domain, `/\`) || strings.HasPrefix(domain, ".") {
		return "", fmt.Errorf("invalid domain: %q", domain)
	}
	dir := filepath.Join(fc.baseDir, domain)
	absBase, err := filepath.Abs(fc.baseDir)
	if err != nil {
		return "", fmt.Errorf("cannot resolve base dir: %w", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("cannot resolve domain dir: %w", err)
	}
	if !strings.HasPrefix(absDir, absBase+string(filepath.Separator)) && absDir != absBase {
		return "", fmt.Errorf("domain path escapes cache directory: %q", domain)
	}
	return dir, nil
}
