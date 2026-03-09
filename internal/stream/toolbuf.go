package stream

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// MaxToolArgBufSize is the upper bound (in bytes) for buffered function-call
// argument deltas per tool call.
const MaxToolArgBufSize = 1 << 20 // 1 MB

// ToolBuffer accumulates function-call arguments from upstream SSE events.
// It is shared by all codec stream translators instead of being duplicated.
type ToolBuffer struct {
	// ToolArgs maps item/call IDs to their resolved argument values.
	ToolArgs map[string]any
	// ToolArgBuf accumulates delta strings per item/call ID.
	ToolArgBuf map[string]string
	// ToolItemMap maps item_id to call_id for cross-referencing.
	ToolItemMap map[string]string
}

// NewToolBuffer creates a new empty ToolBuffer.
func NewToolBuffer() *ToolBuffer {
	return &ToolBuffer{
		ToolArgs:    map[string]any{},
		ToolArgBuf:  map[string]string{},
		ToolItemMap: map[string]string{},
	}
}

// OnOutputItemAdded processes response.output_item.added events for function_call items.
// It extracts item/call ID mapping and any early arguments.
func (tb *ToolBuffer) OnOutputItemAdded(item map[string]any) {
	if item == nil {
		return
	}

	itemID := strings.TrimSpace(StringOr(item, "id"))
	callID := strings.TrimSpace(StringOr(item, "call_id", itemID))
	if itemID != "" && callID != "" && itemID != callID {
		tb.ToolItemMap[itemID] = callID
	}

	rawArgs := ExtractRawToolArgs(item)
	if IsEmptyToolArgs(rawArgs) {
		return
	}
	if itemID != "" {
		tb.ToolArgs[itemID] = rawArgs
	}
	if callID != "" {
		tb.ToolArgs[callID] = rawArgs
	}
}

// OnArgumentsDelta processes response.function_call_arguments.delta events.
func (tb *ToolBuffer) OnArgumentsDelta(data map[string]any) {
	itemID := strings.TrimSpace(StringOr(data, "item_id", StringOr(data, "call_id", StringOr(data, "id", ""))))
	delta, _ := data["delta"].(string)
	if itemID == "" || delta == "" {
		return
	}
	if len(tb.ToolArgBuf[itemID])+len(delta) > MaxToolArgBufSize {
		slog.Warn("toolArgBuf size limit exceeded, dropping delta", "item_id", itemID, "buf_len", len(tb.ToolArgBuf[itemID]), "delta_len", len(delta))
		return
	}
	tb.ToolArgBuf[itemID] += delta
	if callID := strings.TrimSpace(tb.ToolItemMap[itemID]); callID != "" && callID != itemID {
		if len(tb.ToolArgBuf[callID])+len(delta) > MaxToolArgBufSize {
			slog.Warn("toolArgBuf size limit exceeded, dropping delta", "call_id", callID, "buf_len", len(tb.ToolArgBuf[callID]), "delta_len", len(delta))
			return
		}
		tb.ToolArgBuf[callID] += delta
	}
}

// OnArgumentsDone processes response.function_call_arguments.done events.
func (tb *ToolBuffer) OnArgumentsDone(data map[string]any) {
	itemID := strings.TrimSpace(StringOr(data, "item_id", StringOr(data, "call_id", StringOr(data, "id", ""))))
	callID := strings.TrimSpace(tb.ToolItemMap[itemID])
	rawArgs := ExtractRawToolArgs(data)
	if IsEmptyToolArgs(rawArgs) {
		if item, ok := data["item"].(map[string]any); ok {
			rawArgs = ExtractRawToolArgs(item)
		}
	}
	if IsEmptyToolArgs(rawArgs) {
		return
	}
	if itemID != "" {
		tb.ToolArgs[itemID] = rawArgs
	}
	if callID != "" {
		tb.ToolArgs[callID] = rawArgs
	}
}

// ResolveArgs resolves the best-known arguments for a completed output item.
// Returns the args and true if found, or nil and false.
func (tb *ToolBuffer) ResolveArgs(item map[string]any) (any, bool) {
	itemID := strings.TrimSpace(StringOr(item, "id"))
	callID := strings.TrimSpace(StringOr(item, "call_id", itemID))
	keys := []string{itemID}
	if callID != "" && callID != itemID {
		keys = append(keys, callID)
	}
	if mapped := strings.TrimSpace(tb.ToolItemMap[itemID]); mapped != "" && mapped != callID {
		keys = append(keys, mapped)
	}

	for _, key := range keys {
		if key == "" {
			continue
		}
		if raw, ok := tb.ToolArgs[key]; ok && !IsEmptyToolArgs(raw) {
			return raw, true
		}
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		buf := strings.TrimSpace(tb.ToolArgBuf[key])
		if buf == "" {
			continue
		}
		var parsed any
		if json.Unmarshal([]byte(buf), &parsed) == nil {
			return parsed, true
		}
		return buf, true
	}
	return nil, false
}

// --- Shared utility functions used by ToolBuffer and codec encoders ---

// StringOr returns the first non-empty string value for the given keys.
func StringOr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// ExtractRawToolArgs extracts arguments/parameters/input from an item map.
func ExtractRawToolArgs(item map[string]any) any {
	if item == nil {
		return nil
	}
	for _, key := range []string{"arguments", "parameters", "input"} {
		if val, ok := item[key]; ok {
			return val
		}
	}
	return nil
}

// IsEmptyToolArgs returns true if the args represent an empty/null value.
func IsEmptyToolArgs(args any) bool {
	switch v := args.(type) {
	case nil:
		return true
	case string:
		trimmed := strings.TrimSpace(v)
		return trimmed == "" || trimmed == "{}" || trimmed == "null"
	case map[string]any:
		return len(v) == 0
	case []any:
		return len(v) == 0
	default:
		return false
	}
}

// SerializeToolArgs converts args to a JSON string for chat completion chunks.
func SerializeToolArgs(args any, queryFallback bool) string {
	switch a := args.(type) {
	case map[string]any:
		b, _ := json.Marshal(a)
		return string(b)
	case []any:
		b, _ := json.Marshal(a)
		return string(b)
	case string:
		raw := strings.TrimSpace(a)
		if raw == "" {
			return "{}"
		}
		var parsed any
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			b, _ := json.Marshal(parsed)
			return string(b)
		}
		if queryFallback {
			b, _ := json.Marshal(map[string]any{"query": raw})
			return string(b)
		}
		return raw
	}
	return "{}"
}
