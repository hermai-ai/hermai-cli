package enforcement

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/hermai-ai/hermai-cli/pkg/pdftext"
)

type Action struct {
	State                string               `json:"state"`
	Agency               string               `json:"agency"`
	SourceReportURL      string               `json:"source_report_url,omitempty"`
	SourceReportMonth    string               `json:"source_report_month,omitempty"`
	SourcePage           int                  `json:"source_page"`
	ProfessionGroupRaw   string               `json:"profession_group_raw"`
	ProfessionNormalized string               `json:"profession_normalized,omitempty"`
	DefendantName        string               `json:"defendant_name"`
	DefendantLocation    string               `json:"defendant_location"`
	CaseNumbers          []string             `json:"case_numbers"`
	LicenseTypeRaw       string               `json:"license_type_raw,omitempty"`
	DisciplinaryAction   string               `json:"disciplinary_action,omitempty"`
	CaseDescription      string               `json:"case_description"`
	RegulationReferences []string             `json:"regulation_references,omitempty"`
	SourceText           string               `json:"source_text"`
	SourceTextSHA256     string               `json:"source_text_sha256"`
	VerificationSignals  []string             `json:"verification_signals"`
	ExtractionWarnings   []string             `json:"extraction_warnings,omitempty"`
	EntityResolutionHint EntityResolutionHint `json:"entity_resolution_hint,omitempty"`
}

type EntityResolutionHint struct {
	NameNormalized     string   `json:"name_normalized,omitempty"`
	LocationNormalized string   `json:"location_normalized,omitempty"`
	CaseNumbers        []string `json:"case_numbers,omitempty"`
}

type Result struct {
	State               string         `json:"state"`
	Agency              string         `json:"agency"`
	SourceReportURL     string         `json:"source_report_url,omitempty"`
	SourceReportMonth   string         `json:"source_report_month,omitempty"`
	ParserProfile       string         `json:"parser_profile"`
	DocumentPageCount   int            `json:"document_page_count"`
	Actions             []Action       `json:"results"`
	ExtractionWarnings  []string       `json:"extraction_warnings,omitempty"`
	DeterministicChecks map[string]any `json:"deterministic_checks"`
}

type Options struct {
	SourceURL   string
	ReportMonth string
}

var (
	headingRe     = regexp.MustCompile(`^[A-Z][A-Z0-9&,\- /()'.]+$`)
	caseNumberRe  = regexp.MustCompile(`\b[A-Za-z0-9]*\d{6,}[A-Za-z0-9]*(?:-\d+)?\b`)
	statuteRefRe  = regexp.MustCompile(`\b(?:\d{3}\s+ILCS\s+\d+(?:/[0-9A-Za-z.\-]+)?|Section\s+[0-9A-Za-z.\-]+(?:\s+of\s+the\s+[A-Z][A-Za-z ,&.-]+ Act)?)\b`)
	footerNoiseRe = regexp.MustCompile(`^(JB PRITZKER|MARIO TRETO,? JR\.?|Governor|Secretary|NEWS|DIVISION OF PROFESSIONAL REGULATION)(\s+.*)?$`)
	dotLeaderRe   = regexp.MustCompile(`\.{4,}\s*[A-Za-z0-9-]*\d{6,}[A-Za-z0-9-]*$`)
	pageNoiseRe   = regexp.MustCompile(`^(#+|-?\s*\d+\s*-?)$`)
)

type parsedCaseStart struct {
	defendantName     string
	defendantLocation string
	caseNumbers       []string
	caseDescription   string
	sourceText        string
}

func ExtractIllinois(doc *pdftext.Document, opts Options) Result {
	result := Result{
		State:             "IL",
		Agency:            "Illinois Department of Financial and Professional Regulation",
		SourceReportURL:   opts.SourceURL,
		SourceReportMonth: opts.ReportMonth,
		ParserProfile:     "monthly_pdf_grouped_by_profession",
		DocumentPageCount: doc.PageCount,
		DeterministicChecks: map[string]any{
			"case_number_pattern": `\b[A-Za-z0-9]*\d{6,}[A-Za-z0-9]*\b`,
			"uses_llm_confidence": false,
		},
	}

	var currentGroup string
	var current *Action
	for _, page := range doc.Pages {
		lines := cleanLines(page.Text)
		for i := 0; i < len(lines); i++ {
			line := lines[i]
			if line == "" {
				continue
			}
			if footerNoiseRe.MatchString(line) || isPageNoise(line) {
				continue
			}
			if isHeading(line) {
				if current != nil {
					result.Actions = append(result.Actions, finalize(*current))
					current = nil
				}
				currentGroup = line
				continue
			}
			if parsed, consumed := matchCaseStart(lines, i); parsed != nil {
				if current != nil {
					result.Actions = append(result.Actions, finalize(*current))
				}
				current = &Action{
					State:                result.State,
					Agency:               result.Agency,
					SourceReportURL:      opts.SourceURL,
					SourceReportMonth:    opts.ReportMonth,
					SourcePage:           page.Number,
					ProfessionGroupRaw:   currentGroup,
					ProfessionNormalized: normalizeProfession(currentGroup),
					DefendantName:        parsed.defendantName,
					DefendantLocation:    parsed.defendantLocation,
					CaseNumbers:          parsed.caseNumbers,
					CaseDescription:      parsed.caseDescription,
					SourceText:           parsed.sourceText,
				}
				i += consumed - 1
				continue
			}
			if isDotLeaderRow(line) {
				continue
			}
			if current != nil {
				current.CaseDescription = strings.TrimSpace(current.CaseDescription + " " + line)
				current.SourceText = strings.TrimSpace(current.SourceText + "\n" + line)
			}
		}
	}
	if current != nil {
		result.Actions = append(result.Actions, finalize(*current))
	}

	result.DeterministicChecks["action_count"] = len(result.Actions)
	result.DeterministicChecks["actions_with_case_number"] = countWith(result.Actions, func(a Action) bool { return len(a.CaseNumbers) > 0 })
	result.DeterministicChecks["actions_with_source_page"] = countWith(result.Actions, func(a Action) bool { return a.SourcePage > 0 })
	return result
}

func cleanLines(text string) []string {
	rawLines := strings.Split(text, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, raw := range rawLines {
		lines = append(lines, cleanLine(raw))
	}
	return lines
}

func cleanLine(line string) string {
	line = strings.Join(strings.Fields(line), " ")
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "DIVISION OF PROFESSIONAL REGULATION")
	return strings.TrimSpace(line)
}

func isHeading(line string) bool {
	if len(line) < 3 || len(line) > 90 {
		return false
	}
	if strings.Contains(line, " - ") {
		return false
	}
	if strings.Contains(line, "Enforcement Actions") || strings.HasPrefix(line, "SPRINGFIELD") {
		return false
	}
	if line == "UNLICENSED" {
		return true
	}
	// Illinois-specific guard: these words commonly appear as all-caps
	// continuation text, not board/profession headings, in IDFPR reports.
	for _, word := range []string{"LICENSE", "SUSPENDED", "PROBATION", "FAILURE", "RESPONDENT", "VIOLATION", "CONVICTION", "PRACTICE", "ORDER"} {
		if strings.Contains(line, word) {
			return false
		}
	}
	return headingRe.MatchString(line)
}

func matchCaseStart(lines []string, start int) (*parsedCaseStart, int) {
	var candidate string
	for end := start; end < len(lines) && end < start+3; end++ {
		line := lines[end]
		if line == "" || isHeading(line) || footerNoiseRe.MatchString(line) {
			break
		}
		if end == start && isDotLeaderRow(line) {
			return nil, 0
		}
		if candidate == "" {
			candidate = line
		} else {
			candidate += " " + line
		}
		if parsed := parseCaseStart(candidate); parsed != nil {
			parsed.sourceText = strings.Join(lines[start:end+1], "\n")
			return parsed, end - start + 1
		}
	}
	return nil, 0
}

func parseCaseStart(line string) *parsedCaseStart {
	left, desc, ok := splitCaseStart(line)
	if !ok {
		return nil
	}
	name, location, caseNumbers, ok := parseCaseLeft(left)
	if !ok {
		return nil
	}
	caseNumbers = unique(append(caseNumbers, extractCaseNumbers(desc)...))
	if len(caseNumbers) == 0 && !strings.Contains(strings.ToLower(line), "unlicensed") {
		return nil
	}
	return &parsedCaseStart{
		defendantName:     name,
		defendantLocation: location,
		caseNumbers:       caseNumbers,
		caseDescription:   desc,
	}
}

func splitCaseStart(line string) (string, string, bool) {
	separators := []string{" - ", " – "}
	for _, sep := range separators {
		if idx := strings.Index(line, sep); idx >= 0 {
			left := strings.TrimSpace(line[:idx])
			desc := strings.TrimSpace(line[idx+len(sep):])
			if left != "" {
				return left, desc, true
			}
		}
	}
	return "", "", false
}

func parseCaseLeft(left string) (string, string, []string, bool) {
	parts := strings.Split(left, ",")
	if len(parts) < 2 {
		return "", "", nil, false
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	nameEnd := 1
	if len(parts) > 2 && isBusinessSuffix(parts[1]) {
		nameEnd = 2
	}

	name := strings.Join(parts[:nameEnd], ", ")
	if name == "" {
		return "", "", nil, false
	}

	caseNumbers := extractCaseNumbers(left)
	last := strings.ToLower(parts[len(parts)-1])
	tokenIsCase := len(extractCaseNumbers(parts[len(parts)-1])) > 0
	tokenIsUnlicensed := strings.EqualFold(last, "unlicensed")
	if tokenIsCase || tokenIsUnlicensed {
		location := strings.Join(parts[nameEnd:len(parts)-1], ", ")
		if location == "" {
			return "", "", nil, false
		}
		return name, location, caseNumbers, true
	}

	location := strings.Join(parts[nameEnd:], ", ")
	if location == "" {
		return "", "", nil, false
	}
	return name, location, caseNumbers, true
}

func isBusinessSuffix(s string) bool {
	normalized := strings.Trim(strings.ToUpper(strings.TrimSpace(s)), ".")
	switch normalized {
	case "CO", "CORP", "CORPORATION", "INC", "LLC", "L.L.C", "LTD", "LP", "L.P", "PC", "P.C", "PLLC", "P.L.L.C":
		return true
	default:
		return false
	}
}

func isDotLeaderRow(line string) bool {
	return dotLeaderRe.MatchString(line)
}

func isPageNoise(line string) bool {
	return pageNoiseRe.MatchString(strings.TrimSpace(line))
}

func finalize(a Action) Action {
	a.CaseDescription = strings.TrimSpace(a.CaseDescription)
	a.SourceText = strings.TrimSpace(a.SourceText)
	a.CaseNumbers = unique(append(a.CaseNumbers, caseNumberRe.FindAllString(a.CaseDescription, -1)...))
	if a.CaseNumbers == nil {
		a.CaseNumbers = []string{}
	}
	if len(a.CaseNumbers) > 0 || !strings.Contains(strings.ToLower(a.SourceText), "unlicensed") {
		a.LicenseTypeRaw = inferLicenseType(a.CaseDescription)
	}
	a.DisciplinaryAction = inferDisciplinaryAction(a.CaseDescription)
	a.RegulationReferences = unique(statuteRefRe.FindAllString(a.CaseDescription, -1))
	a.SourceTextSHA256 = hash(a.SourceText)
	a.EntityResolutionHint = entityHint(a)

	if len(a.CaseNumbers) > 0 {
		a.VerificationSignals = append(a.VerificationSignals, "case_number_pattern_match")
	} else if strings.Contains(strings.ToLower(a.SourceText), "unlicensed") {
		a.VerificationSignals = append(a.VerificationSignals, "unlicensed_without_case_number")
	} else {
		a.ExtractionWarnings = append(a.ExtractionWarnings, "missing_or_unrecognized_case_number")
	}
	if a.DefendantName != "" && !strings.Contains(a.DefendantName, "  ") {
		a.VerificationSignals = append(a.VerificationSignals, "defendant_name_parsed")
	}
	if a.SourcePage > 0 {
		a.VerificationSignals = append(a.VerificationSignals, "source_page_present")
	}
	if a.ProfessionNormalized != "" {
		a.VerificationSignals = append(a.VerificationSignals, "profession_normalized")
	}
	return a
}

func extractCaseNumbers(s string) []string {
	return unique(caseNumberRe.FindAllString(s, -1))
}

func inferLicenseType(desc string) string {
	lower := strings.ToLower(desc)
	idx := licenseWordIndex(lower)
	if idx == -1 {
		if strings.Contains(lower, "registration card") {
			return "registration card"
		}
		return ""
	}
	start := strings.LastIndex(lower[:idx], " ")
	if start == -1 {
		start = 0
	}
	frag := strings.TrimSpace(desc[start : idx+len("license")])
	return strings.Trim(frag, " ,.;")
}

func licenseWordIndex(lower string) int {
	offset := 0
	for {
		rel := strings.Index(lower[offset:], "license")
		if rel == -1 {
			return -1
		}
		idx := offset + rel
		if idx > 0 && lower[idx-1] == 'n' {
			offset = idx + len("license")
			continue
		}
		return idx
	}
}

func inferDisciplinaryAction(desc string) string {
	lower := strings.ToLower(desc)
	switch {
	case strings.Contains(lower, "permanently surrendered"):
		return "permanent surrender"
	case strings.Contains(lower, "temporarily suspended"):
		return "temporary suspension"
	case strings.Contains(lower, "indefinitely suspended"):
		return "indefinite suspension"
	case strings.Contains(lower, "suspended"):
		return "suspension"
	case strings.Contains(lower, "probation"):
		return "probation"
	case strings.Contains(lower, "reprimanded"):
		return "reprimand"
	case strings.Contains(lower, "refuse to renew"):
		return "refuse to renew"
	default:
		return ""
	}
}

func normalizeProfession(group string) string {
	g := strings.ToUpper(strings.TrimSpace(group))
	switch {
	case strings.Contains(g, "DENT"):
		return "dentistry"
	case strings.Contains(g, "MEDICAL"):
		return "medicine"
	case strings.Contains(g, "NURS"):
		return "nursing"
	case strings.Contains(g, "PHARM"):
		return "pharmacy"
	case strings.Contains(g, "BEHAVIOR ANALYST"):
		return "behavioral_analysis"
	case strings.Contains(g, "COSMETOLOGY") || strings.Contains(g, "BARBER"):
		return "barber_cosmetology_esthetics_hair_braiding_nail_technology"
	case strings.Contains(g, "PRIVATE DETECTIVE") || strings.Contains(g, "PRIVATE SECURITY"):
		return "private_detective_alarm_security_locksmith"
	default:
		return strings.ToLower(strings.ReplaceAll(g, " ", "_"))
	}
}

func entityHint(a Action) EntityResolutionHint {
	return EntityResolutionHint{
		NameNormalized:     strings.ToLower(a.DefendantName),
		LocationNormalized: strings.ToLower(a.DefendantLocation),
		CaseNumbers:        append([]string(nil), a.CaseNumbers...),
	}
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func countWith(actions []Action, pred func(Action) bool) int {
	n := 0
	for _, a := range actions {
		if pred(a) {
			n++
		}
	}
	return n
}
