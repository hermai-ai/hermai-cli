package htmlext

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	maxBodyText = 5000
	maxHeadings = 50
	maxLinks    = 50
	maxImages   = 30
	maxForms    = 10
)

// PageContent is the structured data extracted from an HTML page.
type PageContent struct {
	Title           string            `json:"title"`
	Description     string            `json:"description,omitempty"`
	Language        string            `json:"language,omitempty"`
	Canonical       string            `json:"canonical,omitempty"`
	OpenGraph       map[string]string `json:"open_graph,omitempty"`
	Meta            map[string]string `json:"meta,omitempty"`
	JSONLD          []any             `json:"json_ld,omitempty"`
	NextData        any               `json:"next_data,omitempty"`        // __NEXT_DATA__ from Next.js SSR
	EmbeddedScripts map[string]any    `json:"embedded_scripts,omitempty"` // All detected embedded data patterns
	Headings        []Heading         `json:"headings,omitempty"`
	Links           []Link            `json:"links,omitempty"`
	Images          []Image           `json:"images,omitempty"`
	Forms           []Form            `json:"forms,omitempty"`
	BodyText        string            `json:"body_text"`
	HasArticle      bool              `json:"has_article,omitempty"`
}

// Heading represents an HTML heading element.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// Link represents an HTML anchor element.
type Link struct {
	Text string `json:"text"`
	Href string `json:"href"`
}

// Image represents an HTML image element.
type Image struct {
	Src string `json:"src"`
	Alt string `json:"alt,omitempty"`
}

// Form represents an HTML form that may be executable without a browser.
type Form struct {
	Name   string      `json:"name,omitempty"`
	Method string      `json:"method"`
	Action string      `json:"action"`
	Fields []FormField `json:"fields,omitempty"`
}

// FormField represents one input within a form.
type FormField struct {
	Name     string   `json:"name"`
	Type     string   `json:"type,omitempty"`
	Value    string   `json:"value,omitempty"`
	Required bool     `json:"required,omitempty"`
	Options  []string `json:"options,omitempty"`
}

// Extract parses an HTML string and returns structured page content.
// It never returns an error — partial results are returned on malformed HTML.
func Extract(rawHTML string, baseURL string) PageContent {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return PageContent{}
	}

	base, _ := url.Parse(baseURL)

	// First pass: collect metadata from entire document (title, meta, OG, JSON-LD)
	e := &extractor{
		base:            base,
		seenURL:         make(map[string]bool),
		embeddedScripts: make(map[string]any),
	}
	e.walk(doc)

	// Find the main content node — only extract body text from there.
	// This skips nav, sidebar, footer, and other noise.
	mainNode := findMainContent(doc)
	var bodyText string
	if mainNode != nil {
		var b strings.Builder
		collectBodyText(mainNode, &b)
		bodyText = truncate(collapseWhitespace(b.String()), maxBodyText)
	} else {
		// Fallback: use all body text if no main content area found
		bodyText = truncate(collapseWhitespace(e.body.String()), maxBodyText)
	}

	// Exclude __NEXT_DATA__ from embedded scripts since it's already in the
	// dedicated NextData field with specialized pageProps/gdpClientCache handling.
	delete(e.embeddedScripts, "__NEXT_DATA__")
	var embedded map[string]any
	if len(e.embeddedScripts) > 0 {
		embedded = e.embeddedScripts
	}

	return PageContent{
		Title:           e.title,
		Description:     e.description,
		Language:        e.language,
		Canonical:       e.canonical,
		OpenGraph:       nonEmpty(e.og),
		Meta:            nonEmpty(e.meta),
		JSONLD:          e.jsonLD,
		NextData:        e.nextData,
		EmbeddedScripts: embedded,
		Headings:        e.headings,
		Links:           e.links,
		Images:          e.images,
		Forms:           e.forms,
		BodyText:        bodyText,
		HasArticle:      e.hasArticle,
	}
}

// findMainContent looks for the primary content container in the DOM.
// Tries semantic elements first, then common ID/role patterns.
// Returns nil if no clear main content area is found.
func findMainContent(doc *html.Node) *html.Node {
	// Priority order: most specific to least
	candidates := []func(*html.Node) *html.Node{
		func(n *html.Node) *html.Node { return findByAttr(n, "role", "main") },
		func(n *html.Node) *html.Node { return findByTag(n, atom.Main) },
		func(n *html.Node) *html.Node { return findByTag(n, atom.Article) },
		func(n *html.Node) *html.Node { return findByID(n, "content") },
		func(n *html.Node) *html.Node { return findByID(n, "main-content") },
		func(n *html.Node) *html.Node { return findByID(n, "main") },
	}

	for _, find := range candidates {
		if node := find(doc); node != nil {
			return node
		}
	}
	return nil
}

func findByTag(n *html.Node, tag atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findByTag(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func findByAttr(n *html.Node, key, val string) *html.Node {
	if n.Type == html.ElementNode && attr(n, key) == val {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findByAttr(c, key, val); found != nil {
			return found
		}
	}
	return nil
}

func findByID(n *html.Node, id string) *html.Node {
	return findByAttr(n, "id", id)
}

// collectBodyText extracts visible text from a subtree, skipping noise elements.
func collectBodyText(n *html.Node, b *strings.Builder) {
	if n.Type == html.ElementNode {
		// Skip non-content elements
		switch n.DataAtom {
		case atom.Script, atom.Style, atom.Noscript, atom.Nav, atom.Footer,
			atom.Header, atom.Dialog, atom.Template, atom.Svg:
			return
		}

		// Skip elements hidden via aria or common CSS patterns
		if isHiddenElement(n) {
			return
		}

		// Add line breaks before block elements for readability
		if isBlockElement(n) && b.Len() > 0 {
			b.WriteByte('\n')
		}
	}
	if n.Type == html.TextNode {
		text := strings.TrimSpace(n.Data)
		if text != "" {
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
				b.WriteByte(' ')
			}
			b.WriteString(text)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectBodyText(c, b)
	}
}

// isHiddenElement returns true for elements that are visually hidden
// or contain non-content UI (modals, dropdowns, screen-reader text).
func isHiddenElement(n *html.Node) bool {
	if attr(n, "aria-hidden") == "true" {
		return true
	}
	if hasAttr(n, "hidden") {
		return true
	}
	role := attr(n, "role")
	if role == "dialog" || role == "alert" || role == "navigation" {
		return true
	}
	cls := attr(n, "class")
	for _, skip := range []string{"sr-only", "visually-hidden", "hidden", "modal", "dropdown-menu", "tooltip"} {
		if strings.Contains(cls, skip) {
			return true
		}
	}
	return false
}

// isBlockElement returns true for HTML elements that start a new line.
func isBlockElement(n *html.Node) bool {
	switch n.DataAtom {
	case atom.P, atom.Div, atom.Section, atom.Article,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6,
		atom.Li, atom.Tr, atom.Br, atom.Hr, atom.Blockquote, atom.Pre:
		return true
	}
	return false
}

type extractor struct {
	base    *url.URL
	seenURL map[string]bool

	title           string
	description     string
	language        string
	canonical       string
	og              map[string]string
	meta            map[string]string
	jsonLD          []any
	nextData        any
	embeddedScripts map[string]any
	headings        []Heading
	links           []Link
	images          []Image
	forms           []Form
	body            strings.Builder
	hasArticle      bool

	inTitle  bool
	inScript bool
	inStyle  bool
	inHead   bool
	skipText bool // inside elements whose text we want to ignore
}

func (e *extractor) walk(n *html.Node) {
	switch n.Type {
	case html.ElementNode:
		e.enterElement(n)
	case html.TextNode:
		e.handleText(n)
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		e.walk(c)
	}

	if n.Type == html.ElementNode {
		e.leaveElement(n)
	}
}

func (e *extractor) enterElement(n *html.Node) {
	switch n.DataAtom {
	case atom.Html:
		if lang := attr(n, "lang"); lang != "" {
			e.language = lang
		}
	case atom.Head:
		e.inHead = true
	case atom.Title:
		if e.inHead {
			e.inTitle = true
		}
	case atom.Meta:
		e.handleMeta(n)
	case atom.Link:
		if e.inHead && attr(n, "rel") == "canonical" {
			e.canonical = e.resolve(attr(n, "href"))
		}
	case atom.Script:
		e.inScript = true
		if attr(n, "type") == "application/ld+json" {
			e.extractJSONLD(n)
		} else if attr(n, "id") == "__NEXT_DATA__" {
			e.extractNextData(n)
		} else {
			processScriptNode(n, e.embeddedScripts)
		}
	case atom.Style:
		e.inStyle = true
	case atom.Noscript:
		e.skipText = true
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		if len(e.headings) < maxHeadings {
			level := int(n.Data[1] - '0')
			text := textContent(n)
			if text != "" {
				e.headings = append(e.headings, Heading{Level: level, Text: text})
			}
		}
	case atom.Article:
		e.hasArticle = true
	case atom.A:
		e.handleLink(n)
	case atom.Img:
		e.handleImage(n)
	case atom.Form:
		e.handleForm(n)
	}
}

func (e *extractor) leaveElement(n *html.Node) {
	switch n.DataAtom {
	case atom.Head:
		e.inHead = false
	case atom.Title:
		e.inTitle = false
	case atom.Script:
		e.inScript = false
	case atom.Style:
		e.inStyle = false
	case atom.Noscript:
		e.skipText = false
	}
}

func (e *extractor) handleText(n *html.Node) {
	if e.inTitle {
		e.title = strings.TrimSpace(n.Data)
		return
	}
	if e.inScript || e.inStyle || e.skipText || e.inHead {
		return
	}
	text := strings.TrimSpace(n.Data)
	if text != "" {
		if e.body.Len() > 0 {
			e.body.WriteByte(' ')
		}
		e.body.WriteString(text)
	}
}

func (e *extractor) handleMeta(n *html.Node) {
	name := attr(n, "name")
	property := attr(n, "property")
	content := attr(n, "content")

	if content == "" {
		return
	}

	if name == "description" {
		e.description = content
	}

	if strings.HasPrefix(property, "og:") {
		if e.og == nil {
			e.og = make(map[string]string)
		}
		e.og[strings.TrimPrefix(property, "og:")] = content
	}

	if name != "" && name != "description" && name != "viewport" {
		if e.meta == nil {
			e.meta = make(map[string]string)
		}
		e.meta[name] = content
	}
}

func (e *extractor) handleLink(n *html.Node) {
	if len(e.links) >= maxLinks {
		return
	}
	href := attr(n, "href")
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") {
		return
	}
	resolved := e.resolve(href)
	if resolved == "" || e.seenURL[resolved] {
		return
	}
	e.seenURL[resolved] = true
	e.links = append(e.links, Link{
		Text: truncate(textContent(n), 200),
		Href: resolved,
	})
}

func (e *extractor) handleImage(n *html.Node) {
	if len(e.images) >= maxImages {
		return
	}
	src := attr(n, "src")
	if src == "" || strings.HasPrefix(src, "data:") {
		return
	}
	e.images = append(e.images, Image{
		Src: e.resolve(src),
		Alt: attr(n, "alt"),
	})
}

func (e *extractor) handleForm(n *html.Node) {
	if len(e.forms) >= maxForms {
		return
	}

	method := strings.ToUpper(strings.TrimSpace(attr(n, "method")))
	if method == "" {
		method = "GET"
	}

	action := strings.TrimSpace(attr(n, "action"))
	if action == "" && e.base != nil {
		action = e.base.String()
	}
	action = e.resolve(action)
	if action == "" {
		return
	}

	form := Form{
		Name:   firstNonEmpty(attr(n, "name"), attr(n, "id")),
		Method: method,
		Action: action,
		Fields: parseFormFields(n),
	}
	if len(form.Fields) == 0 {
		return
	}

	e.forms = append(e.forms, form)
}

func parseFormFields(root *html.Node) []FormField {
	fields := make([]FormField, 0, 8)
	seen := make(map[string]bool)

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type != html.ElementNode {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			return
		}

		switch n.DataAtom {
		case atom.Input:
			name := strings.TrimSpace(attr(n, "name"))
			if name == "" || seen["input:"+name] {
				break
			}
			fieldType := strings.ToLower(strings.TrimSpace(attr(n, "type")))
			if fieldType == "" {
				fieldType = "text"
			}
			// Unchecked toggles are not useful defaults for browserless actions.
			if (fieldType == "checkbox" || fieldType == "radio") && !hasAttr(n, "checked") {
				break
			}
			fields = append(fields, FormField{
				Name:     name,
				Type:     fieldType,
				Value:    attr(n, "value"),
				Required: hasAttr(n, "required"),
			})
			seen["input:"+name] = true
		case atom.Textarea:
			name := strings.TrimSpace(attr(n, "name"))
			if name == "" || seen["textarea:"+name] {
				break
			}
			fields = append(fields, FormField{
				Name:     name,
				Type:     "textarea",
				Value:    textContent(n),
				Required: hasAttr(n, "required"),
			})
			seen["textarea:"+name] = true
		case atom.Select:
			name := strings.TrimSpace(attr(n, "name"))
			if name == "" || seen["select:"+name] {
				break
			}
			options, selected := parseSelectOptions(n)
			fields = append(fields, FormField{
				Name:     name,
				Type:     "select",
				Value:    selected,
				Required: hasAttr(n, "required"),
				Options:  options,
			})
			seen["select:"+name] = true
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(root)
	return fields
}

func parseSelectOptions(n *html.Node) ([]string, string) {
	options := make([]string, 0, 8)
	var selected string

	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		if cur.Type == html.ElementNode && cur.DataAtom == atom.Option {
			value := attr(cur, "value")
			if value == "" {
				value = strings.TrimSpace(textContent(cur))
			}
			if value != "" {
				options = append(options, value)
				if selected == "" || hasAttr(cur, "selected") {
					selected = value
				}
			}
		}
		for c := cur.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(n)
	return options, selected
}

func (e *extractor) extractNextData(n *html.Node) {
	text := strings.TrimSpace(scriptText(n))
	if text == "" {
		return
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		e.nextData = parsed
	}
}

func (e *extractor) extractJSONLD(n *html.Node) {
	text := strings.TrimSpace(scriptText(n))
	if text == "" {
		return
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		e.jsonLD = append(e.jsonLD, parsed)
	}
}

func (e *extractor) resolve(rawref string) string {
	if e.base == nil || rawref == "" {
		return rawref
	}
	ref, err := url.Parse(rawref)
	if err != nil {
		return rawref
	}
	return e.base.ResolveReference(ref).String()
}

// textContent recursively collects visible text from an element.
func textContent(n *html.Node) string {
	var b strings.Builder
	collectText(n, &b)
	return strings.TrimSpace(b.String())
}

func collectText(n *html.Node, b *strings.Builder) {
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectText(c, b)
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

func nonEmpty(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
