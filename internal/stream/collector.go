package stream

import (
	"io"
	"strings"

	"github.com/n0madic/go-chatmock/internal/types"
)

// CollectOptions controls what data is extracted from the SSE stream.
type CollectOptions struct {
	InitialResponseID string
	CollectUsage      bool
	CollectReasoning  bool
	CollectToolCalls  bool
	StopOnFailed      bool
}

// CollectedText holds the result of collecting a text response from SSE.
type CollectedText struct {
	ResponseID       string
	FullText         string
	ReasoningSummary string
	ReasoningFull    string
	ToolCalls        []types.ToolCall
	Usage            *types.Usage
	ErrorMessage     string
}

// CollectTextFromSSE reads an upstream SSE stream and assembles text, tool calls,
// usage, and error info into a CollectedText.
func CollectTextFromSSE(body io.ReadCloser, opts CollectOptions) CollectedText {
	defer body.Close()

	out := CollectedText{
		ResponseID: opts.InitialResponseID,
	}
	reader := NewReader(body)

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		if id := ResponseIDFromEvent(evt.Data); id != "" {
			out.ResponseID = id
		}
		if opts.CollectUsage {
			if usage := ExtractUsageFromEvent(evt.Data); usage != nil {
				out.Usage = usage
			}
		}

		switch evt.Type {
		case "response.output_text.delta":
			delta, _ := evt.Data["delta"].(string)
			out.FullText += delta
		case "response.reasoning_summary_text.delta":
			if opts.CollectReasoning {
				delta, _ := evt.Data["delta"].(string)
				out.ReasoningSummary += delta
			}
		case "response.reasoning_text.delta":
			if opts.CollectReasoning {
				delta, _ := evt.Data["delta"].(string)
				out.ReasoningFull += delta
			}
		case "response.output_item.done":
			if opts.CollectToolCalls {
				item, _ := evt.Data["item"].(map[string]any)
				if tc, ok := FunctionToolCallFromOutputItem(item); ok {
					out.ToolCalls = append(out.ToolCalls, tc)
				}
			}
		case "response.failed":
			out.ErrorMessage = ResponseErrorMessageFromEvent(evt.Data)
			if out.ErrorMessage == "" {
				out.ErrorMessage = "response.failed"
			}
			if opts.StopOnFailed {
				return out
			}
		case "response.completed":
			return out
		}
	}

	return out
}

// FunctionToolCallFromOutputItem extracts a ToolCall from a function_call output item.
func FunctionToolCallFromOutputItem(item map[string]any) (types.ToolCall, bool) {
	if item == nil {
		return types.ToolCall{}, false
	}
	if itemType, _ := item["type"].(string); itemType != "function_call" {
		return types.ToolCall{}, false
	}

	callID := strings.TrimSpace(stringOrEmpty(item, "call_id"))
	if callID == "" {
		callID = strings.TrimSpace(stringOrEmpty(item, "id"))
	}
	name := strings.TrimSpace(stringOrEmpty(item, "name"))
	args := stringOrEmpty(item, "arguments")
	if callID == "" || name == "" {
		return types.ToolCall{}, false
	}

	return types.ToolCall{
		ID:       callID,
		Type:     "function",
		Function: types.FunctionCall{Name: name, Arguments: args},
	}, true
}

// ResponseIDFromEvent extracts the response ID from an SSE event data map.
func ResponseIDFromEvent(data map[string]any) string {
	resp, _ := data["response"].(map[string]any)
	if resp == nil {
		return ""
	}
	return StringFromAny(resp["id"])
}

// ResponseErrorMessageFromEvent extracts the error message from a response.failed event.
func ResponseErrorMessageFromEvent(data map[string]any) string {
	resp, _ := data["response"].(map[string]any)
	if resp == nil {
		return ""
	}
	errObj, _ := resp["error"].(map[string]any)
	if errObj == nil {
		return ""
	}
	msg, _ := errObj["message"].(string)
	return strings.TrimSpace(msg)
}

// StringFromAny converts any value to a trimmed string.
func StringFromAny(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case nil:
		return ""
	default:
		return ""
	}
}

func stringOrEmpty(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
