package htmlext

import (
	"encoding/json"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type patternType int

const (
	scriptIDJSON     patternType = iota // <script id="X" type="application/json">JSON</script>
	varAssignment                       // var X = JSON;
	windowAssignment                    // window.X = JSON; or window['X'] = JSON;
)

// embeddedPattern describes one known embedded data pattern.
type embeddedPattern struct {
	Name string
	Type patternType
	Key  string
}

// knownPatterns lists all embedded script patterns the extractor recognizes.
// Order matters: first match for a given Name wins.
var knownPatterns = []embeddedPattern{
	// Script-ID patterns — JSON content inside <script id="X">
	{"__NEXT_DATA__", scriptIDJSON, "__NEXT_DATA__"},
	{"__UNIVERSAL_DATA_FOR_REHYDRATION__", scriptIDJSON, "__UNIVERSAL_DATA_FOR_REHYDRATION__"},
	{"__FRONTITY_CONNECT_STATE__", scriptIDJSON, "__FRONTITY_CONNECT_STATE__"},
	{"SIGI_STATE", scriptIDJSON, "SIGI_STATE"},
	{"__NUXT_DATA__", scriptIDJSON, "__NUXT_DATA__"},
	{"__MODERN_ROUTER_DATA__", scriptIDJSON, "__MODERN_ROUTER_DATA__"},

	// var X = JSON; — assignment to a bare variable
	{"ytInitialData", varAssignment, "ytInitialData"},
	{"ytInitialPlayerResponse", varAssignment, "ytInitialPlayerResponse"},

	// window.X = JSON; or window['X'] = JSON;
	{"SIGI_STATE", windowAssignment, "SIGI_STATE"},
	{"__INITIAL_STATE__", windowAssignment, "__INITIAL_STATE__"},
	{"__APOLLO_STATE__", windowAssignment, "__APOLLO_STATE__"},
	{"__PRELOADED_STATE__", windowAssignment, "__PRELOADED_STATE__"},
	{"__remixContext", windowAssignment, "__remixContext"},
	{"__NUXT__", windowAssignment, "__NUXT__"},
	{"__MODERN_ROUTER_DATA__", windowAssignment, "__MODERN_ROUTER_DATA__"},
}

// PatternInfo describes a known pattern for listing.
type PatternInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ListPatterns returns deduplicated metadata for all known embedded data patterns.
func ListPatterns() []PatternInfo {
	seen := make(map[string]bool)
	var result []PatternInfo
	for _, p := range knownPatterns {
		if seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		result = append(result, PatternInfo{
			Name:        p.Name,
			Description: patternDescription(p),
		})
	}
	return result
}

func patternDescription(p embeddedPattern) string {
	switch p.Name {
	case "__NEXT_DATA__":
		return "Next.js SSR page props"
	case "__UNIVERSAL_DATA_FOR_REHYDRATION__":
		return "TikTok hydration data"
	case "__FRONTITY_CONNECT_STATE__":
		return "WordPress Frontity state"
	case "SIGI_STATE":
		return "TikTok legacy state"
	case "__NUXT_DATA__":
		return "Nuxt 3 payload"
	case "__MODERN_ROUTER_DATA__":
		return "TikTok modern router data"
	case "ytInitialData":
		return "YouTube page data (search results, video metadata, comments)"
	case "ytInitialPlayerResponse":
		return "YouTube player config (streams, captions, chapters)"
	case "__INITIAL_STATE__":
		return "Redux/generic initial state"
	case "__APOLLO_STATE__":
		return "Apollo GraphQL client cache"
	case "__PRELOADED_STATE__":
		return "Redux preloaded state"
	case "__remixContext":
		return "Remix framework route data"
	case "__NUXT__":
		return "Nuxt 2 SSR state"
	default:
		return ""
	}
}

// ExtractEmbeddedScripts scans all <script> tags in the HTML for known
// embedded data patterns (SSR state, hydration data, etc.).
// Returns a map of pattern name -> parsed JSON data.
func ExtractEmbeddedScripts(rawHTML string) map[string]any {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return nil
	}
	return extractEmbeddedFromDoc(doc)
}

// ExtractSinglePattern extracts one named pattern from the HTML.
// Stops walking the DOM as soon as the pattern is found.
func ExtractSinglePattern(rawHTML string, name string) any {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return nil
	}

	result := make(map[string]any)
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.DataAtom == atom.Script {
			processScriptNode(n, result)
			if _, found := result[name]; found {
				return true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}

	walk(doc)
	return result[name]
}

func extractEmbeddedFromDoc(doc *html.Node) map[string]any {
	result := make(map[string]any)

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Script {
			processScriptNode(n, result)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(doc)

	if len(result) == 0 {
		return nil
	}
	return result
}

func processScriptNode(n *html.Node, result map[string]any) {
	id := attr(n, "id")
	text := scriptText(n)

	if id != "" {
		for _, p := range knownPatterns {
			if p.Type != scriptIDJSON || p.Key != id {
				continue
			}
			if _, found := result[p.Name]; found {
				return
			}
			if data := parseJSONText(text); data != nil {
				result[p.Name] = data
			}
			return
		}
	}

	// Assignment patterns need enough text to contain "var x = {...}"
	if len(text) < 20 {
		return
	}

	for _, p := range knownPatterns {
		if _, found := result[p.Name]; found {
			continue
		}
		var data any
		switch p.Type {
		case varAssignment:
			data = extractVarAssign(text, p.Key)
		case windowAssignment:
			data = extractWindowAssign(text, p.Key)
		}
		if data != nil {
			result[p.Name] = data
		}
	}
}

func parseJSONText(text string) any {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil
	}
	if isEmpty(parsed) {
		return nil
	}
	return parsed
}

func extractVarAssign(text, varName string) any {
	prefix := "var " + varName
	for {
		idx := strings.Index(text, prefix)
		if idx < 0 {
			return nil
		}
		afterIdx := idx + len(prefix)
		// Verify we matched a complete variable name, not a prefix like
		// "var ytInitialDataVersion" when searching for "var ytInitialData".
		if afterIdx >= len(text) || text[afterIdx] == ' ' || text[afterIdx] == '\t' || text[afterIdx] == '=' || text[afterIdx] == '\n' || text[afterIdx] == ';' {
			if data := findJSONAfterEquals(text[afterIdx:]); data != nil {
				return data
			}
		}
		text = text[afterIdx:]
	}
}

func extractWindowAssign(text, propName string) any {
	candidates := []string{
		"window." + propName,
		"window['" + propName + "']",
		`window["` + propName + `"]`,
	}
	for _, prefix := range candidates {
		remaining := text
		for {
			idx := strings.Index(remaining, prefix)
			if idx < 0 {
				break
			}
			afterIdx := idx + len(prefix)
			if data := findJSONAfterEquals(remaining[afterIdx:]); data != nil {
				return data
			}
			remaining = remaining[afterIdx:]
		}
	}
	return nil
}

func findJSONAfterEquals(s string) any {
	s = strings.TrimSpace(s)
	if len(s) == 0 || s[0] != '=' {
		return nil
	}
	s = strings.TrimSpace(s[1:])
	if data := extractJSONObject(s); data != nil {
		return data
	}
	return extractJSONParseCall(s)
}

// extractJSONParseCall handles state assigned via `JSON.parse('...')` or
// `JSON.parse("...")`. The wrapping is common on sites that server-render
// pre-escaped JSON strings to avoid HTML-escaping pitfalls (Genius, TikTok,
// others). Returns the parsed JSON value or nil if not this shape.
func extractJSONParseCall(s string) any {
	const prefix = "JSON.parse("
	if !strings.HasPrefix(s, prefix) {
		return nil
	}
	s = strings.TrimSpace(s[len(prefix):])
	if len(s) == 0 {
		return nil
	}
	quote := s[0]
	if quote != '\'' && quote != '"' {
		return nil
	}

	end := -1
	for i := 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == quote {
			end = i
			break
		}
	}
	if end < 0 {
		return nil
	}
	raw := s[1:end]

	unescaped := unescapeJSString(raw)
	var parsed any
	if err := json.Unmarshal([]byte(unescaped), &parsed); err != nil {
		return nil
	}
	if isEmpty(parsed) {
		return nil
	}
	return parsed
}

// unescapeJSString decodes a JavaScript string literal body into its
// runtime value. JSON's escape set is a strict subset — JS allows `\'`,
// `\$`, `\a` and other "any char" escapes that JSON rejects. We interpret
// the JS semantics so the result can be fed into json.Unmarshal.
func unescapeJSString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			i++
			continue
		}
		next := s[i+1]
		switch next {
		case 'n':
			b.WriteByte('\n')
			i += 2
		case 't':
			b.WriteByte('\t')
			i += 2
		case 'r':
			b.WriteByte('\r')
			i += 2
		case 'b':
			b.WriteByte('\b')
			i += 2
		case 'f':
			b.WriteByte('\f')
			i += 2
		case 'v':
			b.WriteByte('\v')
			i += 2
		case '0':
			b.WriteByte(0)
			i += 2
		case 'x':
			if i+3 < len(s) {
				if v, ok := parseHex(s[i+2 : i+4]); ok {
					b.WriteByte(byte(v))
					i += 4
					continue
				}
			}
			b.WriteByte(next)
			i += 2
		case 'u':
			if i+5 < len(s) {
				if v, ok := parseHex(s[i+2 : i+6]); ok {
					b.WriteRune(rune(v))
					i += 6
					continue
				}
			}
			b.WriteByte(next)
			i += 2
		default:
			// JS: any unrecognised escape drops the backslash (\$ → $,
			// \' → ', \a → a). This is the semantics we're restoring.
			b.WriteByte(next)
			i += 2
		}
	}
	return b.String()
}

func parseHex(s string) (int, bool) {
	v := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= int(c - '0')
		case c >= 'a' && c <= 'f':
			v |= int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= int(c-'A') + 10
		default:
			return 0, false
		}
	}
	return v, true
}

// extractJSONObject extracts a complete JSON value from the start of s
// using brace-depth counting with string-escape awareness.
func extractJSONObject(s string) any {
	if len(s) == 0 {
		return nil
	}
	if s[0] != '{' && s[0] != '[' {
		return nil
	}

	depth := 0
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		b := s[i]

		if escaped {
			escaped = false
			continue
		}

		if inString {
			if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}

		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth < 0 {
				return nil
			}
			if depth == 0 {
				candidate := s[:i+1]
				var parsed any
				if err := json.Unmarshal([]byte(candidate), &parsed); err != nil {
					return nil
				}
				if isEmpty(parsed) {
					return nil
				}
				return parsed
			}
		}
	}

	return nil
}

func scriptText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}

func isEmpty(v any) bool {
	switch val := v.(type) {
	case nil:
		return true
	case map[string]any:
		return len(val) == 0
	case []any:
		return len(val) == 0
	}
	return false
}
