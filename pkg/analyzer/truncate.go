package analyzer

// TruncateJSONValues preserves the full key structure of a JSON-like value
// but truncates string values to maxLen characters (appending "..."),
// caps arrays to the first 3 items, and recurses into nested maps and arrays.
// Numbers, bools, and nil are preserved unchanged.
// Returns a new value; the input is never mutated.
func TruncateJSONValues(v any, maxLen int) any {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case string:
		if len(val) > maxLen {
			return val[:maxLen] + "..."
		}
		return val

	case map[string]any:
		result := make(map[string]any, len(val))
		for k, inner := range val {
			result[k] = TruncateJSONValues(inner, maxLen)
		}
		return result

	case []any:
		cap := 3
		if len(val) < cap {
			cap = len(val)
		}
		result := make([]any, cap)
		for i := 0; i < cap; i++ {
			result[i] = TruncateJSONValues(val[i], maxLen)
		}
		return result

	default:
		return val
	}
}
