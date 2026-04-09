package htmlext

import (
	"encoding/json"
	"testing"

	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

func TestExtractStructured_WithJSONLD(t *testing.T) {
	html := `<!DOCTYPE html>
<html lang="en">
<head>
	<title>Great Article</title>
	<meta name="description" content="An insightful article">
	<meta property="og:type" content="article">
	<script type="application/ld+json">{
		"@type": "Article",
		"name": "Great Article",
		"author": {"@type": "Person", "name": "Jane Smith"},
		"datePublished": "2026-03-15",
		"headline": "An insightful headline"
	}</script>
</head>
<body><article><p>Article content here.</p></article></body>
</html>`

	result := ExtractStructured(html, "https://example.com/article/1")

	if result["title"] != "Great Article" {
		t.Errorf("title: got %q", result["title"])
	}
	if result["description"] != "An insightful article" {
		t.Errorf("description: got %q", result["description"])
	}
	if result["type"] != "article" {
		t.Errorf("type: got %q", result["type"])
	}
	if result["author"] != "Jane Smith" {
		t.Errorf("author: got %v", result["author"])
	}
	if result["datePublished"] != "2026-03-15" {
		t.Errorf("datePublished: got %v", result["datePublished"])
	}
	// Should ALSO have body_text from rendered DOM alongside JSON-LD
	if _, hasBody := result["body_text"]; !hasBody {
		t.Error("should include body_text even when JSON-LD is present")
	}
}

func TestExtractStructured_WithoutJSONLD(t *testing.T) {
	html := `<!DOCTYPE html>
<html lang="en">
<head>
	<title>Simple Page</title>
	<meta name="description" content="A simple page">
	<meta property="og:type" content="website">
	<meta property="og:image" content="https://example.com/img.png">
	<meta property="og:site_name" content="Example Site">
	<link rel="canonical" href="https://example.com/simple">
</head>
<body>
	<main>
		<p>This is the main content of a simple page with enough text to be useful.</p>
	</main>
</body>
</html>`

	result := ExtractStructured(html, "https://example.com/simple")

	if result["title"] != "Simple Page" {
		t.Errorf("title: got %q", result["title"])
	}
	if result["description"] != "A simple page" {
		t.Errorf("description: got %q", result["description"])
	}
	if result["language"] != "en" {
		t.Errorf("language: got %q", result["language"])
	}
	if result["url"] != "https://example.com/simple" {
		t.Errorf("url: got %q", result["url"])
	}
	if result["image"] != "https://example.com/img.png" {
		t.Errorf("image: got %q", result["image"])
	}
	if result["site_name"] != "Example Site" {
		t.Errorf("site_name: got %q", result["site_name"])
	}
	if _, hasBody := result["body_text"]; !hasBody {
		t.Error("should include body_text when no JSON-LD")
	}
}

func TestExtractStructured_EmptyHTML(t *testing.T) {
	result := ExtractStructured("", "https://example.com")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result["type"] != "generic" {
		t.Errorf("type: got %q, want 'generic'", result["type"])
	}
}

func TestExtractStructured_InfersArticleFromOG(t *testing.T) {
	html := `<head><meta property="og:type" content="article"></head><body></body>`
	result := ExtractStructured(html, "https://example.com")
	if result["type"] != "article" {
		t.Errorf("type: got %q, want 'article'", result["type"])
	}
}

func TestExtractStructured_InfersProfileFromOG(t *testing.T) {
	html := `<head><meta property="og:type" content="profile"></head><body></body>`
	result := ExtractStructured(html, "https://example.com")
	if result["type"] != "profile" {
		t.Errorf("type: got %q, want 'profile'", result["type"])
	}
}

func TestExtractStructured_InfersFromJSONLDType(t *testing.T) {
	html := `<head>
		<script type="application/ld+json">{"@type":"Product","name":"Widget"}</script>
	</head><body></body>`
	result := ExtractStructured(html, "https://example.com")
	if result["type"] != "product" {
		t.Errorf("type: got %q, want 'product'", result["type"])
	}
}

func TestExtractStructured_MarshalJSON(t *testing.T) {
	result := map[string]any{
		"title":       "Test",
		"description": "Test desc",
		"type":        "generic",
	}
	data, err := MarshalStructured(result)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("produced invalid JSON: %v", err)
	}
	if parsed["title"] != "Test" {
		t.Errorf("title roundtrip: got %q", parsed["title"])
	}
}

func TestExtractStructured_PublisherFlattening(t *testing.T) {
	html := `<head>
		<title>News Article</title>
		<script type="application/ld+json">{
			"@type": "NewsArticle",
			"publisher": {"@type": "Organization", "name": "Daily News"}
		}</script>
	</head><body></body>`
	result := ExtractStructured(html, "https://example.com")
	if result["publisher"] != "Daily News" {
		t.Errorf("publisher: got %v, want 'Daily News'", result["publisher"])
	}
}

func TestApplyEntityExtraction_StringFields(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head><title>Profile Page</title></head>
<body>
	<main>
		<h1 class="fullname">John Doe</h1>
		<span class="username">@johndoe</span>
		<p class="bio">Software engineer building cool things</p>
	</main>
</body>
</html>`

	rules := &schema.ExtractionRules{
		PageType:        "profile",
		ContentSelector: "main",
		TitleSelector:   "h1.fullname",
		EntityFields: []schema.EntityField{
			{Name: "name", Selector: "h1.fullname", Type: "string"},
			{Name: "username", Selector: ".username", Type: "string"},
			{Name: "bio", Selector: ".bio", Type: "string"},
		},
	}

	result := ApplyEntityExtraction(html, rules)

	if result["title"] != "John Doe" {
		t.Errorf("title: got %q", result["title"])
	}
	if result["type"] != "profile" {
		t.Errorf("type: got %q", result["type"])
	}
	if result["name"] != "John Doe" {
		t.Errorf("name: got %q", result["name"])
	}
	if result["username"] != "@johndoe" {
		t.Errorf("username: got %q", result["username"])
	}
	if result["bio"] != "Software engineer building cool things" {
		t.Errorf("bio: got %q", result["bio"])
	}
}

func TestApplyEntityExtraction_NumberField(t *testing.T) {
	html := `<body>
		<span class="followers">1,234</span>
		<span class="score" data-value="4.5">4.5 / 5</span>
	</body>`

	rules := &schema.ExtractionRules{
		ContentSelector: "body",
		EntityFields: []schema.EntityField{
			{Name: "followers", Selector: ".followers", Type: "number"},
			{Name: "score", Selector: ".score", Attribute: "data-value", Type: "number"},
		},
	}

	result := ApplyEntityExtraction(html, rules)

	if result["followers"] != int64(1234) {
		t.Errorf("followers: got %v (%T)", result["followers"], result["followers"])
	}
	if result["score"] != float64(4.5) {
		t.Errorf("score: got %v (%T)", result["score"], result["score"])
	}
}

func TestApplyEntityExtraction_ListField(t *testing.T) {
	html := `<body>
		<ul class="repos">
			<li class="repo-item">
				<a class="repo-name">hermai</a>
				<span class="lang">Go</span>
				<span class="stars">42</span>
			</li>
			<li class="repo-item">
				<a class="repo-name">codirigent</a>
				<span class="lang">Rust</span>
				<span class="stars">71</span>
			</li>
		</ul>
	</body>`

	rules := &schema.ExtractionRules{
		ContentSelector: "body",
		EntityFields: []schema.EntityField{
			{
				Name:     "repos",
				Selector: "li.repo-item",
				Type:     "list",
				ItemFields: []schema.EntityField{
					{Name: "name", Selector: "a.repo-name", Type: "string"},
					{Name: "language", Selector: ".lang", Type: "string"},
					{Name: "stars", Selector: ".stars", Type: "number"},
				},
			},
		},
	}

	result := ApplyEntityExtraction(html, rules)

	repos, ok := result["repos"].([]any)
	if !ok {
		t.Fatalf("repos: expected []any, got %T", result["repos"])
	}
	if len(repos) != 2 {
		t.Fatalf("repos: got %d items, want 2", len(repos))
	}

	first, ok := repos[0].(map[string]any)
	if !ok {
		t.Fatalf("repos[0]: expected map[string]any, got %T", repos[0])
	}
	if first["name"] != "hermai" {
		t.Errorf("repos[0].name: got %q", first["name"])
	}
	if first["language"] != "Go" {
		t.Errorf("repos[0].language: got %q", first["language"])
	}
	if first["stars"] != int64(42) {
		t.Errorf("repos[0].stars: got %v (%T)", first["stars"], first["stars"])
	}

	second := repos[1].(map[string]any)
	if second["name"] != "codirigent" {
		t.Errorf("repos[1].name: got %q", second["name"])
	}
}

func TestApplyEntityExtraction_ListWithoutSubFields(t *testing.T) {
	html := `<body>
		<ul>
			<li class="tag">Go</li>
			<li class="tag">Rust</li>
			<li class="tag">TypeScript</li>
		</ul>
	</body>`

	rules := &schema.ExtractionRules{
		ContentSelector: "body",
		EntityFields: []schema.EntityField{
			{Name: "tags", Selector: "li.tag", Type: "list"},
		},
	}

	result := ApplyEntityExtraction(html, rules)

	tags, ok := result["tags"].([]any)
	if !ok {
		t.Fatalf("tags: expected []any, got %T", result["tags"])
	}
	if len(tags) != 3 {
		t.Fatalf("tags: got %d items, want 3", len(tags))
	}
	if tags[0] != "Go" {
		t.Errorf("tags[0]: got %q", tags[0])
	}
}

func TestApplyEntityExtraction_IgnoreSelectors(t *testing.T) {
	html := `<body>
		<nav><span class="name">Nav Name</span></nav>
		<main>
			<span class="name">Main Name</span>
		</main>
	</body>`

	rules := &schema.ExtractionRules{
		ContentSelector: "body",
		IgnoreSelectors: []string{"nav"},
		EntityFields: []schema.EntityField{
			{Name: "name", Selector: ".name", Type: "string"},
		},
	}

	result := ApplyEntityExtraction(html, rules)

	if result["name"] != "Main Name" {
		t.Errorf("name: got %q, want 'Main Name' (nav should be ignored)", result["name"])
	}
}

func TestApplyEntityExtraction_MissingSelectors(t *testing.T) {
	html := `<body><p>Nothing here</p></body>`

	rules := &schema.ExtractionRules{
		ContentSelector: "body",
		EntityFields: []schema.EntityField{
			{Name: "missing", Selector: ".nonexistent", Type: "string"},
			{Name: "also_missing", Selector: ".nope", Type: "number"},
			{Name: "empty_list", Selector: ".nothing", Type: "list"},
		},
	}

	result := ApplyEntityExtraction(html, rules)

	if _, ok := result["missing"]; ok {
		t.Error("missing field should not be in result")
	}
	if _, ok := result["also_missing"]; ok {
		t.Error("also_missing field should not be in result")
	}
	if _, ok := result["empty_list"]; ok {
		t.Error("empty_list field should not be in result")
	}
}

func TestApplyEntityExtraction_AttributeExtraction(t *testing.T) {
	html := `<body>
		<img class="avatar" src="https://example.com/avatar.png" alt="User Avatar">
		<a class="website" href="https://example.com">My Site</a>
	</body>`

	rules := &schema.ExtractionRules{
		ContentSelector: "body",
		EntityFields: []schema.EntityField{
			{Name: "avatar", Selector: "img.avatar", Attribute: "src", Type: "string"},
			{Name: "website", Selector: "a.website", Attribute: "href", Type: "string"},
		},
	}

	result := ApplyEntityExtraction(html, rules)

	if result["avatar"] != "https://example.com/avatar.png" {
		t.Errorf("avatar: got %q", result["avatar"])
	}
	if result["website"] != "https://example.com" {
		t.Errorf("website: got %q", result["website"])
	}
}

func TestApplyEntityExtraction_EmptyHTML(t *testing.T) {
	rules := &schema.ExtractionRules{
		ContentSelector: "body",
		EntityFields: []schema.EntityField{
			{Name: "test", Selector: ".test", Type: "string"},
		},
	}

	result := ApplyEntityExtraction("", rules)

	// Should return a map (not nil) but with no entity fields
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestCleanNumberString_Variants(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"1,234", "1234"},
		{"$29.99", "29.99"},
		{"1.2k", "1200"},
		{"2.5m", "2500000"},
		{"  42  ", "42"},
		{"", ""},
		{"-5", "-5"},
	}

	for _, tt := range tests {
		got := cleanNumberString(tt.input)
		if got != tt.want {
			t.Errorf("cleanNumberString(%q): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestInferPageType_ArticleElement(t *testing.T) {
	html := `<html><body><article><p>Content</p></article></body></html>`
	page := Extract(html, "https://example.com")
	result := ExtractStructured(html, "https://example.com")
	_ = page
	if result["type"] != "article" {
		t.Errorf("type: got %q, want 'article'", result["type"])
	}
}

func TestMapSchemaOrgType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Article", "article"},
		{"BlogPosting", "article"},
		{"NewsArticle", "article"},
		{"Person", "profile"},
		{"ProfilePage", "profile"},
		{"Product", "product"},
		{"ItemList", "listing"},
		{"WebSite", "generic"},
		{"Organization", "generic"},
	}

	for _, tt := range tests {
		got := mapSchemaOrgType(tt.input)
		if got != tt.want {
			t.Errorf("mapSchemaOrgType(%q): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestApplyEntityExtraction_NumberWithKSuffix(t *testing.T) {
	html := `<body><span class="count">1.2k</span></body>`

	rules := &schema.ExtractionRules{
		ContentSelector: "body",
		EntityFields: []schema.EntityField{
			{Name: "count", Selector: ".count", Type: "number"},
		},
	}

	result := ApplyEntityExtraction(html, rules)

	if result["count"] != int64(1200) {
		t.Errorf("count: got %v (%T), want 1200", result["count"], result["count"])
	}
}

func TestExtractNextDataPayload_WithGdpClientCache(t *testing.T) {
	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"componentId":    "detail",
				"gdpClientCache": `{"zpid123":{"price":500000,"bedrooms":3,"bathrooms":2,"address":"123 Main St","zestimate":480000}}`,
			},
		},
	}
	result := extractNextDataPayload(nextData)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result["price"] != float64(500000) {
		t.Errorf("price: got %v, want 500000", result["price"])
	}
	if result["bedrooms"] != float64(3) {
		t.Errorf("bedrooms: got %v, want 3", result["bedrooms"])
	}
	if result["componentId"] != "detail" {
		t.Errorf("componentId should be preserved, got %v", result["componentId"])
	}
	if _, has := result["gdpClientCache"]; has {
		t.Error("raw gdpClientCache string should be removed")
	}
}

func TestExtractNextDataPayload_GdpClientCacheInvalidJSON(t *testing.T) {
	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"componentId":    "detail",
				"gdpClientCache": "not valid json {{{",
			},
		},
	}
	result := extractNextDataPayload(nextData)
	if result == nil {
		t.Fatal("expected non-nil result (fallback to pageProps)")
	}
	// Should fall back to raw pageProps including the unparseable string
	if result["gdpClientCache"] != "not valid json {{{" {
		t.Error("invalid gdpClientCache should be returned as-is in fallback")
	}
}

func TestSurfacePagination_ZillowSearch(t *testing.T) {
	result := map[string]any{
		"searchPageState": map[string]any{
			"cat1": map[string]any{
				"searchResults": map[string]any{
					"listResults": []any{"listing1", "listing2"},
				},
				"searchList": map[string]any{
					"totalResultCount": float64(229),
					"totalPages":       float64(6),
					"pagination": map[string]any{
						"nextUrl": "/san-francisco-ca/rentals/5-_beds/2_p/",
					},
				},
			},
		},
	}
	surfacePagination(result, "https://www.zillow.com/san-francisco-ca/rentals/5-_beds/")

	pag, ok := result["_pagination"].(map[string]any)
	if !ok {
		t.Fatal("expected _pagination in result")
	}
	if pag["total_results"] != 229 {
		t.Errorf("total_results: got %v, want 229", pag["total_results"])
	}
	if pag["total_pages"] != 6 {
		t.Errorf("total_pages: got %v, want 6", pag["total_pages"])
	}
	nextURL, _ := pag["next_url"].(string)
	if nextURL != "https://www.zillow.com/san-francisco-ca/rentals/5-_beds/2_p/" {
		t.Errorf("next_url: got %q", nextURL)
	}
}

func TestSurfacePagination_NonZillow(t *testing.T) {
	result := map[string]any{
		"title":    "Some Page",
		"otherKey": "value",
	}
	surfacePagination(result, "https://example.com/page")
	if _, has := result["_pagination"]; has {
		t.Error("should not add _pagination for non-Zillow data")
	}
}

func TestExtractNextDataPayload_NoGdpClientCache(t *testing.T) {
	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"searchPageState": map[string]any{"results": []any{1, 2, 3}},
				"otherProp":       "value",
			},
		},
	}
	result := extractNextDataPayload(nextData)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result["otherProp"] != "value" {
		t.Errorf("otherProp: got %v, want 'value'", result["otherProp"])
	}
}

func TestApplyEntityExtraction_FallbackTitle(t *testing.T) {
	html := `<html>
<head><title>Page Title</title></head>
<body><p>Content</p></body>
</html>`

	rules := &schema.ExtractionRules{
		PageType:        "generic",
		ContentSelector: "body",
		TitleSelector:   ".nonexistent-title",
	}

	result := ApplyEntityExtraction(html, rules)

	if result["title"] != "Page Title" {
		t.Errorf("title: got %q, want 'Page Title'", result["title"])
	}
}
