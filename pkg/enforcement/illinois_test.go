package enforcement

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hermai-ai/hermai-cli/pkg/pdftext"
)

func TestDiscoverIllinoisSources(t *testing.T) {
	html := []byte(`
		<a href="/content/dam/soi/en/web/idfpr/forms/discpln/2026-02enf.pdf">February 2026</a>
		<a href="https://idfpr.illinois.gov/content/dam/soi/en/web/idfpr/forms/discpln/2026-01enf.pdf">January 2026</a>
		<a href="/content/dam/soi/en/web/idfpr/forms/complaints/how-to-file.pdf">Complaint PDF</a>
		<a href="/not-a-report.html">Ignore me</a>
	`)
	sources, err := DiscoverIllinoisSources("https://idfpr.illinois.gov/news/disciplines/discreports.html", html)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(sources))
	}
	if sources[0].ReportMonth != "2026-02" {
		t.Fatalf("first report month = %q, want 2026-02", sources[0].ReportMonth)
	}
	if !strings.HasPrefix(sources[0].URL, "https://idfpr.illinois.gov/") {
		t.Fatalf("source URL was not resolved: %s", sources[0].URL)
	}
}

func TestExtractIllinoisFromFixturePDF(t *testing.T) {
	data, err := os.ReadFile("../../testdata/illinois-2026-02enf.pdf")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdftext.Extract(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	result := ExtractIllinois(doc, Options{
		SourceURL:   "https://idfpr.illinois.gov/content/dam/soi/en/web/idfpr/forms/discpln/2026-02enf.pdf",
		ReportMonth: "2026-02",
	})
	if len(result.Actions) != 289 {
		t.Fatalf("actions len = %d, want 289", len(result.Actions))
	}
	if len(result.ExtractionWarnings) > 0 {
		t.Fatalf("result extraction warnings = %v, want none", result.ExtractionWarnings)
	}
	if strings.Contains(result.Actions[0].CaseDescription, "BARBER, COSMETOLOGY") {
		t.Fatal("profession heading leaked into first action description")
	}

	var found bool
	missingCaseNumbers := 0
	for i, action := range result.Actions {
		if len(action.CaseNumbers) == 0 {
			missingCaseNumbers++
			if action.CaseNumbers == nil {
				t.Fatalf("action %d case numbers = nil, want empty slice: %#v", i, action)
			}
			if action.LicenseTypeRaw != "" {
				t.Fatalf("action %d license type = %q, want empty for unlicensed action: %#v", i, action.LicenseTypeRaw, action)
			}
		}
		if action.ProfessionGroupRaw == "" {
			t.Fatalf("action %d has empty profession group: %#v", i, action)
		}
		if len(action.CaseDescription) < 20 {
			t.Fatalf("action %d has suspiciously short case description: %#v", i, action)
		}
		if len(action.ExtractionWarnings) > 0 {
			t.Fatalf("action %d warnings = %v, want none", i, action.ExtractionWarnings)
		}
		if action.DefendantName == "Anna Pelak" {
			found = true
			if action.DefendantLocation != "Palatine" {
				t.Errorf("location = %q, want Palatine", action.DefendantLocation)
			}
			if !equalStrings(action.CaseNumbers, []string{"019020825", "319010098"}) {
				t.Errorf("case numbers = %v, want both license numbers", action.CaseNumbers)
			}
			if action.ProfessionNormalized != "dentistry" {
				t.Errorf("profession = %q, want dentistry", action.ProfessionNormalized)
			}
			if action.SourcePage != 2 {
				t.Errorf("source page = %d, want 2", action.SourcePage)
			}
			if len(action.VerificationSignals) == 0 {
				t.Error("expected deterministic verification signals")
			}
			if action.SourceTextSHA256 == "" {
				t.Error("expected source text hash")
			}
		}
	}
	if !found {
		t.Fatal("did not find Anna Pelak enforcement action")
	}
	if missingCaseNumbers != 2 {
		t.Fatalf("missing case number count = %d, want 2 unlicensed actions", missingCaseNumbers)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
