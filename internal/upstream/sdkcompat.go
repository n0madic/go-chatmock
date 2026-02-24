package upstream

import (
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/n0madic/go-chatmock/internal/types"
)

// responsesInputItemsToSDKInput converts custom input items to the SDK union type.
func responsesInputItemsToSDKInput(items []types.ResponsesInputItem) responses.ResponseNewParamsInputUnion {
	if len(items) == 0 {
		return responses.ResponseNewParamsInputUnion{}
	}
	sdkItems := make(responses.ResponseInputParam, 0, len(items))
	for _, item := range items {
		if sdkItem, ok := responsesInputItemToSDK(item); ok {
			sdkItems = append(sdkItems, sdkItem)
		}
	}
	return responses.ResponseNewParamsInputUnion{
		OfInputItemList: sdkItems,
	}
}

func responsesInputItemToSDK(item types.ResponsesInputItem) (responses.ResponseInputItemUnionParam, bool) {
	switch item.Type {
	case "message":
		if item.Role == "assistant" {
			return assistantMessageToSDK(item)
		}
		content := responsesContentToSDK(item.Content)
		if len(content) == 0 {
			return responses.ResponseInputItemUnionParam{}, false
		}
		return responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRole(item.Role)), true

	case "function_call":
		if item.CallID == "" || item.Name == "" {
			return responses.ResponseInputItemUnionParam{}, false
		}
		return responses.ResponseInputItemParamOfFunctionCall(item.Arguments, item.CallID, item.Name), true

	case "function_call_output":
		if item.CallID == "" {
			return responses.ResponseInputItemUnionParam{}, false
		}
		return responses.ResponseInputItemParamOfFunctionCallOutput(item.CallID, item.Output), true

	default:
		return responses.ResponseInputItemUnionParam{}, false
	}
}

// assistantMessageToSDK converts an assistant message to the SDK OutputMessage type.
// The upstream API requires assistant messages to use output_text content, not input_text.
func assistantMessageToSDK(item types.ResponsesInputItem) (responses.ResponseInputItemUnionParam, bool) {
	if len(item.Content) == 0 {
		return responses.ResponseInputItemUnionParam{}, false
	}
	var content []responses.ResponseOutputMessageContentUnionParam
	for _, c := range item.Content {
		switch c.Type {
		case "output_text", "text", "input_text":
			if c.Text != "" {
				content = append(content, responses.ResponseOutputMessageContentUnionParam{
					OfOutputText: &responses.ResponseOutputTextParam{
						Text: c.Text,
					},
				})
			}
		}
	}
	if len(content) == 0 {
		return responses.ResponseInputItemUnionParam{}, false
	}
	return responses.ResponseInputItemParamOfOutputMessage(content, "", responses.ResponseOutputMessageStatusCompleted), true
}

func responsesContentToSDK(content []types.ResponsesContent) responses.ResponseInputMessageContentListParam {
	if len(content) == 0 {
		return nil
	}
	out := make(responses.ResponseInputMessageContentListParam, 0, len(content))
	for _, c := range content {
		switch c.Type {
		case "input_text", "output_text", "text":
			if c.Text != "" {
				out = append(out, responses.ResponseInputContentParamOfInputText(c.Text))
			}
		case "input_image":
			if c.ImageURL != "" {
				out = append(out, responses.ResponseInputContentUnionParam{
					OfInputImage: &responses.ResponseInputImageParam{
						ImageURL: openai.String(c.ImageURL),
					},
				})
			}
		}
	}
	return out
}

// responsesToolsToSDKTools converts custom tool definitions to SDK union types.
func responsesToolsToSDKTools(tools []types.ResponsesTool) []responses.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		if sdkTool, ok := responsesToolToSDK(t); ok {
			out = append(out, sdkTool)
		}
	}
	return out
}

func responsesToolToSDK(tool types.ResponsesTool) (responses.ToolUnionParam, bool) {
	switch tool.Type {
	case "function":
		params, _ := tool.Parameters.(map[string]any)
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		strict := false
		if tool.Strict != nil {
			strict = *tool.Strict
		}
		ft := responses.FunctionToolParam{
			Name:       tool.Name,
			Parameters: params,
			Strict:     openai.Bool(strict),
		}
		if tool.Description != "" {
			ft.Description = openai.String(tool.Description)
		}
		return responses.ToolUnionParam{OfFunction: &ft}, true

	case "custom":
		ct := responses.CustomToolParam{
			Name: tool.Name,
		}
		if tool.Description != "" {
			ct.Description = openai.String(tool.Description)
		}
		return responses.ToolUnionParam{OfCustom: &ct}, true

	case "web_search":
		return responses.ToolParamOfWebSearch(responses.WebSearchToolTypeWebSearch), true

	case "web_search_preview":
		return responses.ToolParamOfWebSearchPreview(responses.WebSearchPreviewToolTypeWebSearchPreview), true

	default:
		return responses.ToolUnionParam{}, false
	}
}

// reasoningToSDK converts the custom reasoning param to the SDK type.
func reasoningToSDK(r *types.ReasoningParam) shared.ReasoningParam {
	if r == nil {
		return shared.ReasoningParam{}
	}
	sp := shared.ReasoningParam{
		Effort: shared.ReasoningEffort(r.Effort),
	}
	if r.Summary != "" {
		sp.Summary = shared.ReasoningSummary(r.Summary)
	}
	return sp
}

// toolChoiceToSDK converts a tool_choice value (string or map) to the SDK union type.
func toolChoiceToSDK(choice any) responses.ResponseNewParamsToolChoiceUnion {
	switch tc := choice.(type) {
	case string:
		switch tc {
		case "none":
			return responses.ResponseNewParamsToolChoiceUnion{
				OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsNone),
			}
		case "required":
			return responses.ResponseNewParamsToolChoiceUnion{
				OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsRequired),
			}
		default: // "auto" and anything else
			return responses.ResponseNewParamsToolChoiceUnion{
				OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsAuto),
			}
		}
	case map[string]any:
		// Complex tool choice objects (e.g. {"type":"function","function":{"name":"..."}})
		// are passed through by marshaling to JSON and re-marshaling through the SDK.
		// For now, default to auto for complex objects.
		if typ, _ := tc["type"].(string); typ == "function" {
			if fn, _ := tc["function"].(map[string]any); fn != nil {
				if name, _ := fn["name"].(string); name != "" {
					return responses.ResponseNewParamsToolChoiceUnion{
						OfFunctionTool: &responses.ToolChoiceFunctionParam{
							Name: name,
						},
					}
				}
			}
		}
		if typ, _ := tc["type"].(string); typ == "required" {
			return responses.ResponseNewParamsToolChoiceUnion{
				OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsRequired),
			}
		}
		return responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsAuto),
		}
	default:
		return responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsAuto),
		}
	}
}

// includesToSDK converts string includes to SDK ResponseIncludable values.
func includesToSDK(includes []string) []responses.ResponseIncludable {
	if len(includes) == 0 {
		return nil
	}
	out := make([]responses.ResponseIncludable, len(includes))
	for i, inc := range includes {
		out[i] = responses.ResponseIncludable(inc)
	}
	return out
}
