package engine

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

type targetSignals struct {
	rawURL       string
	path         string
	segments     []string
	lastSegment  string
	lastTokens   []string
	pathPair     string
	queryIDs     map[string]bool
	signalsExist bool
}

func buildTargetSignals(targetURL string) targetSignals {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return targetSignals{}
	}

	segments := splitSemanticSegments(parsed.Path)
	last := ""
	if len(segments) > 0 {
		last = segments[len(segments)-1]
	}

	queryIDs := map[string]bool{}
	for _, values := range parsed.Query() {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" {
				queryIDs[strings.ToLower(value)] = true
			}
		}
	}

	pair := ""
	if len(segments) >= 2 {
		pair = segments[len(segments)-2] + "/" + segments[len(segments)-1]
	}

	signalsExist := strings.Trim(parsed.Path, "/") != "" || len(queryIDs) > 0
	return targetSignals{
		rawURL:       strings.ToLower(strings.TrimSpace(targetURL)),
		path:         strings.ToLower(strings.Trim(parsed.Path, "/")),
		segments:     segments,
		lastSegment:  last,
		lastTokens:   tokenizeSemantic(last),
		pathPair:     pair,
		queryIDs:     queryIDs,
		signalsExist: signalsExist,
	}
}

func semanticScore(targetURL, resolvedURL string, body []byte) float64 {
	signals := buildTargetSignals(targetURL)
	if !signals.signalsExist {
		return 0.6
	}

	score := endpointURLScore(signals, resolvedURL)
	bodyScore := responseBodyScore(signals, body)
	if bodyScore > score {
		score = bodyScore
	}
	return score
}

func endpointURLScore(signals targetSignals, resolvedURL string) float64 {
	parsed, err := url.Parse(resolvedURL)
	if err != nil {
		return 0
	}

	path := strings.ToLower(strings.Trim(parsed.Path, "/"))
	switch {
	case path == signals.path && path != "":
		return 0.72
	case strings.TrimSuffix(path, ".json") == signals.path && path != "":
		return 0.82
	}

	if signals.pathPair != "" && strings.Contains(path, signals.pathPair) {
		return 0.66
	}
	if signals.lastSegment != "" && strings.Contains(path, signals.lastSegment) {
		return 0.58
	}
	return 0
}

func responseBodyScore(signals targetSignals, body []byte) float64 {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0
	}
	return scoreValue(signals, "", parsed, 0, 0)
}

func scoreValue(signals targetSignals, key string, value any, depth, seen int) float64 {
	if depth > 5 || seen > 250 {
		return 0
	}

	switch val := value.(type) {
	case map[string]any:
		best := 0.0
		for childKey, childValue := range val {
			score := scoreValue(signals, strings.ToLower(childKey), childValue, depth+1, seen+1)
			if score > best {
				best = score
			}
			if best >= 1 {
				return best
			}
		}
		return best
	case []any:
		best := 0.0
		limit := len(val)
		if limit > 12 {
			limit = 12
		}
		for i := 0; i < limit; i++ {
			score := scoreValue(signals, key, val[i], depth+1, seen+1)
			if score > best {
				best = score
			}
			if best >= 1 {
				return best
			}
		}
		return best
	case string:
		return scoreString(signals, key, val)
	case float64:
		if key == "id" || strings.HasSuffix(key, "_id") {
			id := strings.ToLower(strconv.FormatInt(int64(val), 10))
			if signals.queryIDs[id] {
				return 0.96
			}
		}
	case json.Number:
		if key == "id" || strings.HasSuffix(key, "_id") {
			if signals.queryIDs[strings.ToLower(val.String())] {
				return 0.96
			}
		}
	}

	return 0
}

func scoreString(signals targetSignals, key, value string) float64 {
	if value == "" {
		return 0
	}

	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return 0
	}

	if lower == signals.rawURL {
		return 1
	}

	if looksLikeURL(lower) {
		parsed, err := url.Parse(lower)
		if err == nil {
			path := strings.ToLower(strings.Trim(parsed.Path, "/"))
			if path == signals.path && path != "" {
				return 1
			}
			if strings.TrimSuffix(path, ".json") == signals.path && path != "" {
				return 0.94
			}
			if signals.pathPair != "" && strings.Contains(path, signals.pathPair) {
				return 0.88
			}
		}
	}

	switch key {
	case "full_name":
		if signals.pathPair != "" && strings.Contains(lower, signals.pathPair) {
			return 0.95
		}
	case "slug", "handle", "subreddit", "repo", "owner", "name", "title", "path", "permalink", "url", "link", "html_url", "canonical_url":
		if signals.lastSegment != "" && lower == signals.lastSegment {
			return 0.9
		}
	}

	if signals.lastSegment != "" && strings.Contains(lower, signals.lastSegment) {
		if len(signals.lastTokens) == 0 {
			return 0.74
		}
		matches := 0
		for _, token := range signals.lastTokens {
			if strings.Contains(lower, token) {
				matches++
			}
		}
		if matches == len(signals.lastTokens) {
			return 0.84
		}
		if matches > 0 {
			return 0.7
		}
	}

	if signals.pathPair != "" && strings.Contains(lower, signals.pathPair) {
		return 0.86
	}

	if signals.queryIDs[lower] {
		return 0.94
	}

	return 0
}

func looksLikeURL(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

func splitSemanticSegments(path string) []string {
	raw := strings.Split(strings.Trim(path, "/"), "/")
	segments := make([]string, 0, len(raw))
	for _, segment := range raw {
		segment = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(segment, ".json")))
		if segment == "" {
			continue
		}
		segments = append(segments, segment)
	}
	return segments
}

func tokenizeSemantic(segment string) []string {
	if segment == "" {
		return nil
	}
	parts := strings.FieldsFunc(segment, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/'
	})
	var tokens []string
	for _, part := range parts {
		part = strings.TrimSpace(strings.ToLower(part))
		if part != "" {
			tokens = append(tokens, part)
		}
	}
	return tokens
}
