package probe

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

//go:embed known_sites.json
var knownSitesJSON []byte

// jsonSitePattern is the JSON-serializable form of a known site pattern.
// Community contributors add new sites by editing known_sites.json.
type jsonSitePattern struct {
	Name           string            `json:"name"`
	HostPattern    string            `json:"host_pattern"`
	PathPattern    string            `json:"path_pattern"`
	APIURLTemplate string            `json:"api_url_template"`
	Variables      []schema.Variable `json:"variables"`
}

// sitePattern is the compiled runtime form of a known site pattern.
type sitePattern struct {
	Name           string
	HostPattern    *regexp.Regexp
	PathPattern    *regexp.Regexp
	APIURLTemplate string
	Variables      []schema.Variable
}

// knownSites is the compiled registry, loaded from known_sites.json at init.
var knownSites []sitePattern

func init() {
	var raw []jsonSitePattern
	if err := json.Unmarshal(knownSitesJSON, &raw); err != nil {
		panic(fmt.Sprintf("hermai: failed to parse known_sites.json: %v", err))
	}

	knownSites = make([]sitePattern, 0, len(raw))
	for _, r := range raw {
		hostRe, err := regexp.Compile(r.HostPattern)
		if err != nil {
			panic(fmt.Sprintf("hermai: invalid host_pattern %q in known_sites.json: %v", r.HostPattern, err))
		}
		pathRe, err := regexp.Compile(r.PathPattern)
		if err != nil {
			panic(fmt.Sprintf("hermai: invalid path_pattern %q in known_sites.json: %v", r.PathPattern, err))
		}
		knownSites = append(knownSites, sitePattern{
			Name:           r.Name,
			HostPattern:    hostRe,
			PathPattern:    pathRe,
			APIURLTemplate: r.APIURLTemplate,
			Variables:      r.Variables,
		})
	}
}

// buildAPIURL substitutes {var} placeholders in the template with extracted values.
func buildAPIURL(template string, vars map[string]string) string {
	result := template
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{"+k+"}", v)
	}
	return result
}

// tryKnownSite checks if the URL matches any known site pattern.
// Returns the schema, strategy name, and error.
func tryKnownSite(ctx context.Context, client httpclient.Doer, targetURL string) (*schema.Schema, string, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, "", err
	}

	host := parsed.Hostname()

	for _, site := range knownSites {
		if !site.HostPattern.MatchString(host) {
			continue
		}

		if !site.PathPattern.MatchString(parsed.Path) {
			continue
		}

		// Extract named groups from host and path
		vars := extractNamedGroups(site.HostPattern, host)
		for k, v := range extractNamedGroups(site.PathPattern, parsed.Path) {
			vars[k] = v
		}

		// Also extract query params (for HN-style ?id=123)
		for k, values := range parsed.Query() {
			if len(values) > 0 {
				vars[k] = values[0]
			}
		}

		// Resolve path/path_rest variables by segment index when not already
		// populated by regex named groups (needed for github_tree, github_blob)
		segments := splitPathSegments(parsed.Path)
		for _, v := range site.Variables {
			if _, exists := vars[v.Name]; exists {
				continue
			}
			idx, err := strconv.Atoi(v.Pattern)
			if err != nil || idx < 0 || idx >= len(segments) {
				continue
			}
			switch v.Source {
			case "path":
				vars[v.Name] = segments[idx]
			case "path_rest":
				vars[v.Name] = strings.Join(segments[idx:], "/")
			}
		}

		apiURL := buildAPIURL(site.APIURLTemplate, vars)

		// Verify the API actually returns JSON
		body, err := doJSONRequest(ctx, client, apiURL, nil)
		if err != nil || body == nil {
			continue
		}

		resolvedTemplate := resolveKnownSiteTemplate(site.APIURLTemplate, vars, site.Variables)
		urlPattern, schemaVars := knownSitePattern(parsed.Path, site.Variables)
		s := &schema.Schema{
			ID:             schema.GenerateID(host, urlPattern),
			Domain:         host,
			URLPattern:     urlPattern,
			SchemaType:     schema.SchemaTypeAPI,
			Coverage:       schema.SchemaCoveragePartial,
			Version:        1,
			CreatedAt:      time.Now(),
			DiscoveredFrom: targetURL,
			Endpoints: []schema.Endpoint{
				{
					Name:        site.Name,
					Description: knownSiteDescription(site.Name),
					Method:      "GET",
					URLTemplate: resolvedTemplate,
					Headers:     map[string]string{},
					Variables:   schemaVars,
					IsPrimary:   true,
					Confidence:  0.98,
				},
			},
		}

		return s, fmt.Sprintf("known_site:%s", site.Name), nil
	}

	return nil, "", nil
}

// extractNamedGroups extracts named capture groups from a regex match.
func extractNamedGroups(re *regexp.Regexp, s string) map[string]string {
	match := re.FindStringSubmatch(s)
	if match == nil {
		return map[string]string{}
	}

	result := map[string]string{}
	for i, name := range re.SubexpNames() {
		if i != 0 && name != "" && i < len(match) {
			result[name] = match[i]
		}
	}
	return result
}

func knownSitePattern(path string, variables []schema.Variable) (string, []schema.Variable) {
	pattern := splitPathSegments(schema.NormalizePathStructure(path))
	segments := splitPathSegments(path)
	resolvedVars := make([]schema.Variable, 0, len(variables))

	for _, variable := range variables {
		// path_rest: wildcard all segments from starting index onward
		if variable.Source == "path_rest" {
			idx, err := strconv.Atoi(variable.Pattern)
			if err != nil || idx < 0 || idx >= len(pattern) {
				resolvedVars = append(resolvedVars, variable)
				continue
			}
			for i := idx; i < len(pattern); i++ {
				pattern[i] = "{}"
			}
			resolvedVars = append(resolvedVars, schema.Variable{
				Name:    variable.Name,
				Source:  "path_rest",
				Pattern: strconv.Itoa(idx),
			})
			continue
		}

		if variable.Source != "path" && variable.Source != "url" {
			resolvedVars = append(resolvedVars, variable)
			continue
		}

		idx := knownSiteVariableIndex(variable.Pattern, segments)
		if idx < 0 || idx >= len(pattern) {
			resolvedVars = append(resolvedVars, variable)
			continue
		}

		pattern[idx] = "{}"
		resolvedVars = append(resolvedVars, schema.Variable{
			Name:    variable.Name,
			Source:  "path",
			Pattern: strconv.Itoa(idx),
		})
	}

	if len(pattern) == 0 {
		return "/", resolvedVars
	}
	return "/" + strings.Join(pattern, "/"), resolvedVars
}

func knownSiteVariableIndex(pattern string, segments []string) int {
	if idx, err := strconv.Atoi(pattern); err == nil {
		return idx
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return -1
	}

	for i := len(segments) - 1; i >= 0; i-- {
		if re.MatchString(segments[i]) {
			return i
		}
	}
	return -1
}

func knownSiteDescription(name string) string {
	switch name {
	case "github_repo":
		return "GitHub repository metadata from the public GitHub REST API"
	case "github_tree":
		return "GitHub directory listing from the public GitHub Contents API"
	case "github_blob":
		return "GitHub file content from the public GitHub Contents API"
	case "hn_item":
		return "Hacker News item details from the public Firebase API"
	case "x_tweet":
		return "Public tweet metadata from the X syndication endpoint"
	case "youtube_video", "youtube_short":
		return "Video metadata from the public YouTube oEmbed endpoint"
	case "arkansas_court_case":
		return "Arkansas court case details from the public judiciary API"
	case "wikipedia_page":
		return "Wikipedia page summary from the public REST API"
	case "npm_package", "npm_scoped_package":
		return "NPM package metadata from the public registry API"
	case "pypi_package":
		return "PyPI package metadata from the public JSON API"
	case "yahoo_finance_quote":
		return "Yahoo Finance quote data from the public chart API"
	case "hn_front_page":
		return "Hacker News top stories from the public Firebase API"
	default:
		return "Public endpoint discovered via a known-site adapter"
	}
}

func resolveKnownSiteTemplate(template string, vars map[string]string, variables []schema.Variable) string {
	preserve := make(map[string]bool, len(variables))
	for _, variable := range variables {
		if variable.Source == "path" || variable.Source == "path_rest" || variable.Source == "query" {
			preserve[variable.Name] = true
		}
	}

	result := template
	for key, value := range vars {
		if preserve[key] {
			continue
		}
		result = strings.ReplaceAll(result, "{"+key+"}", value)
	}
	return result
}
