package analyzer

import (
	"testing"
)

func TestTruncateJSONValues_ShortStringsPreserved(t *testing.T) {
	input := map[string]any{
		"name": "short",
	}
	result := TruncateJSONValues(input, 10)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map")
	}
	if m["name"] != "short" {
		t.Errorf("expected 'short', got %v", m["name"])
	}
}

func TestTruncateJSONValues_LongStringsTruncated(t *testing.T) {
	input := map[string]any{
		"description": "this is a very long string that should be truncated",
	}
	result := TruncateJSONValues(input, 10)
	m := result.(map[string]any)
	val := m["description"].(string)
	if val != "this is a ..." {
		t.Errorf("expected 'this is a ...', got '%s'", val)
	}
}

func TestTruncateJSONValues_ArraysCappedToThree(t *testing.T) {
	input := []any{"a", "b", "c", "d", "e"}
	result := TruncateJSONValues(input, 100)
	arr := result.([]any)
	if len(arr) != 3 {
		t.Errorf("expected 3 items, got %d", len(arr))
	}
	if arr[0] != "a" || arr[1] != "b" || arr[2] != "c" {
		t.Errorf("unexpected items: %v", arr)
	}
}

func TestTruncateJSONValues_NestedStructure(t *testing.T) {
	input := map[string]any{
		"data": map[string]any{
			"items": []any{"one", "two", "three", "four", "five"},
			"info":  "a very long nested string value here",
		},
	}
	result := TruncateJSONValues(input, 10)
	m := result.(map[string]any)
	data := m["data"].(map[string]any)

	items := data["items"].([]any)
	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}

	info := data["info"].(string)
	if info != "a very lon..." {
		t.Errorf("expected 'a very lon...', got '%s'", info)
	}
}

func TestTruncateJSONValues_NumbersPreserved(t *testing.T) {
	result := TruncateJSONValues(float64(42), 5)
	if result != float64(42) {
		t.Errorf("expected 42, got %v", result)
	}
}

func TestTruncateJSONValues_BoolsPreserved(t *testing.T) {
	result := TruncateJSONValues(true, 5)
	if result != true {
		t.Errorf("expected true, got %v", result)
	}
}

func TestTruncateJSONValues_NilPreserved(t *testing.T) {
	result := TruncateJSONValues(nil, 5)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestTruncateJSONValues_EmptyMapPreserved(t *testing.T) {
	input := map[string]any{}
	result := TruncateJSONValues(input, 10)
	m := result.(map[string]any)
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestTruncateJSONValues_EmptyArrayPreserved(t *testing.T) {
	input := []any{}
	result := TruncateJSONValues(input, 10)
	arr := result.([]any)
	if len(arr) != 0 {
		t.Errorf("expected empty array, got %v", arr)
	}
}

func TestTruncateJSONValues_DoesNotMutateInput(t *testing.T) {
	input := map[string]any{
		"key": "a very long string value",
	}
	_ = TruncateJSONValues(input, 5)
	if input["key"] != "a very long string value" {
		t.Error("input was mutated")
	}
}
