package htmlext

import (
	"strings"
	"testing"
)

func TestExtract_FullPage(t *testing.T) {
	html := `<!DOCTYPE html>
<html lang="en">
<head>
	<title>Test Page</title>
	<meta name="description" content="A test page for extraction">
	<meta name="keywords" content="test,extraction">
	<meta property="og:title" content="OG Test Page">
	<meta property="og:image" content="https://example.com/og.png">
	<link rel="canonical" href="https://example.com/test">
	<script type="application/ld+json">{"@type":"Article","name":"Test"}</script>
</head>
<body>
	<h1>Main Heading</h1>
	<p>Some paragraph text.</p>
	<h2>Sub Heading</h2>
	<a href="/about">About Us</a>
	<a href="https://example.com/contact">Contact</a>
	<img src="/images/logo.png" alt="Logo">
	<p>More text here.</p>
</body>
</html>`

	result := Extract(html, "https://example.com/test")

	if result.Title != "Test Page" {
		t.Errorf("title: got %q, want %q", result.Title, "Test Page")
	}
	if result.Description != "A test page for extraction" {
		t.Errorf("description: got %q", result.Description)
	}
	if result.Language != "en" {
		t.Errorf("language: got %q", result.Language)
	}
	if result.Canonical != "https://example.com/test" {
		t.Errorf("canonical: got %q", result.Canonical)
	}
	if result.OpenGraph["title"] != "OG Test Page" {
		t.Errorf("og:title: got %q", result.OpenGraph["title"])
	}
	if result.OpenGraph["image"] != "https://example.com/og.png" {
		t.Errorf("og:image: got %q", result.OpenGraph["image"])
	}
	if result.Meta["keywords"] != "test,extraction" {
		t.Errorf("meta keywords: got %q", result.Meta["keywords"])
	}
	if len(result.JSONLD) != 1 {
		t.Fatalf("json_ld: got %d items, want 1", len(result.JSONLD))
	}
	if len(result.Headings) != 2 {
		t.Fatalf("headings: got %d, want 2", len(result.Headings))
	}
	if result.Headings[0].Level != 1 || result.Headings[0].Text != "Main Heading" {
		t.Errorf("h1: got %+v", result.Headings[0])
	}
	if result.Headings[1].Level != 2 || result.Headings[1].Text != "Sub Heading" {
		t.Errorf("h2: got %+v", result.Headings[1])
	}
	if len(result.Links) != 2 {
		t.Fatalf("links: got %d, want 2", len(result.Links))
	}
	if result.Links[0].Href != "https://example.com/about" {
		t.Errorf("link[0] href: got %q", result.Links[0].Href)
	}
	if result.Links[0].Text != "About Us" {
		t.Errorf("link[0] text: got %q", result.Links[0].Text)
	}
	if len(result.Images) != 1 {
		t.Fatalf("images: got %d, want 1", len(result.Images))
	}
	if result.Images[0].Src != "https://example.com/images/logo.png" {
		t.Errorf("image src: got %q", result.Images[0].Src)
	}
	if result.Images[0].Alt != "Logo" {
		t.Errorf("image alt: got %q", result.Images[0].Alt)
	}
	if !strings.Contains(result.BodyText, "Some paragraph text.") {
		t.Errorf("body_text missing paragraph, got: %q", result.BodyText)
	}
	if !strings.Contains(result.BodyText, "More text here.") {
		t.Errorf("body_text missing second paragraph, got: %q", result.BodyText)
	}
}

func TestExtract_EmptyHTML(t *testing.T) {
	result := Extract("", "https://example.com")
	if result.Title != "" {
		t.Errorf("expected empty title, got %q", result.Title)
	}
	if result.BodyText != "" {
		t.Errorf("expected empty body, got %q", result.BodyText)
	}
}

func TestExtract_MalformedHTML(t *testing.T) {
	// Go's html.Parse does error recovery; the key property is it never crashes.
	html := `<html><head><title>Broken</title></head><body><p>Still works`
	result := Extract(html, "https://example.com")
	if result.Title != "Broken" {
		t.Errorf("title: got %q, want %q", result.Title, "Broken")
	}
	if !strings.Contains(result.BodyText, "Still works") {
		t.Errorf("body_text: got %q", result.BodyText)
	}
}

func TestExtract_SeverelyBrokenHTML(t *testing.T) {
	// Should not panic or error on garbage input.
	result := Extract(`<<<<>>>><not real html at all`, "https://example.com")
	_ = result // just verify no panic
}

func TestExtract_BodyOnly(t *testing.T) {
	html := `<body><h1>Hello</h1><p>World</p></body>`
	result := Extract(html, "https://example.com")
	if len(result.Headings) != 1 || result.Headings[0].Text != "Hello" {
		t.Errorf("headings: got %+v", result.Headings)
	}
	if !strings.Contains(result.BodyText, "World") {
		t.Errorf("body_text: got %q", result.BodyText)
	}
}

func TestExtract_SkipsScriptAndStyle(t *testing.T) {
	html := `<body>
		<script>var x = "hidden";</script>
		<style>.hidden { display: none; }</style>
		<p>Visible text</p>
	</body>`
	result := Extract(html, "https://example.com")
	if strings.Contains(result.BodyText, "hidden") {
		t.Errorf("body_text should not contain script/style content: %q", result.BodyText)
	}
	if !strings.Contains(result.BodyText, "Visible text") {
		t.Errorf("body_text missing visible text: %q", result.BodyText)
	}
}

func TestExtract_LinkDeduplication(t *testing.T) {
	html := `<body>
		<a href="/page">First</a>
		<a href="/page">Second</a>
		<a href="/other">Other</a>
	</body>`
	result := Extract(html, "https://example.com")
	if len(result.Links) != 2 {
		t.Errorf("expected 2 deduplicated links, got %d", len(result.Links))
	}
}

func TestExtract_SkipsAnchorAndJavascriptLinks(t *testing.T) {
	html := `<body>
		<a href="#section">Anchor</a>
		<a href="javascript:void(0)">JS</a>
		<a href="/real">Real</a>
	</body>`
	result := Extract(html, "https://example.com")
	if len(result.Links) != 1 {
		t.Errorf("expected 1 link, got %d: %+v", len(result.Links), result.Links)
	}
}

func TestExtract_SkipsDataURIImages(t *testing.T) {
	html := `<body>
		<img src="data:image/png;base64,abc" alt="Pixel">
		<img src="/real.png" alt="Real">
	</body>`
	result := Extract(html, "https://example.com")
	if len(result.Images) != 1 {
		t.Errorf("expected 1 image, got %d", len(result.Images))
	}
	if result.Images[0].Src != "https://example.com/real.png" {
		t.Errorf("image src: got %q", result.Images[0].Src)
	}
}

func TestExtract_RelativeURLResolution(t *testing.T) {
	html := `<body>
		<a href="/about">About</a>
		<a href="contact">Contact</a>
		<img src="../img/logo.png" alt="Logo">
	</body>`
	result := Extract(html, "https://example.com/pages/test")

	if result.Links[0].Href != "https://example.com/about" {
		t.Errorf("link /about: got %q", result.Links[0].Href)
	}
	if result.Links[1].Href != "https://example.com/pages/contact" {
		t.Errorf("link contact: got %q", result.Links[1].Href)
	}
	if result.Images[0].Src != "https://example.com/img/logo.png" {
		t.Errorf("image ../img/logo.png: got %q", result.Images[0].Src)
	}
}

func TestExtract_BodyTextTruncation(t *testing.T) {
	word := "word "
	html := "<body><p>" + strings.Repeat(word, 2000) + "</p></body>"
	result := Extract(html, "https://example.com")
	if len(result.BodyText) > maxBodyText {
		t.Errorf("body_text length %d exceeds max %d", len(result.BodyText), maxBodyText)
	}
}

func TestExtract_HeadingLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("<body>")
	for i := range 60 {
		b.WriteString("<h3>Heading ")
		b.WriteString(strings.Repeat("x", i))
		b.WriteString("</h3>")
	}
	b.WriteString("</body>")

	result := Extract(b.String(), "https://example.com")
	if len(result.Headings) > maxHeadings {
		t.Errorf("headings %d exceeds max %d", len(result.Headings), maxHeadings)
	}
}

func TestExtract_LinkLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("<body>")
	for i := range 60 {
		b.WriteString(`<a href="/page-`)
		b.WriteString(strings.Repeat("x", i))
		b.WriteString(`">Link</a>`)
	}
	b.WriteString("</body>")

	result := Extract(b.String(), "https://example.com")
	if len(result.Links) > maxLinks {
		t.Errorf("links %d exceeds max %d", len(result.Links), maxLinks)
	}
}

func TestExtract_OpenGraphVariants(t *testing.T) {
	html := `<head>
		<meta property="og:title" content="Title">
		<meta property="og:description" content="Desc">
		<meta property="og:url" content="https://example.com">
		<meta property="og:type" content="website">
	</head>`
	result := Extract(html, "https://example.com")
	if len(result.OpenGraph) != 4 {
		t.Errorf("expected 4 OG properties, got %d: %v", len(result.OpenGraph), result.OpenGraph)
	}
}

func TestExtract_MultipleJSONLD(t *testing.T) {
	html := `<head>
		<script type="application/ld+json">{"@type":"Article"}</script>
		<script type="application/ld+json">{"@type":"BreadcrumbList"}</script>
		<script type="application/ld+json">NOT VALID JSON</script>
	</head>`
	result := Extract(html, "https://example.com")
	if len(result.JSONLD) != 2 {
		t.Errorf("expected 2 valid JSON-LD items (skip invalid), got %d", len(result.JSONLD))
	}
}

func TestExtract_WhitespaceCollapsing(t *testing.T) {
	html := `<body><p>  Multiple   spaces   and
	tabs  and
	newlines  </p></body>`
	result := Extract(html, "https://example.com")
	if strings.Contains(result.BodyText, "  ") {
		t.Errorf("body_text has uncollapsed whitespace: %q", result.BodyText)
	}
}

func TestExtract_NoBaseURL(t *testing.T) {
	html := `<body><a href="/about">About</a><img src="/logo.png"></body>`
	result := Extract(html, "")
	if len(result.Links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(result.Links))
	}
	// Without base URL, relative paths stay relative
	if result.Links[0].Href != "/about" {
		t.Errorf("link href: got %q", result.Links[0].Href)
	}
}

func TestExtract_MetaExcludesViewport(t *testing.T) {
	html := `<head>
		<meta name="viewport" content="width=device-width">
		<meta name="author" content="Test Author">
	</head>`
	result := Extract(html, "https://example.com")
	if _, ok := result.Meta["viewport"]; ok {
		t.Error("meta should not include viewport")
	}
	if result.Meta["author"] != "Test Author" {
		t.Errorf("meta author: got %q", result.Meta["author"])
	}
}
