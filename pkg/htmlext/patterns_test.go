package htmlext

import (
	"testing"
)

func TestExtractEmbeddedScripts_ScriptID(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">
{"__DEFAULT_SCOPE__":{"webapp.app-context":{"language":"en"}}}
</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	data, ok := result["__UNIVERSAL_DATA_FOR_REHYDRATION__"]
	if !ok {
		t.Fatal("expected __UNIVERSAL_DATA_FOR_REHYDRATION__ key")
	}
	m, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", data)
	}
	if _, ok := m["__DEFAULT_SCOPE__"]; !ok {
		t.Error("expected __DEFAULT_SCOPE__ in data")
	}
}

func TestExtractEmbeddedScripts_NextData(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<script id="__NEXT_DATA__" type="application/json">
{"props":{"pageProps":{"title":"Test","items":[1,2,3]}}}
</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if _, ok := result["__NEXT_DATA__"]; !ok {
		t.Error("expected __NEXT_DATA__ key")
	}
}

func TestExtractEmbeddedScripts_VarAssignment(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<script>
var ytInitialData = {"responseContext":{"serviceTrackingParams":[{"service":"CSI"}]},"contents":{"twoColumnWatchNextResults":{}}};
</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	data, ok := result["ytInitialData"]
	if !ok {
		t.Fatal("expected ytInitialData key")
	}
	m, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", data)
	}
	if _, ok := m["responseContext"]; !ok {
		t.Error("expected responseContext in data")
	}
	if _, ok := m["contents"]; !ok {
		t.Error("expected contents in data")
	}
}

func TestExtractEmbeddedScripts_WindowDotAssignment(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<script>
window.__APOLLO_STATE__ = {"ROOT_QUERY":{"user:123":{"name":"test","age":30}}};
</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	data, ok := result["__APOLLO_STATE__"]
	if !ok {
		t.Fatal("expected __APOLLO_STATE__ key")
	}
	m, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", data)
	}
	if _, ok := m["ROOT_QUERY"]; !ok {
		t.Error("expected ROOT_QUERY in data")
	}
}

func TestExtractEmbeddedScripts_WindowBracketAssignment(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<script>
window['SIGI_STATE'] = {"ItemModule":{"123":{"desc":"test video"}}};
</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	data, ok := result["SIGI_STATE"]
	if !ok {
		t.Fatal("expected SIGI_STATE key")
	}
	m, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", data)
	}
	if _, ok := m["ItemModule"]; !ok {
		t.Error("expected ItemModule in data")
	}
}

func TestExtractEmbeddedScripts_MultiplePatterns(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<script>
var ytInitialData = {"page":"watch","contents":{"items":[1,2]}};
var ytInitialPlayerResponse = {"playabilityStatus":{"status":"OK"},"streamingData":{"formats":[]}};
</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if _, ok := result["ytInitialData"]; !ok {
		t.Error("expected ytInitialData")
	}
	if _, ok := result["ytInitialPlayerResponse"]; !ok {
		t.Error("expected ytInitialPlayerResponse")
	}
}

func TestExtractEmbeddedScripts_MalformedJSON(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">
{this is not valid json}
</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result != nil {
		t.Errorf("expected nil for malformed JSON, got %v", result)
	}
}

func TestExtractEmbeddedScripts_EmptyObject(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<script>
var ytInitialData = {};
</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result != nil {
		t.Errorf("expected nil for empty object, got %v", result)
	}
}

func TestExtractEmbeddedScripts_NoPatterns(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<script>console.log("no patterns here");</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result != nil {
		t.Errorf("expected nil for no patterns, got %v", result)
	}
}

func TestExtractEmbeddedScripts_ScriptIDTakesPrecedence(t *testing.T) {
	// SIGI_STATE appears as both script-id and window-assignment.
	// Script-id should take precedence since it's checked first.
	html := `<!DOCTYPE html><html><body>
<script id="SIGI_STATE" type="application/json">
{"ItemModule":{"from_script_id":true}}
</script>
<script>
window['SIGI_STATE'] = {"ItemModule":{"from_window":true}};
</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	data, ok := result["SIGI_STATE"]
	if !ok {
		t.Fatal("expected SIGI_STATE key")
	}
	m := data.(map[string]any)
	items := m["ItemModule"].(map[string]any)
	if _, ok := items["from_script_id"]; !ok {
		t.Error("expected script-id version to win over window assignment")
	}
}

func TestExtractEmbeddedScripts_NestedJSON(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<script>
var ytInitialData = {"a":{"b":{"c":[1,{"d":"e\"f"}]}},"x":"y"};
</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	data, ok := result["ytInitialData"]
	if !ok {
		t.Fatal("expected ytInitialData key")
	}
	m := data.(map[string]any)
	if _, ok := m["a"]; !ok {
		t.Error("expected nested key 'a'")
	}
	if m["x"] != "y" {
		t.Error("expected top-level key 'x' = 'y'")
	}
}

func TestExtractEmbeddedScripts_WhitespaceVariations(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<script>
window.__INITIAL_STATE__={"user":{"id":1}};
</script>
</body></html>`

	result := ExtractEmbeddedScripts(html)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if _, ok := result["__INITIAL_STATE__"]; !ok {
		t.Error("expected __INITIAL_STATE__ without spaces around =")
	}
}

func TestExtractJSONObject_Strings(t *testing.T) {
	input := `{"key":"value with {braces} and \"quotes\"","arr":[1,2]}`
	got := extractJSONObject(input)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	if m["key"] != `value with {braces} and "quotes"` {
		t.Errorf("key: got %q", m["key"])
	}
}

func TestListPatterns(t *testing.T) {
	patterns := ListPatterns()
	if len(patterns) == 0 {
		t.Fatal("expected at least one pattern")
	}

	names := make(map[string]bool)
	for _, p := range patterns {
		if names[p.Name] {
			t.Errorf("duplicate pattern name: %s", p.Name)
		}
		names[p.Name] = true
		if p.Description == "" {
			t.Errorf("pattern %s has empty description", p.Name)
		}
	}

	expected := []string{"ytInitialData", "__NEXT_DATA__", "__APOLLO_STATE__", "SIGI_STATE"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected pattern %s in list", name)
		}
	}
}

// --- generic <script type="application/json" id="X"> fallback --------------

func TestExtractEmbeddedScripts_GenericIDFallback(t *testing.T) {
	// An SSR-rendered micro-frontend (Estée Lauder's ELC platform uses
	// id="page_data", Shopify Hydrogen sometimes uses custom ids) ships
	// its hydration blob inside a <script> tag with a non-standard id.
	// The extractor should surface any such tag keyed by its raw id.
	html := `<html><body>
		<script type="application/json" id="page_data">` +
		`{"consolidated-categories":{"12345":{"name":"Lipstick"}},"consolidated-products":{"PROD1":{"price":25.00,"shades":20}},"page_config":{"storefront":"mc-us-en-ecommv1"}}` +
		`</script>
	</body></html>`

	result := ExtractEmbeddedScripts(html)
	got, ok := result["page_data"]
	if !ok {
		t.Fatalf("expected page_data key in result; got %v", result)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("page_data should be an object, got %T", got)
	}
	if _, ok := m["consolidated-products"]; !ok {
		t.Error("page_data is missing expected keys")
	}
}

func TestExtractEmbeddedScripts_NamedPatternBeatsGeneric(t *testing.T) {
	// __NEXT_DATA__ must still surface under its named key, not under a
	// raw "script:__NEXT_DATA__" bucket, even though it matches both
	// the named and generic rules.
	html := `<html><body>
		<script type="application/json" id="__NEXT_DATA__">{"props":{"pageProps":{"title":"Hello"}}}</script>
	</body></html>`
	result := ExtractEmbeddedScripts(html)
	if _, ok := result["__NEXT_DATA__"]; !ok {
		t.Error("__NEXT_DATA__ missing from result")
	}
	// No duplicate entry under some generic key.
	if len(result) != 1 {
		t.Errorf("expected 1 key, got %d: %v", len(result), keys(result))
	}
}

func TestExtractEmbeddedScripts_GenericFallbackIgnoresShortOrIDless(t *testing.T) {
	// Very short JSON bodies, JSON without an id, and non-JSON types
	// should all be skipped so the extractor doesn't surface every
	// micro config stub.
	html := `<html><body>
		<script type="application/json" id="tiny">{"k":1}</script>                  <!-- too short -->
		<script type="application/json">{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"long_enough":"yes, definitely more than one hundred chars of padding here to cross the threshold set by the extractor"}</script>   <!-- no id -->
		<script type="text/javascript" id="plainJS">var x = {"a": 1};</script>   <!-- wrong type -->
	</body></html>`
	result := ExtractEmbeddedScripts(html)
	if len(result) != 0 {
		t.Errorf("expected nothing, got %v", keys(result))
	}
}

func TestExtractEmbeddedScripts_MultipleGenericIDsCoexist(t *testing.T) {
	html := `<html><body>
		<script type="application/json" id="page_data">` +
		`{"widely":"used","pattern":"for micro frontends","more":"padding here","yet":"more","one":1,"two":2,"three":3,"four":4,"five":5,"six":6}</script>
		<script type="application/json" id="pdp_config">` +
		`{"currency":"USD","locale":"en-US","shipping_threshold":45,"promo_banners":["free_ship","sale_14_off"],"more":"stuff here long enough for threshold"}</script>
	</body></html>`
	result := ExtractEmbeddedScripts(html)
	for _, want := range []string{"page_data", "pdp_config"} {
		if _, ok := result[want]; !ok {
			t.Errorf("missing %q in result: got %v", want, keys(result))
		}
	}
}

// keys is a tiny helper for error messages so failing tests show what
// WAS found when the expected key is missing.
func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
