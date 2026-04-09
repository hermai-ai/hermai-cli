package schema

import (
	"encoding/json"
	"sort"
)

const maxInferDepth = 3

// InferResponseSchema examines a JSON response body and produces a
// ResponseSchema describing its structure. Returns nil for invalid JSON.
func InferResponseSchema(body []byte) *ResponseSchema {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	fs := inferField(raw, 0)
	return &ResponseSchema{
		Type:   fs.Type,
		Fields: fs.Fields,
		Items:  fs.Items,
	}
}

func inferField(v any, depth int) FieldSchema {
	switch val := v.(type) {
	case map[string]any:
		if depth >= maxInferDepth {
			return FieldSchema{Type: "object"}
		}
		fields := make([]FieldSchema, 0, len(val))
		for k, child := range val {
			f := inferField(child, depth+1)
			f.Name = k
			fields = append(fields, f)
		}
		sort.Slice(fields, func(i, j int) bool {
			return fields[i].Name < fields[j].Name
		})
		return FieldSchema{Type: "object", Fields: fields}

	case []any:
		if len(val) == 0 {
			return FieldSchema{Type: "array"}
		}
		item := inferField(val[0], depth)
		return FieldSchema{Type: "array", Items: &item}

	case string:
		return FieldSchema{Type: "string"}
	case float64:
		return FieldSchema{Type: "number"}
	case bool:
		return FieldSchema{Type: "boolean"}
	case nil:
		return FieldSchema{Type: "null"}
	default:
		return FieldSchema{Type: "string"}
	}
}
