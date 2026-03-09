package types

import (
	"fmt"
	"strings"
)

// StringPtr returns a pointer to the given string.
func StringPtr(s string) *string {
	return &s
}

// BoolPtr returns a pointer to the given bool.
func BoolPtr(b bool) *bool {
	return &b
}

// BoolPtrState returns a human-readable string for a *bool: "true", "false", or "unset".
func BoolPtrState(v *bool) string {
	if v == nil {
		return "unset"
	}
	if *v {
		return "true"
	}
	return "false"
}

// SummarizeToolChoice returns a short string summarizing a tool_choice value for logging.
func SummarizeToolChoice(choice any) string {
	switch v := choice.(type) {
	case nil:
		return "auto"
	case string:
		val := strings.TrimSpace(v)
		if val == "" {
			return "auto"
		}
		return val
	case map[string]any:
		kind, _ := v["type"].(string)
		if fn, ok := v["function"].(map[string]any); ok {
			if name, _ := fn["name"].(string); name != "" {
				if kind != "" {
					return kind + ":" + name
				}
				return "function:" + name
			}
		}
		if kind != "" {
			return kind
		}
		return "object"
	default:
		return fmt.Sprintf("%T", choice)
	}
}

// FirstNonEmpty returns the first non-empty trimmed string from the given values.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// CloneInputItems creates a deep copy of a ResponsesInputItem slice,
// including a copy of each item's Content slice.
func CloneInputItems(items []ResponsesInputItem) []ResponsesInputItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]ResponsesInputItem, len(items))
	copy(out, items)
	for i := range out {
		if len(items[i].Content) == 0 {
			continue
		}
		content := make([]ResponsesContent, len(items[i].Content))
		copy(content, items[i].Content)
		out[i].Content = content
	}
	return out
}
