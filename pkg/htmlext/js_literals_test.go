package htmlext

import (
	"testing"
)

func TestExtractHandlesUndefined(t *testing.T) {
	html := `<html><body><script>window.__INITIAL_STATE__={"user":{"id":1,"last_login":undefined},"count":42}</script></body></html>`
	page := Extract(html, "https://example.com/")

	state, ok := page.EmbeddedScripts["__INITIAL_STATE__"].(map[string]any)
	if !ok {
		t.Fatalf("expected __INITIAL_STATE__ as map, got %T (scripts=%v)", page.EmbeddedScripts["__INITIAL_STATE__"], page.EmbeddedScripts)
	}
	user, _ := state["user"].(map[string]any)
	if user["id"].(float64) != 1 {
		t.Errorf("user.id = %v", user["id"])
	}
	if _, hasKey := user["last_login"]; !hasKey {
		t.Errorf("expected last_login key (null) present, got %v", user)
	}
	if state["count"].(float64) != 42 {
		t.Errorf("count = %v", state["count"])
	}
}

func TestExtractHandlesNaNAndInfinity(t *testing.T) {
	html := `<html><body><script>window.__APOLLO_STATE__={"a":NaN,"b":Infinity,"c":-Infinity,"d":1}</script></body></html>`
	page := Extract(html, "https://example.com/")

	state, ok := page.EmbeddedScripts["__APOLLO_STATE__"].(map[string]any)
	if !ok {
		t.Fatalf("expected __APOLLO_STATE__ as map, got %T", page.EmbeddedScripts["__APOLLO_STATE__"])
	}
	for _, k := range []string{"a", "b", "c"} {
		if _, hasKey := state[k]; !hasKey {
			t.Errorf("expected key %q (null) to be present", k)
		}
	}
	if state["d"].(float64) != 1 {
		t.Errorf("d = %v", state["d"])
	}
}

func TestSanitizeJSLiterals_LeavesStrings(t *testing.T) {
	// A string value that contains the word should not be rewritten —
	// the leading delimiter guard (quote) plus word boundary handles
	// the common case. This test locks that in.
	in := `{"note":"the value was undefined"}`
	got := sanitizeJSLiterals(in)
	if got != in {
		t.Errorf("sanitize mutated a string: got %q", got)
	}
}

func TestSanitizeJSLiterals_Preservation(t *testing.T) {
	// When there's nothing to rewrite, input is returned verbatim so
	// the fast path flag in extractJSONObject (input == output ⇒ give
	// up) doesn't misfire.
	in := `{"ok":true}`
	got := sanitizeJSLiterals(in)
	if got != in {
		t.Errorf("sanitize should be a no-op for clean JSON, got %q", got)
	}
}
