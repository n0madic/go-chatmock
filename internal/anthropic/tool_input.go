package anthropic

import "strings"

// ExtractToolInputFromMap extracts tool arguments from known fields.
// Empty string placeholders are ignored.
func ExtractToolInputFromMap(m map[string]any) (any, bool) {
	if m == nil {
		return nil, false
	}
	for _, k := range []string{"arguments", "parameters", "input", "args"} {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		return v, true
	}
	return nil, false
}

// FunctionCallItemKeys returns known IDs that can correlate a function call
// item with argument delta events.
func FunctionCallItemKeys(item map[string]any) []string {
	var keys []string
	for _, k := range []string{"id", "call_id", "item_id"} {
		if v, ok := item[k].(string); ok && strings.TrimSpace(v) != "" {
			keys = append(keys, strings.TrimSpace(v))
		}
	}
	return keys
}

// BufferedToolInput returns the best-known tool args for the given keys.
// Deltas are preferred over placeholders from response.output_item.added.
func BufferedToolInput(keys []string, args map[string]any, deltas map[string]string) any {
	for _, key := range keys {
		if d := strings.TrimSpace(deltas[key]); d != "" {
			return d
		}
	}
	for _, key := range keys {
		v, ok := args[key]
		if !ok || v == nil {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		return v
	}
	return nil
}
