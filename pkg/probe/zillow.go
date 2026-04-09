package probe

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	zillowSearchPathRe = regexp.MustCompile(`^/[\w-]+-[\w]{2}/`) // /city-state/...
	zillowDetailPathRe = regexp.MustCompile(`/homedetails/`)
	zillowBedsRe       = regexp.MustCompile(`(\d+)-_beds`)
	zillowPageRe       = regexp.MustCompile(`(\d+)_p/?$`)
)

// IsZillowSearch returns true if the URL is a Zillow search page
// (not a detail page, not a tool page).
func IsZillowSearch(targetURL string) bool {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "www.zillow.com" && host != "zillow.com" {
		return false
	}
	path := parsed.Path
	if zillowDetailPathRe.MatchString(path) {
		return false
	}
	return zillowSearchPathRe.MatchString(path)
}

// NormalizeZillowSearchURL rewrites a Zillow search URL to include
// searchQueryState in the query string. Zillow slug URLs like
// /san-francisco-ca/rentals/5-_beds/ do NOT filter data — only
// searchQueryState does.
//
// If searchQueryState is already present, the URL is returned unchanged.
func NormalizeZillowSearchURL(targetURL string) (string, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return targetURL, err
	}

	// Already has searchQueryState — pass through
	if parsed.Query().Get("searchQueryState") != "" {
		return targetURL, nil
	}

	// Not a Zillow search — pass through
	if !IsZillowSearch(targetURL) {
		return targetURL, nil
	}

	state := buildSearchQueryState(parsed.Path)
	encoded, err := json.Marshal(state)
	if err != nil {
		return targetURL, nil
	}

	q := parsed.Query()
	q.Set("searchQueryState", string(encoded))
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

// buildSearchQueryState parses Zillow URL slug segments into the
// searchQueryState JSON structure that Zillow's server expects.
func buildSearchQueryState(path string) map[string]any {
	filterState := map[string]any{
		"sort": map[string]any{"value": "priorityscore"},
	}

	// Detect /rentals/ → for rent filter
	if strings.Contains(path, "/rentals") {
		filterState["fr"] = map[string]any{"value": true}   // for rent
		filterState["fsba"] = map[string]any{"value": false} // not for sale by agent
		filterState["fsbo"] = map[string]any{"value": false} // not for sale by owner
		filterState["nc"] = map[string]any{"value": false}   // not new construction
		filterState["cmsn"] = map[string]any{"value": false} // not coming soon
		filterState["auc"] = map[string]any{"value": false}  // not auction
		filterState["fore"] = map[string]any{"value": false} // not foreclosure
	}

	// Detect /N-_beds/ → bedroom filter
	if m := zillowBedsRe.FindStringSubmatch(path); len(m) > 1 {
		beds, _ := strconv.Atoi(m[1])
		if beds > 0 {
			filterState["beds"] = map[string]any{"min": beds}
		}
	}

	state := map[string]any{
		"pagination":    map[string]any{},
		"isMapVisible":  false,
		"filterState":   filterState,
		"isListVisible": true,
	}

	// Detect /N_p/ → page number
	if m := zillowPageRe.FindStringSubmatch(path); len(m) > 1 {
		page, _ := strconv.Atoi(m[1])
		if page > 1 {
			state["pagination"] = map[string]any{"currentPage": page}
		}
	}

	return state
}
