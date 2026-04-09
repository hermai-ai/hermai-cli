package htmlext

import "testing"

func TestExtract_Forms(t *testing.T) {
	html := `<html><body>
		<form method="GET" action="/search" id="search-form">
			<input type="search" name="q" required>
			<input type="hidden" name="lang" value="en">
			<select name="sort">
				<option value="relevance" selected>Relevance</option>
				<option value="price">Price</option>
			</select>
		</form>
	</body></html>`

	result := Extract(html, "https://example.com/products")
	if len(result.Forms) != 1 {
		t.Fatalf("expected 1 form, got %d", len(result.Forms))
	}

	form := result.Forms[0]
	if form.Method != "GET" {
		t.Fatalf("expected GET form, got %s", form.Method)
	}
	if form.Action != "https://example.com/search" {
		t.Fatalf("expected resolved form action, got %s", form.Action)
	}
	if len(form.Fields) != 3 {
		t.Fatalf("expected 3 fields, got %d", len(form.Fields))
	}
	if form.Fields[0].Name != "q" || !form.Fields[0].Required {
		t.Fatalf("unexpected first field: %+v", form.Fields[0])
	}
	if form.Fields[2].Name != "sort" || len(form.Fields[2].Options) != 2 {
		t.Fatalf("unexpected select field: %+v", form.Fields[2])
	}
}
