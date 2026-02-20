package sse

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	anthropicutil "github.com/n0madic/go-chatmock/internal/anthropic"
	"github.com/n0madic/go-chatmock/internal/types"
)

// TranslateAnthropic converts upstream Responses SSE into Anthropic Messages SSE.
func TranslateAnthropic(w http.ResponseWriter, body io.ReadCloser, model string) {
	defer body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	writeEvent := func(event string, payload any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		fmt.Fprintf(w, "event: %s\n", event)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		return true
	}

	type streamState struct {
		messageID      string
		started        bool
		textBlockOpen  bool
		textBlockIndex int
		nextBlockIndex int
		sawToolUse     bool
		toolArgs       map[string]any
		toolArgDeltas  map[string]string
	}
	st := &streamState{
		messageID:      "msg_chatmock_stream",
		textBlockIndex: -1,
		toolArgs:       map[string]any{},
		toolArgDeltas:  map[string]string{},
	}

	startIfNeeded := func() {
		if st.started {
			return
		}
		st.started = true
		_ = writeEvent("message_start", map[string]any{
			"type": "message_start",
			"message": types.AnthropicMessageResponse{
				ID:           st.messageID,
				Type:         "message",
				Role:         "assistant",
				Model:        model,
				Content:      []types.AnthropicContentOut{},
				StopReason:   nil,
				StopSequence: nil,
				Usage:        types.AnthropicUsage{},
			},
		})
	}

	closeTextBlock := func() {
		if !st.textBlockOpen {
			return
		}
		_ = writeEvent("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": st.textBlockIndex,
		})
		st.textBlockOpen = false
		st.textBlockIndex = -1
	}

	reader := NewReader(body)
	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		if id := responseID(evt.Data); id != "" {
			st.messageID = id
		}

		switch evt.Type {
		case "response.output_item.added":
			item, _ := evt.Data["item"].(map[string]any)
			itemType, _ := item["type"].(string)
			if itemType == "function_call" {
				if rawArgs, ok := anthropicutil.ExtractToolInputFromMap(item); ok {
					for _, key := range anthropicutil.FunctionCallItemKeys(item) {
						st.toolArgs[key] = rawArgs
					}
				}
			}

		case "response.function_call_arguments.delta":
			itemID := stringOr(evt.Data, "item_id", stringOr(evt.Data, "call_id", stringOr(evt.Data, "id", "")))
			delta, _ := evt.Data["delta"].(string)
			if itemID != "" && delta != "" {
				st.toolArgDeltas[itemID] += delta
			}

		case "response.function_call_arguments.done":
			itemID := stringOr(evt.Data, "item_id", stringOr(evt.Data, "call_id", stringOr(evt.Data, "id", "")))
			if itemID != "" {
				if rawArgs, ok := anthropicutil.ExtractToolInputFromMap(evt.Data); ok {
					st.toolArgs[itemID] = rawArgs
				}
			}

		case "response.output_text.delta":
			startIfNeeded()
			if !st.textBlockOpen {
				st.textBlockOpen = true
				st.textBlockIndex = st.nextBlockIndex
				st.nextBlockIndex++
				_ = writeEvent("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": st.textBlockIndex,
					"content_block": types.AnthropicContentOut{
						Type: "text",
						Text: "",
					},
				})
			}
			delta, _ := evt.Data["delta"].(string)
			_ = writeEvent("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": st.textBlockIndex,
				"delta": map[string]any{
					"type": "text_delta",
					"text": delta,
				},
			})

		case "response.output_text.done":
			closeTextBlock()

		case "response.output_item.done":
			item, _ := evt.Data["item"].(map[string]any)
			itemType, _ := item["type"].(string)
			if itemType != "function_call" {
				continue
			}
			startIfNeeded()
			closeTextBlock()
			st.sawToolUse = true

			callID := stringOr(item, "call_id", stringOr(item, "id", "tool_use"))
			name := stringOr(item, "name", "")
			rawArgs, _ := anthropicutil.ExtractToolInputFromMap(item)
			if rawArgs == nil {
				rawArgs = anthropicutil.BufferedToolInput(anthropicutil.FunctionCallItemKeys(item), st.toolArgs, st.toolArgDeltas)
			}

			blockIndex := st.nextBlockIndex
			_ = writeEvent("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": blockIndex,
				"content_block": types.AnthropicContentOut{
					Type: "tool_use",
					ID:   callID,
					Name: name,
					// Claude Code expects tool args from input_json_delta events.
					Input: map[string]any{},
				},
			})
			if partialJSON, ok := toolInputPartialJSON(rawArgs); ok {
				_ = writeEvent("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": blockIndex,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": partialJSON,
					},
				})
			}
			_ = writeEvent("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": blockIndex,
			})
			st.nextBlockIndex++

		case "response.failed":
			startIfNeeded()
			closeTextBlock()
			msg := "response.failed"
			if r, ok := evt.Data["response"].(map[string]any); ok {
				if e, ok := r["error"].(map[string]any); ok {
					if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
						msg = strings.TrimSpace(m)
					}
				}
			}
			_ = writeEvent("error", types.AnthropicErrorResponse{
				Type: "error",
				Error: types.AnthropicErrorBody{
					Type:    "api_error",
					Message: msg,
				},
			})
			return

		case "response.completed":
			startIfNeeded()
			closeTextBlock()

			usage := types.ExtractUsageFromEvent(evt.Data)
			u := types.AnthropicUsage{}
			if usage != nil {
				u.InputTokens = usage.PromptTokens
				u.OutputTokens = usage.CompletionTokens
			}

			stopReason := "end_turn"
			if st.sawToolUse {
				stopReason = "tool_use"
			}
			_ = writeEvent("message_delta", map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   stopReason,
					"stop_sequence": nil,
				},
				"usage": u,
			})
			_ = writeEvent("message_stop", map[string]any{
				"type": "message_stop",
			})
			return
		}
	}

	if st.started {
		closeTextBlock()
		_ = writeEvent("message_delta", map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   "end_turn",
				"stop_sequence": nil,
			},
			"usage": types.AnthropicUsage{},
		})
		_ = writeEvent("message_stop", map[string]any{"type": "message_stop"})
	}
}

func responseID(data map[string]any) string {
	if data == nil {
		return ""
	}
	resp, _ := data["response"].(map[string]any)
	if resp == nil {
		return ""
	}
	id, _ := resp["id"].(string)
	return id
}

func toolInputPartialJSON(raw any) (string, bool) {
	switch v := raw.(type) {
	case nil:
		return "", false
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return "", false
		}
		// Keep both full JSON and partial fragments as-is for Claude-compatible
		// input_json_delta reconstruction.
		return s, true
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}
