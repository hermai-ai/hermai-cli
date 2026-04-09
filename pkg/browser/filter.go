package browser

import (
	"strings"
)

// allowedContentTypes lists content-type substrings that indicate API traffic.
var allowedContentTypes = []string{
	"application/json",
	"application/graphql",
	"application/graphql+json",
	"text/json",
}

// noisePatterns lists URL substrings that indicate analytics/tracking noise.
var noisePatterns = []string{
	"analytics",
	"tracking",
	"/log/",
	"/logging",
	"beacon",
	"pixel",
	"healthz",
	"metrics",
	"telemetry",
	"sentry",
	"datadog",
	"segment",
	"gtag",
	"fbevents",
	"hotjar",
	"mixpanel",
	"amplitude",
	"sensorcollect",
	"protechts.net",
	"/realtime/",
	"presencestatus",
}

// FilterHAR returns a new HARLog with noise removed. The input is not mutated.
//
// Stage 1: Keep only entries whose response content-type matches an allowed API type.
// Stage 2: Drop entries whose URL contains known analytics/tracking patterns.
// Stage 3: Deduplicate by Method + URL, keeping the first occurrence.
func FilterHAR(har *HARLog) *HARLog {
	filtered := filterByContentType(har.Entries)
	filtered = filterByNoise(filtered)
	filtered = deduplicateEntries(filtered)

	return &HARLog{Entries: filtered}
}

func filterByContentType(entries []HAREntry) []HAREntry {
	var result []HAREntry

	for _, entry := range entries {
		ct := strings.ToLower(entry.Response.ContentType)
		if matchesContentType(ct) {
			result = append(result, entry)
		}
	}

	if result == nil {
		return []HAREntry{}
	}

	return result
}

func matchesContentType(ct string) bool {
	// Use the same broad check as isJSONContentType to catch custom vendor
	// types like LinkedIn's "application/vnd.linkedin.normalized+json+2.1".
	return isJSONContentType(ct)
}

func filterByNoise(entries []HAREntry) []HAREntry {
	var result []HAREntry

	for _, entry := range entries {
		urlLower := strings.ToLower(entry.Request.URL)
		if !isNoiseURL(urlLower) {
			result = append(result, entry)
		}
	}

	if result == nil {
		return []HAREntry{}
	}

	return result
}

func isNoiseURL(urlLower string) bool {
	for _, pattern := range noisePatterns {
		if strings.Contains(urlLower, pattern) {
			return true
		}
	}

	return false
}

func deduplicateEntries(entries []HAREntry) []HAREntry {
	seen := make(map[string]bool)
	var result []HAREntry

	for _, entry := range entries {
		key := entry.Request.Method + " " + entry.Request.URL
		if !seen[key] {
			seen[key] = true
			result = append(result, entry)
		}
	}

	if result == nil {
		return []HAREntry{}
	}

	return result
}
