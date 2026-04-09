package cache

import (
	"context"

	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// Service defines the contract for schema caching.
// Implementations store and retrieve discovered API schemas so that
// repeat fetches can skip LLM analysis.
type Service interface {
	// Lookup finds the best matching cached schema for the given URL.
	// Returns nil, nil when no match is found.
	Lookup(ctx context.Context, targetURL string) (*schema.Schema, error)

	// LookupAll returns all matching cached schemas for the given URL,
	// separated by type. Returns (apiSchema, cssSchema, error).
	// Either may be nil if no schema of that type is cached.
	LookupAll(ctx context.Context, targetURL string) (apiSchema *schema.Schema, cssSchema *schema.Schema, err error)

	// Store persists a schema to the cache.
	Store(ctx context.Context, s *schema.Schema) error

	// StoreIfNoAPI stores a CSS selector schema only if no API schema
	// already exists for the same URL pattern. Prevents CSS from
	// overwriting a higher-quality API schema.
	StoreIfNoAPI(ctx context.Context, s *schema.Schema) error

	// Invalidate removes a cached schema by its ID.
	// Returns nil if the schema is not found.
	Invalidate(ctx context.Context, schemaID string) error
}
