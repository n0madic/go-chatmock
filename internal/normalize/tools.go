package normalize

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/transform"
	"github.com/n0madic/go-chatmock/internal/types"
)

var errInvalidResponsesTool = errors.New("invalid responses_tool")

// NormalizeTools resolves tools from mixed Chat/Responses formats.
func NormalizeTools(
	raw map[string]any,
	responseFormat string,
	chatReq types.ChatCompletionRequest,
	responsesReq types.ResponsesRequest,
	toolChoice any,
	defaultWebSearch bool,
) (tools []types.ResponsesTool, baseTools []types.ResponsesTool, hadResponsesTools bool, defaultWebSearchApplied bool, nerr *NormalizeError) {
	chatTools := transform.ToolsChatToResponses(chatReq.Tools)
	responsesTools := sanitizeResponsesTools(responsesReq.Tools)
	responsesStyleTools := parseResponsesStyleToolsFromRaw(raw["tools"])

	var primary []types.ResponsesTool
	if responseFormat == "chat" {
		primary = chatTools
		if len(primary) == 0 {
			if len(responsesTools) > 0 {
				primary = responsesTools
			} else {
				primary = responsesStyleTools
			}
		}
	} else {
		primary = responsesTools
		if len(primary) == 0 {
			if len(chatTools) > 0 {
				primary = chatTools
			} else {
				primary = responsesStyleTools
			}
		}
	}

	extraTools, err := parseExplicitResponsesTools(chatReq.ResponsesTools)
	if err != nil {
		return nil, nil, false, false, &NormalizeError{
			StatusCode: http.StatusBadRequest,
			Message:    "Only web_search/web_search_preview are supported in responses_tools",
		}
	}
	baseTools = cloneResponsesTools(primary)
	tools = cloneResponsesTools(primary)
	if len(extraTools) > 0 {
		tools = append(tools, extraTools...)
	}
	hadResponsesTools = len(extraTools) > 0

	if len(tools) == 0 && defaultWebSearch {
		tc, _ := toolChoice.(string)
		if strings.TrimSpace(tc) != "none" {
			tools = []types.ResponsesTool{{Type: "web_search"}}
			defaultWebSearchApplied = true
		}
	}

	return tools, baseTools, hadResponsesTools, defaultWebSearchApplied, nil
}

func parseExplicitResponsesTools(responsesTools []any) ([]types.ResponsesTool, error) {
	if responsesTools == nil {
		return nil, nil
	}
	var out []types.ResponsesTool
	for _, t := range responsesTools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		ttype := strings.TrimSpace(stringFromAny(tm["type"]))
		if ttype != "web_search" && ttype != "web_search_preview" {
			return nil, errInvalidResponsesTool
		}
		out = append(out, types.ResponsesTool{Type: ttype})
	}
	return out, nil
}

func sanitizeResponsesTools(in []types.ResponsesTool) []types.ResponsesTool {
	if len(in) == 0 {
		return nil
	}
	out := make([]types.ResponsesTool, 0, len(in))
	for _, t := range in {
		switch t.Type {
		case "function":
			if strings.TrimSpace(t.Name) == "" {
				continue
			}
			if t.Parameters == nil {
				t.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			if t.Strict == nil {
				t.Strict = types.BoolPtr(false)
			}
		case "custom":
			if strings.TrimSpace(t.Name) == "" {
				continue
			}
		case "web_search", "web_search_preview":
			// pass through
		default:
			continue
		}
		out = append(out, t)
	}
	return out
}

func parseResponsesStyleToolsFromRaw(rawTools any) []types.ResponsesTool {
	toolsSlice, ok := rawTools.([]any)
	if !ok || len(toolsSlice) == 0 {
		return nil
	}
	hasTopLevelName := false
	for _, raw := range toolsSlice {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := m["name"].(string); name != "" {
			hasTopLevelName = true
			break
		}
	}
	if !hasTopLevelName {
		return nil
	}
	toolBytes, err := json.Marshal(toolsSlice)
	if err != nil {
		return nil
	}
	var parsed []types.ResponsesTool
	if err := json.Unmarshal(toolBytes, &parsed); err != nil {
		return nil
	}
	var out []types.ResponsesTool
	for _, t := range parsed {
		switch t.Type {
		case "function":
			if strings.TrimSpace(t.Name) == "" {
				continue
			}
			if t.Parameters == nil {
				t.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			if t.Strict == nil {
				t.Strict = types.BoolPtr(false)
			}
		case "custom":
			if strings.TrimSpace(t.Name) == "" {
				continue
			}
		case "web_search", "web_search_preview":
			// pass through
		default:
			continue
		}
		out = append(out, t)
	}
	return out
}

func cloneResponsesTools(tools []types.ResponsesTool) []types.ResponsesTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]types.ResponsesTool, len(tools))
	copy(out, tools)
	return out
}

// ExtractResponsesTools parses responses_tools for Ollama and handles defaults.
func ExtractResponsesTools(responsesTools []any, responsesToolChoice string, defaultWebSearch bool) ([]types.ResponsesTool, bool) {
	if responsesTools == nil {
		if defaultWebSearch {
			if responsesToolChoice != "none" {
				return []types.ResponsesTool{{Type: "web_search"}}, true
			}
		}
		return nil, false
	}
	var extraTools []types.ResponsesTool
	for _, t := range responsesTools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		ttype, _ := tm["type"].(string)
		if ttype != "web_search" && ttype != "web_search_preview" {
			return nil, true // signal error
		}
		extraTools = append(extraTools, types.ResponsesTool{Type: ttype})
	}
	if len(extraTools) == 0 && defaultWebSearch {
		if responsesToolChoice != "none" {
			extraTools = []types.ResponsesTool{{Type: "web_search"}}
		}
	}
	return extraTools, len(extraTools) > 0
}
