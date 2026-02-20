package types

import "encoding/json"

// StringPtr returns a pointer to the given string.
func StringPtr(s string) *string {
	return &s
}

// BoolPtr returns a pointer to the given bool.
func BoolPtr(b bool) *bool {
	return &b
}

// IntFromAny converts a JSON-decoded numeric value to int.
// Handles float64, int, and json.Number (all common from json.Unmarshal).
func IntFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// ExtractUsageFromEvent extracts a *Usage from a response.completed SSE event data map.
// Returns nil if no usage data is present.
func ExtractUsageFromEvent(data map[string]any) *Usage {
	resp, _ := data["response"].(map[string]any)
	if resp == nil {
		return nil
	}
	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		return nil
	}
	pt := IntFromAny(usage["input_tokens"])
	ct := IntFromAny(usage["output_tokens"])
	tt := IntFromAny(usage["total_tokens"])
	if tt == 0 {
		tt = pt + ct
	}
	return &Usage{
		PromptTokens:     pt,
		CompletionTokens: ct,
		TotalTokens:      tt,
	}
}
