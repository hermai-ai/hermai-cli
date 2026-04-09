package actions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

type stubCache struct {
	api *schema.Schema
	css *schema.Schema
}

func (s *stubCache) Lookup(context.Context, string) (*schema.Schema, error) { return s.api, nil }
func (s *stubCache) LookupAll(context.Context, string) (*schema.Schema, *schema.Schema, error) {
	return s.api, s.css, nil
}
func (s *stubCache) Store(context.Context, *schema.Schema) error      { return nil }
func (s *stubCache) StoreIfNoAPI(context.Context, *schema.Schema) error { return nil }
func (s *stubCache) Invalidate(context.Context, string) error         { return nil }

func TestBuildCatalog_HybridActions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<form method="GET" action="/search">
				<input type="search" name="q">
				<input type="hidden" name="lang" value="en">
			</form>
			<a href="/items?page=2">Next</a>
		</body></html>`))
	}))
	defer server.Close()

	catalog, err := BuildCatalog(context.Background(), &stubCache{
		api: &schema.Schema{
			Domain:     "example.com",
			Coverage:   schema.SchemaCoverageComplete,
			CreatedAt:  time.Now(),
			SchemaType: schema.SchemaTypeAPI,
			Endpoints: []schema.Endpoint{
				{
					Name:        "search_products",
					Description: "Search products",
					Method:      "GET",
					URLTemplate: server.URL + "/api/search?q={query}",
					Variables:   []schema.Variable{{Name: "query", Source: "query", Pattern: "q"}},
					ResponseSchema: &schema.ResponseSchema{
						Type: "object",
						Fields: []schema.FieldSchema{
							{Name: "items", Type: "array"},
						},
					},
				},
			},
		},
	}, server.URL, DiscoverOptions{})
	if err != nil {
		t.Fatalf("BuildCatalog error: %v", err)
	}

	if catalog.Source != "hybrid" {
		t.Fatalf("expected hybrid source, got %s", catalog.Source)
	}
	if catalog.Coverage != schema.SchemaCoverageComplete {
		t.Fatalf("expected complete coverage, got %s", catalog.Coverage)
	}
	if len(catalog.Actions) < 4 {
		t.Fatalf("expected API + HTML actions, got %d", len(catalog.Actions))
	}

	names := make(map[string]bool)
	for _, action := range catalog.Actions {
		names[action.Name] = true
	}
	for _, want := range []string{"search_products", "navigate", "search", "paginate"} {
		if !names[want] {
			t.Fatalf("expected action %q in catalog", want)
		}
	}
}

func TestExecuteAction_SearchGET(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("q") != "ipad" {
			t.Fatalf("expected q=ipad, got %q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Search Results</title></head><body><a href="/search?page=2">Next</a></body></html>`))
	}))
	defer server.Close()

	result, err := ExecuteAction(context.Background(), server.URL, schema.Action{
		Name:        "search",
		Kind:        schema.ActionKindSearch,
		Transport:   schema.ActionTransportHTTPGet,
		Method:      "GET",
		URLTemplate: server.URL + "/search?q={q}",
		Params: []schema.ActionParam{
			{Name: "q", In: "query", Required: true},
		},
	}, map[string]string{"q": "ipad"}, HTTPOptions{})
	if err != nil {
		t.Fatalf("ExecuteAction error: %v", err)
	}
	if result.Source != "html_extraction" {
		t.Fatalf("expected html_extraction source, got %s", result.Source)
	}
	if result.Metadata.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", result.Metadata.StatusCode)
	}
	if len(result.NextActions) == 0 {
		t.Fatal("expected next actions")
	}
}

func TestBuildCatalog_SearchTemplatePreservesPlaceholders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><form method="GET" action="/search"><input type="search" name="q"></form></body></html>`))
	}))
	defer server.Close()

	catalog, err := BuildCatalog(context.Background(), &stubCache{}, server.URL+"/search?q=ipad", DiscoverOptions{})
	if err != nil {
		t.Fatalf("BuildCatalog error: %v", err)
	}

	for _, action := range catalog.Actions {
		if action.Name != "search" {
			continue
		}
		if action.URLTemplate != server.URL+"/search?q={query}" && action.URLTemplate != server.URL+"/search?q={q}" {
			t.Fatalf("expected readable placeholder in search template, got %q", action.URLTemplate)
		}
		if strings.Contains(action.URLTemplate, "%7B") {
			t.Fatalf("expected raw placeholder, got %q", action.URLTemplate)
		}
		return
	}

	t.Fatal("expected search action in catalog")
}

func TestExecuteAction_SearchGET_LegacyEncodedPlaceholder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("q") != "ipad pro" {
			t.Fatalf("expected q=ipad pro, got %q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Search Results</title></head><body>ok</body></html>`))
	}))
	defer server.Close()

	result, err := ExecuteAction(context.Background(), server.URL, schema.Action{
		Name:        "search",
		Kind:        schema.ActionKindSearch,
		Transport:   schema.ActionTransportHTTPGet,
		Method:      "GET",
		URLTemplate: server.URL + "/search?q=%7Bq%7D",
		Params: []schema.ActionParam{
			{Name: "q", In: "query", Required: true},
		},
	}, map[string]string{"q": "ipad pro"}, HTTPOptions{})
	if err != nil {
		t.Fatalf("ExecuteAction error: %v", err)
	}
	if result.Source != "html_extraction" {
		t.Fatalf("expected html_extraction source, got %s", result.Source)
	}
}

func TestBuildCatalog_DetectsUnderscoreSearchKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><h1>Results</h1></body></html>`))
	}))
	defer server.Close()

	catalog, err := BuildCatalog(context.Background(), &stubCache{}, server.URL+"/sch/i.html?_nkw=ipad", DiscoverOptions{})
	if err != nil {
		t.Fatalf("BuildCatalog error: %v", err)
	}

	for _, action := range catalog.Actions {
		if action.Name != "search" {
			continue
		}
		if action.URLTemplate != server.URL+"/sch/i.html?_nkw={query}" {
			t.Fatalf("expected _nkw search template, got %q", action.URLTemplate)
		}
		if hasActionNamed(catalog.Actions, "paginate") {
			t.Fatal("did not expect speculative paginate action without a page key")
		}
		return
	}

	t.Fatal("expected search action in catalog")
}

func hasActionNamed(actions []schema.Action, name string) bool {
	for _, action := range actions {
		if action.Name == name {
			return true
		}
	}
	return false
}

func TestDedupeActions_PrefersCanonicalSearchAction(t *testing.T) {
	actions := dedupeActions([]schema.Action{
		{
			Name:        "search",
			Kind:        schema.ActionKindSearch,
			Transport:   schema.ActionTransportHTTPGet,
			URLTemplate: "https://www.amazon.com/s/ref=nb_sb_noss?field-keywords={field-keywords}&url=search-alias%3Daps",
			Source:      "html_page",
			Confidence:  0.9,
			Params: []schema.ActionParam{
				{Name: "url", In: "query", Required: false},
				{Name: "field-keywords", In: "query", Required: true},
			},
		},
		{
			Name:        "search",
			Kind:        schema.ActionKindSearch,
			Transport:   schema.ActionTransportHTTPGet,
			URLTemplate: "https://www.amazon.com/s?k={query}",
			Source:      "url_pattern",
			Confidence:  0.82,
			Params: []schema.ActionParam{
				{Name: "query", In: "query", Required: true},
			},
		},
	})

	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
	if actions[0].Name != "search" {
		t.Fatalf("expected canonical search action first, got %q", actions[0].Name)
	}
	if actions[0].URLTemplate != "https://www.amazon.com/s?k={query}" {
		t.Fatalf("expected URL-pattern search to win, got %q", actions[0].URLTemplate)
	}
	if actions[1].Name != "search_2" {
		t.Fatalf("expected lower-quality duplicate to be renamed search_2, got %q", actions[1].Name)
	}
}
