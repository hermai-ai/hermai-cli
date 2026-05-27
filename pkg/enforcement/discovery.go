package enforcement

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/pdftext"
	"golang.org/x/net/html"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type SourceDocument struct {
	State       string `json:"state"`
	Agency      string `json:"agency"`
	URL         string `json:"url"`
	Label       string `json:"label"`
	ReportMonth string `json:"report_month,omitempty"`
	ContentType string `json:"content_type"`
}

type Corpus struct {
	State                 string           `json:"state"`
	Agency                string           `json:"agency"`
	RootURL               string           `json:"root_url"`
	ParserProfile         string           `json:"parser_profile"`
	DiscoveredSourceCount int              `json:"discovered_source_count"`
	SelectedSourceCount   int              `json:"selected_source_count"`
	ProcessedSourceCount  int              `json:"processed_source_count"`
	Sources               []SourceDocument `json:"sources"`
	ReportStats           []ReportStats    `json:"report_stats,omitempty"`
	Results               []Action         `json:"results"`
	DeterministicChecks   map[string]any   `json:"deterministic_checks"`
	ExtractionWarnings    []string         `json:"extraction_warnings,omitempty"`
}

type ReportStats struct {
	SourceURL                  string         `json:"source_url"`
	ReportMonth                string         `json:"report_month,omitempty"`
	PageCount                  int            `json:"page_count,omitempty"`
	OCRRequired                bool           `json:"ocr_required,omitempty"`
	ActionCount                int            `json:"action_count"`
	WarningCount               int            `json:"warning_count"`
	MissingCaseNumberCount     int            `json:"missing_case_number_count"`
	EmptyProfessionGroupCount  int            `json:"empty_profession_group_count"`
	ShortDescriptionCount      int            `json:"short_description_count"`
	ProfessionGroupCounts      map[string]int `json:"profession_group_counts,omitempty"`
	ExtractionWarningSummaries []string       `json:"extraction_warning_summaries,omitempty"`
}

var (
	monthLabelRe       = regexp.MustCompile(`(?i)\b(January|February|March|April|May|June|July|August|September|October|November|December)\s+(\d{4})\b`)
	illinoisReportPath = regexp.MustCompile(`(?i)/discpln/\d{4}-\d{2}enf\.pdf$`)
	titleCaser         = cases.Title(language.English)
)

func DiscoverIllinoisSources(rootURL string, body []byte) ([]SourceDocument, error) {
	base, err := url.Parse(rootURL)
	if err != nil {
		return nil, fmt.Errorf("parse root URL: %w", err)
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse root HTML: %w", err)
	}

	var sources []SourceDocument
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			href := attr(n, "href")
			if href != "" {
				if u, ok := resolvePDF(base, href); ok {
					label := strings.TrimSpace(nodeText(n))
					sources = append(sources, SourceDocument{
						State:       "IL",
						Agency:      "Illinois Department of Financial and Professional Regulation",
						URL:         u,
						Label:       label,
						ReportMonth: parseReportMonth(label),
						ContentType: "application/pdf",
					})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	sort.SliceStable(sources, func(i, j int) bool {
		return sources[i].ReportMonth > sources[j].ReportMonth
	})
	return dedupeSources(sources), nil
}

func FetchIllinoisCorpus(ctx context.Context, client *http.Client, rootURL string, limit int, delay time.Duration) (*Corpus, error) {
	body, _, err := fetchBytes(ctx, client, rootURL)
	if err != nil {
		return nil, err
	}
	sources, err := DiscoverIllinoisSources(rootURL, body)
	if err != nil {
		return nil, err
	}
	discoveredCount := len(sources)
	if limit > 0 && limit < len(sources) {
		sources = sources[:limit]
	}

	corpus := &Corpus{
		State:                 "IL",
		Agency:                "Illinois Department of Financial and Professional Regulation",
		RootURL:               rootURL,
		ParserProfile:         "monthly_pdf_grouped_by_profession",
		DiscoveredSourceCount: discoveredCount,
		SelectedSourceCount:   len(sources),
		Sources:               sources,
		DeterministicChecks: map[string]any{
			"uses_llm_confidence":       false,
			"intended_refresh_strategy": "append_only_content_hashes",
		},
	}

	for i, source := range sources {
		pdfBytes, contentType, err := fetchBytes(ctx, client, source.URL)
		if err != nil {
			corpus.ExtractionWarnings = append(corpus.ExtractionWarnings, fmt.Sprintf("%s: %v", source.URL, err))
			corpus.ReportStats = append(corpus.ReportStats, ReportStats{
				SourceURL:                  source.URL,
				ReportMonth:                source.ReportMonth,
				WarningCount:               1,
				ExtractionWarningSummaries: []string{err.Error()},
			})
			continue
		}
		if !pdftext.IsPDF(pdfBytes) {
			warning := fmt.Sprintf("expected PDF response, got content-type %q", contentType)
			corpus.ExtractionWarnings = append(corpus.ExtractionWarnings, fmt.Sprintf("%s: %s", source.URL, warning))
			corpus.ReportStats = append(corpus.ReportStats, ReportStats{
				SourceURL:                  source.URL,
				ReportMonth:                source.ReportMonth,
				WarningCount:               1,
				ExtractionWarningSummaries: []string{warning},
			})
			continue
		}
		doc, err := extractPDF(ctx, pdfBytes)
		if err != nil {
			corpus.ExtractionWarnings = append(corpus.ExtractionWarnings, fmt.Sprintf("%s: %v", source.URL, err))
			corpus.ReportStats = append(corpus.ReportStats, ReportStats{
				SourceURL:                  source.URL,
				ReportMonth:                source.ReportMonth,
				WarningCount:               1,
				ExtractionWarningSummaries: []string{err.Error()},
			})
			continue
		}
		result := ExtractIllinois(doc, Options{SourceURL: source.URL, ReportMonth: source.ReportMonth})
		corpus.Results = append(corpus.Results, result.Actions...)
		corpus.ReportStats = append(corpus.ReportStats, buildReportStats(source, doc, result))
		corpus.ProcessedSourceCount++
		if delay > 0 && i < len(sources)-1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	corpus.DeterministicChecks["action_count"] = len(corpus.Results)
	return corpus, nil
}

func buildReportStats(source SourceDocument, doc *pdftext.Document, result Result) ReportStats {
	stats := ReportStats{
		SourceURL:             source.URL,
		ReportMonth:           source.ReportMonth,
		PageCount:             doc.PageCount,
		OCRRequired:           doc.OCRRequired,
		ActionCount:           len(result.Actions),
		ProfessionGroupCounts: map[string]int{},
	}
	stats.WarningCount += len(result.ExtractionWarnings)
	stats.ExtractionWarningSummaries = append(stats.ExtractionWarningSummaries, result.ExtractionWarnings...)
	for _, action := range result.Actions {
		if len(action.CaseNumbers) == 0 {
			stats.MissingCaseNumberCount++
		}
		if action.ProfessionGroupRaw == "" {
			stats.EmptyProfessionGroupCount++
		}
		if len(action.CaseDescription) < 20 {
			stats.ShortDescriptionCount++
		}
		if len(action.ExtractionWarnings) > 0 {
			stats.WarningCount += len(action.ExtractionWarnings)
			stats.ExtractionWarningSummaries = append(stats.ExtractionWarningSummaries, action.ExtractionWarnings...)
		}
		if action.ProfessionGroupRaw != "" {
			stats.ProfessionGroupCounts[action.ProfessionGroupRaw]++
		}
	}
	if len(stats.ProfessionGroupCounts) == 0 {
		stats.ProfessionGroupCounts = nil
	}
	return stats
}

var extractPDF = func(ctx context.Context, data []byte) (*pdftext.Document, error) {
	return pdftext.Extract(ctx, data)
}

func fetchBytes(ctx context.Context, client *http.Client, target string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Hermai CLI (+https://hermai.ai)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch %s: %w", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, resp.Header.Get("Content-Type"), fmt.Errorf("fetch %s: HTTP %d", target, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return nil, resp.Header.Get("Content-Type"), err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func resolvePDF(base *url.URL, href string) (string, bool) {
	u, err := base.Parse(href)
	if err != nil {
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(u.Path), ".pdf") {
		return "", false
	}
	if !illinoisReportPath.MatchString(u.Path) {
		return "", false
	}
	return u.String(), true
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		if cur.Type == html.TextNode {
			b.WriteString(cur.Data)
			b.WriteByte(' ')
		}
		for c := cur.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func parseReportMonth(label string) string {
	match := monthLabelRe.FindStringSubmatch(label)
	if match == nil {
		return ""
	}
	t, err := time.Parse("January 2006", titleCaser.String(strings.ToLower(match[1]))+" "+match[2])
	if err != nil {
		return ""
	}
	return t.Format("2006-01")
}

func dedupeSources(in []SourceDocument) []SourceDocument {
	seen := map[string]bool{}
	out := make([]SourceDocument, 0, len(in))
	for _, src := range in {
		if seen[src.URL] {
			continue
		}
		seen[src.URL] = true
		out = append(out, src)
	}
	return out
}
