package schema

import "testing"

func TestInferResponseSchema_Object(t *testing.T) {
	body := []byte(`{"name":"iPhone","price":999,"in_stock":true,"tags":["phone","apple"]}`)
	rs := InferResponseSchema(body)
	if rs == nil {
		t.Fatal("expected non-nil schema")
	}
	if rs.Type != "object" {
		t.Errorf("expected type=object, got %s", rs.Type)
	}
	if len(rs.Fields) != 4 {
		t.Fatalf("expected 4 fields, got %d", len(rs.Fields))
	}
	fieldMap := make(map[string]FieldSchema)
	for _, f := range rs.Fields {
		fieldMap[f.Name] = f
	}
	if fieldMap["name"].Type != "string" {
		t.Errorf("expected name=string, got %s", fieldMap["name"].Type)
	}
	if fieldMap["price"].Type != "number" {
		t.Errorf("expected price=number, got %s", fieldMap["price"].Type)
	}
	if fieldMap["in_stock"].Type != "boolean" {
		t.Errorf("expected in_stock=boolean, got %s", fieldMap["in_stock"].Type)
	}
	if fieldMap["tags"].Type != "array" {
		t.Errorf("expected tags=array, got %s", fieldMap["tags"].Type)
	}
	if fieldMap["tags"].Items == nil || fieldMap["tags"].Items.Type != "string" {
		t.Errorf("expected tags.items=string")
	}
}

func TestInferResponseSchema_Array(t *testing.T) {
	body := []byte(`[{"id":1,"title":"Post 1"},{"id":2,"title":"Post 2"}]`)
	rs := InferResponseSchema(body)
	if rs == nil {
		t.Fatal("expected non-nil schema")
	}
	if rs.Type != "array" {
		t.Errorf("expected type=array, got %s", rs.Type)
	}
	if rs.Items == nil {
		t.Fatal("expected Items for array type")
	}
	if rs.Items.Type != "object" {
		t.Errorf("expected items.type=object, got %s", rs.Items.Type)
	}
	if len(rs.Items.Fields) != 2 {
		t.Errorf("expected 2 item fields, got %d", len(rs.Items.Fields))
	}
}

func TestInferResponseSchema_NestedObject(t *testing.T) {
	body := []byte(`{"user":{"name":"Alice","age":30},"score":95}`)
	rs := InferResponseSchema(body)
	if rs == nil {
		t.Fatal("expected non-nil schema")
	}
	fieldMap := make(map[string]FieldSchema)
	for _, f := range rs.Fields {
		fieldMap[f.Name] = f
	}
	if fieldMap["user"].Type != "object" {
		t.Errorf("expected user=object, got %s", fieldMap["user"].Type)
	}
	if len(fieldMap["user"].Fields) != 2 {
		t.Errorf("expected 2 nested fields, got %d", len(fieldMap["user"].Fields))
	}
}

func TestInferResponseSchema_Empty(t *testing.T) {
	rs := InferResponseSchema([]byte(`{}`))
	if rs == nil {
		t.Fatal("expected non-nil schema")
	}
	if rs.Type != "object" {
		t.Errorf("expected type=object, got %s", rs.Type)
	}
	if len(rs.Fields) != 0 {
		t.Errorf("expected 0 fields, got %d", len(rs.Fields))
	}
}

func TestInferResponseSchema_InvalidJSON(t *testing.T) {
	rs := InferResponseSchema([]byte(`not json`))
	if rs != nil {
		t.Errorf("expected nil for invalid JSON, got %+v", rs)
	}
}

func TestInferResponseSchema_MaxDepth(t *testing.T) {
	body := []byte(`{"a":{"b":{"c":{"d":{"e":"deep"}}}}}`)
	rs := InferResponseSchema(body)
	if rs == nil {
		t.Fatal("expected non-nil schema")
	}
	a := findField(rs.Fields, "a")
	if a == nil {
		t.Fatal("missing field a")
	}
	b := findField(a.Fields, "b")
	if b == nil {
		t.Fatal("missing field b")
	}
	c := findField(b.Fields, "c")
	if c == nil {
		t.Fatal("missing field c")
	}
	if c.Type != "object" {
		t.Errorf("expected c=object, got %s", c.Type)
	}
	if len(c.Fields) != 0 {
		t.Errorf("expected 0 fields at max depth, got %d", len(c.Fields))
	}
}

func findField(fields []FieldSchema, name string) *FieldSchema {
	for i := range fields {
		if fields[i].Name == name {
			return &fields[i]
		}
	}
	return nil
}
