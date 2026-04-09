package schema

import (
	"net/url"
	"strings"
)

// MatchURL finds the best matching schema for a given URL.
// It normalizes the input URL path, then matches against each schema's URLPattern.
// Returns the most specific match, or nil if no schema matches.
func MatchURL(rawURL string, schemas []Schema) *Schema {
	if len(schemas) == 0 {
		return nil
	}

	path := extractPath(rawURL)
	normalized := NormalizePathStructure(path)

	var bestMatch *Schema
	bestScore := -1

	for i := range schemas {
		if patternMatches(normalized, schemas[i].URLPattern) {
			score := specificity(schemas[i].URLPattern)
			if score > bestScore {
				bestScore = score
				bestMatch = &schemas[i]
			}
		}
	}

	return bestMatch
}

// extractPath pulls the path portion from a URL string.
func extractPath(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Path == "" {
		return "/"
	}
	return parsed.Path
}

// patternMatches checks if a normalized path matches a URL pattern
// by comparing segments. A "{}" in the pattern matches any segment.
func patternMatches(normalizedPath, pattern string) bool {
	pathSegs := splitSegments(normalizedPath)
	patternSegs := splitSegments(pattern)

	if len(pathSegs) != len(patternSegs) {
		return false
	}

	for i := range pathSegs {
		if patternSegs[i] == "{}" {
			continue
		}
		if pathSegs[i] != patternSegs[i] {
			return false
		}
	}

	return true
}

// specificity scores a URL pattern. Static segments score 10 points,
// wildcard segments ({}) score 1 point. Higher total = more specific.
func specificity(pattern string) int {
	segments := splitSegments(pattern)
	score := 0

	for _, seg := range segments {
		if seg == "{}" {
			score += 1
		} else {
			score += 10
		}
	}

	return score
}

// splitSegments splits a path into non-empty segments.
func splitSegments(path string) []string {
	parts := strings.Split(path, "/")
	segments := make([]string, 0, len(parts))

	for _, p := range parts {
		if p != "" {
			segments = append(segments, p)
		}
	}

	return segments
}
