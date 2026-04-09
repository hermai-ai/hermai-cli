package actions

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/hermai-ai/hermai-cli/pkg/cache"
	"github.com/hermai-ai/hermai-cli/pkg/htmlext"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// Catalog is the action-first, agent-facing surface for a URL.
type Catalog struct {
	Domain   string          `json:"domain"`
	URL      string          `json:"url"`
	Source   string          `json:"source"`
	Coverage string          `json:"coverage,omitempty"`
	Actions  []schema.Action `json:"actions"`
}

// DiscoverOptions configures browserless action discovery.
type DiscoverOptions struct {
	ProxyURL string
	Insecure bool
}

// BuildCatalog compiles actions from cached API schemas and the live public page.
func BuildCatalog(ctx context.Context, c cache.Service, targetURL string, opts DiscoverOptions) (*Catalog, error) {
	domain, err := schema.ExtractDomain(targetURL)
	if err != nil {
		return nil, err
	}

	var (
		apiSchema *schema.Schema
		cssSchema *schema.Schema
	)
	if c != nil {
		apiSchema, cssSchema, _ = c.LookupAll(ctx, targetURL)
	}

	actions := make([]schema.Action, 0, 8)
	actions = append(actions, genericNavigateAction())
	actions = append(actions, inferURLActions(targetURL)...)
	if apiSchema != nil {
		actions = append(actions, compileAPIActions(apiSchema)...)
	}

	page, err := fetchPage(ctx, targetURL, HTTPOptions{
		ProxyURL: opts.ProxyURL,
		Insecure: opts.Insecure,
	})
	if err == nil && strings.Contains(strings.ToLower(page.ContentType), "text/html") {
		content := htmlext.Extract(page.Body, page.FinalURL)
		actions = append(actions, compileHTMLActions(page.FinalURL, content)...)
	}

	actions = dedupeActions(actions)

	source := "html_extraction"
	switch {
	case apiSchema != nil && len(actions) > len(compileAPIActions(apiSchema)):
		source = "hybrid"
	case apiSchema != nil:
		source = "api"
	case cssSchema != nil:
		source = "html_extraction"
	}

	coverage := schema.SchemaCoveragePartial
	if apiSchema != nil && apiSchema.Coverage != "" {
		coverage = apiSchema.Coverage
	}

	return &Catalog{
		Domain:   domain,
		URL:      targetURL,
		Source:   source,
		Coverage: coverage,
		Actions:  actions,
	}, nil
}

func compileAPIActions(s *schema.Schema) []schema.Action {
	actions := make([]schema.Action, 0, len(s.Endpoints))
	for _, ep := range s.Endpoints {
		action := schema.Action{
			Name:        normalizedActionName(ep.Name, "api_call"),
			Description: firstNonEmpty(ep.Description, ep.Name),
			Kind:        schema.ActionKindAPICall,
			Transport:   schema.ActionTransportAPICall,
			Method:      strings.ToUpper(ep.Method),
			URLTemplate: ep.URLTemplate,
			Headers:     ep.Headers,
			Confidence:  ep.Confidence,
			Source:      "api_schema",
			Result: &schema.ActionResult{
				EntityType:     inferEntityType(ep.Name, ep.ResponseSchema),
				Fields:         responseFieldNames(ep.ResponseSchema),
				ResponseSchema: ep.ResponseSchema,
			},
			Requirements: schema.ActionRequirements{
				Auth:    schema.ActionAuthPublicOnly,
				Session: schema.ActionSessionCookies,
				JS:      schema.ActionJSNotRequired,
			},
		}
		for _, v := range ep.Variables {
			action.Params = append(action.Params, schema.ActionParam{
				Name:        v.Name,
				In:          v.Source,
				Type:        "string",
				Required:    true,
				Description: "Resolved from the source URL unless explicitly overridden",
			})
		}
		for _, qp := range ep.QueryParams {
			action.Params = append(action.Params, schema.ActionParam{
				Name:     qp.Key,
				In:       "query",
				Type:     "string",
				Required: qp.Required,
				Default:  qp.Value,
			})
		}
		actions = append(actions, action)
	}
	return actions
}

func compileHTMLActions(targetURL string, page htmlext.PageContent) []schema.Action {
	actions := make([]schema.Action, 0, 4)

	for _, form := range page.Forms {
		if action, ok := actionFromForm(targetURL, form); ok {
			actions = append(actions, action)
		}
	}

	if action, ok := paginationAction(targetURL, page.Links); ok {
		actions = append(actions, action)
	}

	return actions
}

func genericNavigateAction() schema.Action {
	return schema.Action{
		Name:        "navigate",
		Description: "Open a public URL on the same site without a browser",
		Kind:        schema.ActionKindNavigate,
		Transport:   schema.ActionTransportHTTPGet,
		Method:      "GET",
		URLTemplate: "{url}",
		Confidence:  0.95,
		Source:      "html_page",
		Params: []schema.ActionParam{
			{Name: "url", In: "url", Type: "string", Required: true},
		},
		Result: &schema.ActionResult{
			EntityType: "page",
			Fields:     []string{"title", "description", "links", "forms", "body_text"},
		},
		Requirements: schema.ActionRequirements{
			Auth:    schema.ActionAuthPublicOnly,
			Session: schema.ActionSessionCookies,
			JS:      schema.ActionJSOptional,
		},
	}
}

func inferURLActions(targetURL string) []schema.Action {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil
	}

	var actions []schema.Action
	query := u.Query()

	if searchKey := detectSearchQueryKey(query); searchKey != "" {
		searchURL := *u
		searchQuery := searchURL.Query()
		searchQuery.Set(searchKey, "{query}")
		if pageKey := detectPageQueryKey(searchQuery); pageKey != "" {
			searchQuery.Del(pageKey)
		}
		searchURL.RawQuery = encodeTemplateQuery(searchQuery)
		actions = append(actions, schema.Action{
			Name:        "search",
			Description: "Search the current site using its public URL surface",
			Kind:        schema.ActionKindSearch,
			Transport:   schema.ActionTransportHTTPGet,
			Method:      "GET",
			URLTemplate: searchURL.String(),
			Confidence:  0.82,
			Source:      "url_pattern",
			Params: []schema.ActionParam{
				{Name: "query", In: "query", Type: "string", Required: true},
			},
			Result: &schema.ActionResult{
				EntityType: "page",
				Fields:     []string{"title", "description", "links", "forms", "body_text"},
			},
			Requirements: schema.ActionRequirements{
				Auth:    schema.ActionAuthPublicOnly,
				Session: schema.ActionSessionCookies,
				JS:      schema.ActionJSOptional,
			},
		})

		if pageKey := detectPageQueryKey(query); pageKey != "" {
			paginateURL := *u
			paginateQuery := paginateURL.Query()
			paginateQuery.Set(pageKey, "{page}")
			paginateURL.RawQuery = encodeTemplateQuery(paginateQuery)
			actions = append(actions, schema.Action{
				Name:        "paginate",
				Description: "Navigate to another page of public results",
				Kind:        schema.ActionKindPaginate,
				Transport:   schema.ActionTransportHTTPGet,
				Method:      "GET",
				URLTemplate: paginateURL.String(),
				Confidence:  0.78,
				Source:      "url_pattern",
				Params: []schema.ActionParam{
					{Name: "page", In: "query", Type: "integer", Required: true},
				},
				Result: &schema.ActionResult{
					EntityType: "page",
					Fields:     []string{"title", "description", "links", "forms", "body_text"},
				},
				Requirements: schema.ActionRequirements{
					Auth:    schema.ActionAuthPublicOnly,
					Session: schema.ActionSessionCookies,
					JS:      schema.ActionJSOptional,
				},
			})
		}
	}

	return actions
}

func actionFromForm(targetURL string, form htmlext.Form) (schema.Action, bool) {
	searchField := detectSearchField(form.Fields)
	if strings.ToUpper(form.Method) == "POST" && visibleFieldCount(form.Fields) == 0 {
		return schema.Action{}, false
	}
	params := make([]schema.ActionParam, 0, len(form.Fields))
	queryTemplate := url.Values{}
	bodyParams := make([]schema.ActionParam, 0, len(form.Fields))

	for _, field := range form.Fields {
		switch strings.ToLower(field.Type) {
		case "submit", "button", "image", "reset", "file":
			continue
		}

		param := schema.ActionParam{
			Name:     field.Name,
			Type:     "string",
			Required: field.Required || field.Name == searchField,
			Default:  field.Value,
		}
		switch strings.ToUpper(form.Method) {
		case "POST":
			param.In = "form"
			bodyParams = append(bodyParams, param)
		default:
			param.In = "query"
			params = append(params, param)
			value := field.Value
			if field.Type != "hidden" && field.Type != "select" {
				value = "{" + field.Name + "}"
			} else if value == "" && field.Type == "select" && len(field.Options) > 0 {
				value = "{" + field.Name + "}"
			}
			if value == "" {
				value = "{" + field.Name + "}"
			}
			queryTemplate.Set(field.Name, value)
		}
	}

	action := schema.Action{
		Name:        "submit_form",
		Description: "Submit a public form without a browser",
		Kind:        schema.ActionKindSubmitForm,
		Source:      "html_page",
		Confidence:  0.75,
		Result: &schema.ActionResult{
			EntityType: "page",
			Fields:     []string{"title", "description", "links", "forms", "body_text"},
		},
		Requirements: schema.ActionRequirements{
			Auth:    schema.ActionAuthPublicOnly,
			Session: schema.ActionSessionCookies,
			JS:      schema.ActionJSOptional,
		},
	}

	if searchField != "" {
		action.Name = "search"
		action.Kind = schema.ActionKindSearch
		action.Description = "Search the site using a public form"
		action.Confidence = 0.9
	}

	switch strings.ToUpper(form.Method) {
	case "POST":
		action.Transport = schema.ActionTransportHTTPPostForm
		action.Method = "POST"
		action.URLTemplate = form.Action
		action.Params = bodyParams
	default:
		action.Transport = schema.ActionTransportHTTPGet
		action.Method = "GET"
		u, err := url.Parse(form.Action)
		if err != nil {
			return schema.Action{}, false
		}
		u.RawQuery = encodeTemplateQuery(queryTemplate)
		action.URLTemplate = u.String()
		action.Params = params
	}

	return action, len(action.Params) > 0
}

func paginationAction(targetURL string, links []htmlext.Link) (schema.Action, bool) {
	for _, link := range links {
		if !looksLikePagination(link) {
			continue
		}
		u, err := url.Parse(link.Href)
		if err != nil {
			continue
		}
		q := u.Query()
		if q.Get("page") == "" {
			continue
		}
		q.Set("page", "{page}")
		u.RawQuery = encodeTemplateQuery(q)
		return schema.Action{
			Name:        "paginate",
			Description: "Navigate to another page of results",
			Kind:        schema.ActionKindPaginate,
			Transport:   schema.ActionTransportHTTPGet,
			Method:      "GET",
			URLTemplate: u.String(),
			Confidence:  0.88,
			Source:      "html_page",
			Params: []schema.ActionParam{
				{Name: "page", In: "query", Type: "integer", Required: true},
			},
			Result: &schema.ActionResult{
				EntityType: "page",
				Fields:     []string{"title", "description", "links", "forms", "body_text"},
			},
			Requirements: schema.ActionRequirements{
				Auth:    schema.ActionAuthPublicOnly,
				Session: schema.ActionSessionCookies,
				JS:      schema.ActionJSOptional,
			},
		}, true
	}

	parsed, err := url.Parse(targetURL)
	if err != nil {
		return schema.Action{}, false
	}
	q := parsed.Query()
	if q.Get("page") != "" {
		q.Set("page", "{page}")
		parsed.RawQuery = encodeTemplateQuery(q)
		return schema.Action{
			Name:        "paginate",
			Description: "Navigate to another page of results",
			Kind:        schema.ActionKindPaginate,
			Transport:   schema.ActionTransportHTTPGet,
			Method:      "GET",
			URLTemplate: parsed.String(),
			Confidence:  0.7,
			Source:      "html_page",
			Params: []schema.ActionParam{
				{Name: "page", In: "query", Type: "integer", Required: true},
			},
			Result: &schema.ActionResult{
				EntityType: "page",
				Fields:     []string{"title", "description", "links", "forms", "body_text"},
			},
			Requirements: schema.ActionRequirements{
				Auth:    schema.ActionAuthPublicOnly,
				Session: schema.ActionSessionCookies,
				JS:      schema.ActionJSOptional,
			},
		}, true
	}

	return schema.Action{}, false
}

func detectSearchField(fields []htmlext.FormField) string {
	for _, field := range fields {
		name := strings.ToLower(field.Name)
		normalized := normalizeKeyToken(name)
		fieldType := strings.ToLower(field.Type)
		if fieldType == "hidden" {
			continue
		}
		if fieldType == "search" {
			return field.Name
		}
		if matchesSearchKey(name, normalized) {
			return field.Name
		}
	}
	return ""
}

func visibleFieldCount(fields []htmlext.FormField) int {
	count := 0
	for _, field := range fields {
		switch strings.ToLower(field.Type) {
		case "hidden", "submit", "button", "reset", "image":
			continue
		default:
			count++
		}
	}
	return count
}

func detectSearchQueryKey(values url.Values) string {
	for key := range values {
		lower := strings.ToLower(key)
		if matchesSearchKey(lower, normalizeKeyToken(lower)) {
			return key
		}
	}
	return ""
}

func detectPageQueryKey(values url.Values) string {
	for key := range values {
		lower := strings.ToLower(key)
		normalized := normalizeKeyToken(lower)
		for _, candidate := range []string{"page", "pagenum", "pageno", "pagenumber", "pgn", "pg"} {
			if lower == candidate || normalized == candidate {
				return key
			}
		}
	}
	return ""
}

func looksLikePagination(link htmlext.Link) bool {
	text := strings.ToLower(strings.TrimSpace(link.Text))
	if text == "next" || strings.Contains(text, "next page") {
		return true
	}
	u, err := url.Parse(link.Href)
	if err != nil {
		return false
	}
	if u.Query().Get("page") != "" {
		return true
	}
	if strings.Contains(path.Base(u.Path), "page") {
		return true
	}
	return false
}

func dedupeActions(actions []schema.Action) []schema.Action {
	sort.SliceStable(actions, func(i, j int) bool {
		iScore := actionRank(actions[i])
		jScore := actionRank(actions[j])
		if iScore != jScore {
			return iScore > jScore
		}
		if actions[i].Name != actions[j].Name {
			return actions[i].Name < actions[j].Name
		}
		if len(actions[i].Params) != len(actions[j].Params) {
			return len(actions[i].Params) < len(actions[j].Params)
		}
		return actions[i].URLTemplate < actions[j].URLTemplate
	})

	out := make([]schema.Action, 0, len(actions))
	seen := make(map[string]bool)
	nameCount := make(map[string]int)

	for _, action := range actions {
		key := action.Name + "|" + action.Transport + "|" + action.URLTemplate
		if seen[key] {
			continue
		}
		seen[key] = true
		nameCount[action.Name]++
		if nameCount[action.Name] > 1 {
			action.Name = fmt.Sprintf("%s_%d", action.Name, nameCount[action.Name])
		}
		out = append(out, action)
	}
	return out
}

func actionRank(action schema.Action) int {
	score := int(action.Confidence * 100)

	switch action.Transport {
	case schema.ActionTransportAPICall:
		score += 40
	case schema.ActionTransportHTTPGet:
		score += 15
	}

	switch action.Kind {
	case schema.ActionKindSearch:
		score += 30
	case schema.ActionKindPaginate:
		score += 20
	case schema.ActionKindNavigate:
		score += 5
	}

	switch action.Source {
	case "api_schema":
		score += 20
	case "url_pattern":
		score += 15
	}

	score -= len(action.Params) * 4

	if len(action.URLTemplate) > 120 {
		score -= 8
	}

	for _, noisy := range []string{"qid=", "ref=", "_trksid=", "anti-csrftoken", "offerListingId", "clientName"} {
		if strings.Contains(action.URLTemplate, noisy) {
			score -= 12
		}
	}

	for _, param := range action.Params {
		name := normalizeKeyToken(strings.ToLower(param.Name))
		switch name {
		case "query", "q", "search", "keyword", "keywords", "term", "nkw":
			score += 18
		case "url", "ref", "qid":
			score -= 8
		}
		if !param.Required {
			score -= 2
		}
	}

	if strings.Contains(action.URLTemplate, "{query}") || strings.Contains(action.URLTemplate, "{q}") {
		score += 20
	}

	if action.Kind == schema.ActionKindSearch && len(action.Params) == 1 {
		score += 10
	}

	return score
}

func normalizedActionName(input, fallback string) string {
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return fallback
	}
	replacer := strings.NewReplacer(" ", "_", "-", "_", "/", "_")
	input = replacer.Replace(input)
	input = strings.Trim(input, "_")
	if input == "" {
		return fallback
	}
	return input
}

func inferEntityType(name string, rs *schema.ResponseSchema) string {
	name = strings.ToLower(name)
	switch {
	case strings.Contains(name, "search"):
		return "search_results"
	case strings.Contains(name, "list"):
		return "list"
	case strings.Contains(name, "repo"), strings.Contains(name, "product"), strings.Contains(name, "item"):
		return "entity"
	}
	if rs != nil && rs.Type == "array" {
		return "list"
	}
	return "object"
}

func responseFieldNames(rs *schema.ResponseSchema) []string {
	if rs == nil {
		return nil
	}
	if rs.Type == "array" && rs.Items != nil && len(rs.Items.Fields) > 0 {
		fields := make([]string, 0, len(rs.Items.Fields))
		for _, field := range rs.Items.Fields {
			fields = append(fields, field.Name)
		}
		return fields
	}
	fields := make([]string, 0, len(rs.Fields))
	for _, field := range rs.Fields {
		fields = append(fields, field.Name)
	}
	return fields
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func encodeTemplateQuery(values url.Values) string {
	encoded := values.Encode()
	for key, list := range values {
		for _, value := range list {
			if !strings.Contains(value, "{") || !strings.Contains(value, "}") {
				continue
			}
			encodedValue := url.QueryEscape(value)
			replacement := url.QueryEscape(key) + "=" + value
			encoded = strings.ReplaceAll(encoded, url.QueryEscape(key)+"="+encodedValue, replacement)
		}
	}
	return encoded
}

func matchesSearchKey(raw, normalized string) bool {
	for _, candidate := range []string{"q", "query", "search", "keyword", "keywords", "k", "term", "nkw"} {
		if raw == candidate || normalized == candidate {
			return true
		}
		if strings.Contains(raw, candidate) || strings.Contains(normalized, candidate) {
			return true
		}
	}
	return false
}

func normalizeKeyToken(input string) string {
	var b strings.Builder
	b.Grow(len(input))
	for _, r := range input {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
