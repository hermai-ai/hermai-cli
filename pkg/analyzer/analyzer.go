package analyzer

import (
	"context"

	"github.com/hermai-ai/hermai-cli/pkg/browser"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// Service defines the interface for analyzing captured browser traffic.
type Service interface {
	// Analyze examines HAR traffic + DOM to identify API endpoints.
	Analyze(ctx context.Context, har *browser.HARLog, dom string, originalURL string) (*schema.Schema, error)

	// Suggest uses LLM knowledge to suggest public API endpoints when
	// browser capture fails (auth wall, Cloudflare, empty HAR).
	Suggest(ctx context.Context, originalURL string, failureReason string) (*schema.Schema, error)

	// AnalyzeHTML examines raw HTML to identify CSS selectors for extracting
	// the main content. Returns extraction rules that can be cached and
	// replayed without LLM on subsequent visits.
	AnalyzeHTML(ctx context.Context, rawHTML string, originalURL string) (*schema.ExtractionRules, error)

	// AnalyzeNextDataPaths examines __NEXT_DATA__ pageProps and identifies
	// named extraction paths (e.g. "products" -> ".ssrQuery.hits") so that
	// subsequent fetches return targeted sub-trees instead of the full blob.
	AnalyzeNextDataPaths(ctx context.Context, pageProps map[string]any, originalURL string) (map[string]string, error)

	// Ask sends fetched data + a natural language prompt to the LLM and
	// returns a plain text answer. Uses the fast classify model.
	Ask(ctx context.Context, data string, prompt string) (string, error)
}
