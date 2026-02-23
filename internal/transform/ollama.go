package transform

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/n0madic/go-chatmock/internal/types"
)

// ToDataURL converts a raw base64 image string or URL to a proper data URL.
func ToDataURL(imageStr string) string {
	if imageStr == "" {
		return imageStr
	}
	s := strings.TrimSpace(imageStr)
	if strings.HasPrefix(s, "data:image/") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	b64 := strings.NewReplacer("\n", "", "\r", "").Replace(s)
	kind := "image/png"
	if strings.HasPrefix(b64, "/9j/") {
		kind = "image/jpeg"
	} else if strings.HasPrefix(b64, "iVBORw0KGgo") {
		kind = "image/png"
	} else if strings.HasPrefix(b64, "R0lGOD") {
		kind = "image/gif"
	}
	return fmt.Sprintf("data:%s;base64,%s", kind, b64)
}

// ConvertOllamaMessages converts Ollama-format messages to OpenAI chat format.
// Input stays []any because Ollama messages have a custom format requiring dynamic parsing.
func ConvertOllamaMessages(messages []any, topImages []string) []types.ChatMessage {
	var out []types.ChatMessage
	var pendingCallIDs []string
	callCounter := 0

	for _, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "" {
			role = "user"
		}
		nm := types.ChatMessage{Role: role}

		// Extract content
		var parts []map[string]any
		content := msg["content"]
		switch c := content.(type) {
		case []any:
			for _, p := range c {
				pm, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if ptype, _ := pm["type"].(string); ptype == "text" {
					if text, _ := pm["text"].(string); text != "" {
						parts = append(parts, map[string]any{"type": "text", "text": text})
					}
				}
			}
		case string:
			parts = append(parts, map[string]any{"type": "text", "text": c})
		}

		// Handle images
		if images, ok := msg["images"].([]any); ok {
			for _, img := range images {
				if imgStr, ok := img.(string); ok {
					url := ToDataURL(imgStr)
					if url != "" {
						parts = append(parts, map[string]any{
							"type":      "image_url",
							"image_url": map[string]any{"url": url},
						})
					}
				}
			}
		}

		if len(parts) > 0 {
			nm.Content = anySlice(parts)
		}

		// Handle tool_calls on assistant messages
		if role == "assistant" {
			if tcs, ok := msg["tool_calls"].([]any); ok {
				for _, tc := range tcs {
					tcm, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := tcm["function"].(map[string]any)
					name, _ := fn["name"].(string)
					if name == "" {
						continue
					}
					callID, _ := tcm["id"].(string)
					if callID == "" {
						callID, _ = tcm["call_id"].(string)
					}
					if callID == "" {
						callCounter++
						callID = fmt.Sprintf("ollama_call_%d", callCounter)
					}
					pendingCallIDs = append(pendingCallIDs, callID)

					args := "{}"
					if a, ok := fn["arguments"].(string); ok {
						args = a
					} else if a, ok := fn["arguments"].(map[string]any); ok {
						b, _ := json.Marshal(a)
						args = string(b)
					}

					nm.ToolCalls = append(nm.ToolCalls, types.ToolCall{
						ID:   callID,
						Type: "function",
						Function: types.FunctionCall{
							Name:      name,
							Arguments: args,
						},
					})
				}
			}
		}

		// Handle tool messages
		if role == "tool" {
			tci, _ := msg["tool_call_id"].(string)
			if tci == "" {
				tci, _ = msg["id"].(string)
			}
			if tci == "" && len(pendingCallIDs) > 0 {
				tci = pendingCallIDs[0]
				pendingCallIDs = pendingCallIDs[1:]
			}
			if tci != "" {
				nm.ToolCallID = tci
			}
			if len(parts) == 0 {
				if cs, ok := content.(string); ok {
					nm.Content = cs
				}
			}
		}

		out = append(out, nm)
	}

	// Attach top-level images to last user message
	if len(topImages) > 0 {
		attachIdx := -1
		for i := len(out) - 1; i >= 0; i-- {
			if out[i].Role == "user" {
				attachIdx = i
				break
			}
		}
		if attachIdx < 0 {
			out = append(out, types.ChatMessage{Role: "user", Content: []any{}})
			attachIdx = len(out) - 1
		}

		existing := toAnySlice(out[attachIdx].Content)
		for _, img := range topImages {
			url := ToDataURL(img)
			if url != "" {
				existing = append(existing, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": url},
				})
			}
		}
		out[attachIdx].Content = existing
	}

	return out
}

// NormalizeOllamaTools converts Ollama-format tools to OpenAI function tool format.
// Input stays []any because Ollama tools have varying schemas.
func NormalizeOllamaTools(tools []any) []types.ChatTool {
	var out []types.ChatTool
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}

		if fn, ok := tm["function"].(map[string]any); ok {
			name, _ := fn["name"].(string)
			if name == "" {
				continue
			}
			desc, _ := fn["description"].(string)
			params := fn["parameters"]
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			out = append(out, types.ChatTool{
				Type: "function",
				Function: &types.FunctionDef{
					Name:        name,
					Description: desc,
					Parameters:  params,
				},
			})
			continue
		}

		if name, _ := tm["name"].(string); name != "" {
			desc, _ := tm["description"].(string)
			out = append(out, types.ChatTool{
				Type: "function",
				Function: &types.FunctionDef{
					Name:        name,
					Description: desc,
					Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
				},
			})
		}
	}
	return out
}

// anySlice converts []map[string]any to []any for Content field.
func anySlice(parts []map[string]any) []any {
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return out
}

// toAnySlice extracts an []any from a Content field value.
func toAnySlice(v any) []any {
	if v == nil {
		return nil
	}
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}
