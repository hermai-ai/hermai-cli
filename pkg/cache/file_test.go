package cache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

func newTestSchema(id, domain, urlPattern string, createdAt time.Time) *schema.Schema {
	return &schema.Schema{
		ID:             id,
		Domain:         domain,
		URLPattern:     urlPattern,
		Version:        1,
		CreatedAt:      createdAt,
		DiscoveredFrom: "https://" + domain + "/test",
		Endpoints:      []schema.Endpoint{},
	}
}

func TestStoreAndLookup(t *testing.T) {
	dir := t.TempDir()
	fc := NewFileCache(dir, 1*time.Hour)
	ctx := context.Background()

	s := newTestSchema("abc123", "example.com", "/api/products/{}", time.Now())

	if err := fc.Store(ctx, s); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	result, err := fc.Lookup(ctx, "https://example.com/api/products/42")
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}

	if result == nil {
		t.Fatal("expected a match, got nil")
	}

	if result.ID != "abc123" {
		t.Errorf("expected schema ID abc123, got %s", result.ID)
	}
}

func TestLookupMiss(t *testing.T) {
	dir := t.TempDir()
	fc := NewFileCache(dir, 1*time.Hour)
	ctx := context.Background()

	result, err := fc.Lookup(ctx, "https://unknown.com/api/nothing")
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}

	if result != nil {
		t.Errorf("expected nil for unknown URL, got %+v", result)
	}
}

func TestInvalidate(t *testing.T) {
	dir := t.TempDir()
	fc := NewFileCache(dir, 1*time.Hour)
	ctx := context.Background()

	s := newTestSchema("def456", "example.com", "/api/users/{}", time.Now())

	if err := fc.Store(ctx, s); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Verify it exists first
	result, err := fc.Lookup(ctx, "https://example.com/api/users/99")
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected match before invalidation")
	}

	// Invalidate
	if err := fc.Invalidate(ctx, "def456"); err != nil {
		t.Fatalf("Invalidate failed: %v", err)
	}

	// Verify gone
	result, err = fc.Lookup(ctx, "https://example.com/api/users/99")
	if err != nil {
		t.Fatalf("Lookup after invalidate failed: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil after invalidation, got %+v", result)
	}
}

func TestInvalidateNotFound(t *testing.T) {
	dir := t.TempDir()
	fc := NewFileCache(dir, 1*time.Hour)
	ctx := context.Background()

	err := fc.Invalidate(ctx, "nonexistent")
	if err != nil {
		t.Errorf("Invalidate of nonexistent schema should return nil, got %v", err)
	}
}

func TestTTLExpiry(t *testing.T) {
	dir := t.TempDir()
	fc := NewFileCache(dir, 1*time.Minute)
	ctx := context.Background()

	oldTime := time.Now().Add(-2 * time.Minute)
	s := newTestSchema("expired1", "example.com", "/api/old/{}", oldTime)

	if err := fc.Store(ctx, s); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	result, err := fc.Lookup(ctx, "https://example.com/api/old/123")
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}

	if result != nil {
		t.Errorf("expected nil for expired schema, got %+v", result)
	}
}

func TestStoreCreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	fc := NewFileCache(dir, 1*time.Hour)
	ctx := context.Background()

	s := newTestSchema("dir123", "newdomain.io", "/api/items/{}", time.Now())

	if err := fc.Store(ctx, s); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	expectedDir := filepath.Join(dir, "newdomain.io")
	info, err := os.Stat(expectedDir)
	if err != nil {
		t.Fatalf("expected domain directory to exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected domain path to be a directory")
	}

	expectedFile := filepath.Join(expectedDir, "dir123.json")
	if _, err := os.Stat(expectedFile); err != nil {
		t.Fatalf("expected schema file to exist: %v", err)
	}
}

func TestStoreOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	fc := NewFileCache(dir, 1*time.Hour)
	ctx := context.Background()

	s1 := newTestSchema("same1", "example.com", "/api/v1/{}", time.Now())
	s1.Version = 1

	if err := fc.Store(ctx, s1); err != nil {
		t.Fatalf("Store v1 failed: %v", err)
	}

	s2 := newTestSchema("same1", "example.com", "/api/v1/{}", time.Now())
	s2.Version = 2

	if err := fc.Store(ctx, s2); err != nil {
		t.Fatalf("Store v2 failed: %v", err)
	}

	result, err := fc.Lookup(ctx, "https://example.com/api/v1/42")
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected a match, got nil")
	}
	if result.Version != 2 {
		t.Errorf("expected version 2, got %d", result.Version)
	}
}
