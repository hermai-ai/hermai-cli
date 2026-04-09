package analyzer

import (
	"strings"
	"testing"

	"github.com/hermai-ai/hermai-cli/pkg/browser"
)

func TestSystemPrompt_MentionsAPIAndJSON(t *testing.T) {
	prompt := SystemPrompt()
	if !strings.Contains(prompt, "API") {
		t.Error("SystemPrompt should mention API")
	}
	if !strings.Contains(prompt, "JSON") {
		t.Error("SystemPrompt should mention JSON")
	}
}

func TestBuildPrompt_IncludesOriginalURL(t *testing.T) {
	har := &browser.HARLog{}
	prompt := BuildPrompt(har, "<html></html>", "https://example.com/page")
	if !strings.Contains(prompt, "https://example.com/page") {
		t.Error("BuildPrompt should include original URL")
	}
}

func TestBuildPrompt_IncludesDOMContent(t *testing.T) {
	har := &browser.HARLog{}
	dom := "<div>Hello World</div>"
	prompt := BuildPrompt(har, dom, "https://example.com")
	if !strings.Contains(prompt, "Hello World") {
		t.Error("BuildPrompt should include DOM content")
	}
}

func TestBuildPrompt_IncludesAPIURL(t *testing.T) {
	har := &browser.HARLog{
		Entries: []browser.HAREntry{
			{
				Request: browser.HARRequest{
					Method: "GET",
					URL:    "https://api.example.com/data",
					Headers: map[string]string{
						"Accept": "application/json",
					},
				},
				Response: browser.HARResponse{
					Status:      200,
					ContentType: "application/json",
					Body:        `{"result": "ok"}`,
				},
			},
		},
	}
	prompt := BuildPrompt(har, "<html></html>", "https://example.com")
	if !strings.Contains(prompt, "https://api.example.com/data") {
		t.Error("BuildPrompt should include API URL from HAR entries")
	}
}

func TestBuildPrompt_TruncatesResponseBodies(t *testing.T) {
	longValue := strings.Repeat("x", 200)
	body := `{"data": "` + longValue + `"}`
	har := &browser.HARLog{
		Entries: []browser.HAREntry{
			{
				Request: browser.HARRequest{
					Method:  "GET",
					URL:     "https://api.example.com/data",
					Headers: map[string]string{},
				},
				Response: browser.HARResponse{
					Status:      200,
					ContentType: "application/json",
					Body:        body,
				},
			},
		},
	}
	prompt := BuildPrompt(har, "<html></html>", "https://example.com")
	if strings.Contains(prompt, longValue) {
		t.Error("BuildPrompt should truncate long response body values")
	}
	if !strings.Contains(prompt, "...") {
		t.Error("BuildPrompt should contain truncation indicator")
	}
}

func TestHTMLExtractionSystemPrompt_CommentsCountAndURL(t *testing.T) {
	prompt := HTMLExtractionSystemPrompt()
	if !strings.Contains(prompt, "comments_count") {
		t.Error("HTMLExtractionSystemPrompt should show comments_count as a separate field")
	}
	if !strings.Contains(prompt, "comments_url") {
		t.Error("HTMLExtractionSystemPrompt should show comments_url as a separate field")
	}
}

func TestHTMLExtractionSystemPrompt_NumericLinkRule(t *testing.T) {
	prompt := HTMLExtractionSystemPrompt()
	if !strings.Contains(prompt, "links with numeric text") {
		t.Error("HTMLExtractionSystemPrompt should contain explicit rule about numeric data in link text")
	}
}

func TestBuildPrompt_TruncatesDOMOver3000Chars(t *testing.T) {
	longDOM := strings.Repeat("a", 4000)
	har := &browser.HARLog{}
	prompt := BuildPrompt(har, longDOM, "https://example.com")
	if strings.Contains(prompt, longDOM) {
		t.Error("BuildPrompt should truncate DOM longer than 3000 chars")
	}
}
