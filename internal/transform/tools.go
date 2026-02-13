package transform

import (
	"github.com/n0madic/go-chatmock/internal/types"
)

// ToolsChatToResponses converts OpenAI-format tools to Responses API tools.
func ToolsChatToResponses(tools []types.ChatTool) []types.ResponsesTool {
	var out []types.ResponsesTool
	for _, t := range tools {
		if t.Type != "function" {
			continue
		}
		if t.Function == nil || t.Function.Name == "" {
			continue
		}
		params := t.Function.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, types.ResponsesTool{
			Type:        "function",
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Strict:      types.BoolPtr(false),
			Parameters:  params,
		})
	}
	return out
}
