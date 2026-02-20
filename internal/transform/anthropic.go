package transform

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/n0madic/go-chatmock/internal/types"
)

// AnthropicMessagesToResponsesInput converts Anthropic Messages API input
// messages into Responses API input items.
func AnthropicMessagesToResponsesInput(messages []types.AnthropicMessage) ([]types.ResponsesInputItem, error) {
	var out []types.ResponsesInputItem
	nextCallID := 1

	for _, msg := range messages {
		role := strings.TrimSpace(strings.ToLower(msg.Role))
		if role == "" {
			continue
		}
		blocks, err := msg.ParseContent()
		if err != nil {
			return nil, err
		}
		if len(blocks) == 0 {
			continue
		}

		var pending []types.ResponsesContent
		flushPending := func() {
			if len(pending) == 0 {
				return
			}
			contentCopy := make([]types.ResponsesContent, len(pending))
			copy(contentCopy, pending)
			out = append(out, types.ResponsesInputItem{
				Type:    "message",
				Role:    normalizeRole(role),
				Content: contentCopy,
			})
			pending = pending[:0]
		}

		for _, block := range blocks {
			blockType := strings.TrimSpace(strings.ToLower(block.Type))
			switch blockType {
			case "", "text":
				txt := block.Text
				if txt == "" {
					continue
				}
				kind := "input_text"
				if role == "assistant" {
					kind = "output_text"
				}
				pending = append(pending, types.ResponsesContent{Type: kind, Text: txt})

			case "tool_use":
				flushPending()
				callID := strings.TrimSpace(block.ID)
				if callID == "" {
					callID = fmt.Sprintf("call_%d", nextCallID)
					nextCallID++
				}
				args := "{}"
				if block.Input != nil {
					if b, err := json.Marshal(block.Input); err == nil {
						args = string(b)
					}
				}
				out = append(out, types.ResponsesInputItem{
					Type:      "function_call",
					Name:      block.Name,
					Arguments: args,
					CallID:    callID,
				})

			case "tool_result":
				flushPending()
				callID := strings.TrimSpace(block.ToolUseID)
				if callID == "" {
					callID = strings.TrimSpace(block.ID)
				}
				out = append(out, types.ResponsesInputItem{
					Type:   "function_call_output",
					CallID: callID,
					Output: types.ParseToolResultText(block.Content),
				})

			default:
				// Keep compatibility by preserving unknown blocks that still carry text.
				if block.Text != "" {
					kind := "input_text"
					if role == "assistant" {
						kind = "output_text"
					}
					pending = append(pending, types.ResponsesContent{Type: kind, Text: block.Text})
				}
			}
		}

		flushPending()
	}

	return out, nil
}

// AnthropicToolsToResponses converts Anthropic Messages tools to Responses tools.
func AnthropicToolsToResponses(tools []types.AnthropicTool) []types.ResponsesTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]types.ResponsesTool, 0, len(tools))
	for _, t := range tools {
		if strings.TrimSpace(t.Name) == "" {
			continue
		}
		out = append(out, types.ResponsesTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}
	return out
}

// AnthropicToolChoiceToResponses maps Anthropic tool_choice values to Responses tool_choice.
func AnthropicToolChoiceToResponses(choice any) any {
	if choice == nil {
		return "auto"
	}
	if s, ok := choice.(string); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "none":
			return "none"
		case "auto":
			return "auto"
		default:
			return "auto"
		}
	}
	m, ok := choice.(map[string]any)
	if !ok {
		return "auto"
	}

	kind, _ := m["type"].(string)
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "none":
		return "none"
	case "auto":
		return "auto"
	case "any":
		return map[string]any{"type": "required"}
	case "tool":
		name, _ := m["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return map[string]any{"type": "required"}
		}
		return map[string]any{"type": "function", "name": name}
	default:
		return "auto"
	}
}

// EstimateResponsesInputTokens returns a deterministic, approximate token count
// suitable for local count_tokens compatibility.
func EstimateResponsesInputTokens(instructions string, input []types.ResponsesInputItem, tools []types.ResponsesTool) int {
	chars := runeLen(instructions)

	for _, item := range input {
		chars += 8 + runeLen(item.Type) + runeLen(item.Role) + runeLen(item.Name) + runeLen(item.CallID)
		chars += runeLen(item.Arguments) + runeLen(item.Output)
		for _, c := range item.Content {
			chars += 4 + runeLen(c.Type) + runeLen(c.Text) + runeLen(c.ImageURL)
		}
	}

	for _, tool := range tools {
		chars += 12 + runeLen(tool.Type) + runeLen(tool.Name) + runeLen(tool.Description)
		if b, err := json.Marshal(tool.Parameters); err == nil {
			chars += runeLen(string(b))
		}
	}

	if chars <= 0 {
		return 1
	}
	tokens := chars / 4
	if chars%4 != 0 {
		tokens++
	}
	if tokens < 1 {
		return 1
	}
	return tokens
}

func normalizeRole(role string) string {
	switch role {
	case "assistant":
		return "assistant"
	case "user":
		return "user"
	default:
		return "user"
	}
}

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}
