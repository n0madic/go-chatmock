package codec

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/n0madic/go-chatmock/internal/stream"
	"github.com/n0madic/go-chatmock/internal/types"
)

// AnthropicEncoder encodes responses in Anthropic Messages format.
type AnthropicEncoder struct{}

func (e *AnthropicEncoder) WriteStreamHeaders(w http.ResponseWriter, statusCode int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(statusCode)
}

func (e *AnthropicEncoder) StreamTranslator(w http.ResponseWriter, model string, opts StreamOpts) Translator {
	return &anthropicStreamTranslator{w: w, model: model}
}

func (e *AnthropicEncoder) WriteCollected(w http.ResponseWriter, statusCode int, resp *CollectedResponse, model string) {
	if resp.ErrorMessage != "" {
		WriteAnthropicError(w, http.StatusBadGateway, "api_error", resp.ErrorMessage)
		return
	}

	var content []types.AnthropicContentOut
	if resp.FullText != "" {
		content = append(content, types.AnthropicContentOut{
			Type: "text",
			Text: resp.FullText,
		})
	}

	sawToolUse := false
	for _, tc := range resp.ToolCalls {
		content = append(content, types.AnthropicContentOut{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: parseAnthropicToolInputAny(tc.Function.Arguments),
		})
		sawToolUse = true
	}

	var usageObj types.AnthropicUsage
	if resp.Usage != nil {
		usageObj = types.AnthropicUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		}
	}

	stopReason := "end_turn"
	if sawToolUse {
		stopReason = "tool_use"
	}

	result := types.AnthropicMessageResponse{
		ID:           resp.ResponseID,
		Type:         "message",
		Role:         "assistant",
		Model:        model,
		Content:      content,
		StopReason:   types.StringPtr(stopReason),
		StopSequence: nil,
		Usage:        usageObj,
	}
	WriteJSON(w, statusCode, result)
}

func (e *AnthropicEncoder) WriteError(w http.ResponseWriter, statusCode int, message string) {
	WriteAnthropicError(w, statusCode, "api_error", message)
}

// anthropicStreamTranslator translates upstream SSE into Anthropic Messages SSE.
type anthropicStreamTranslator struct {
	w     http.ResponseWriter
	model string

	messageID      string
	started        bool
	textBlockOpen  bool
	textBlockIndex int
	nextBlockIndex int
	sawToolUse     bool
	toolArgs       map[string]any
	toolArgDeltas  map[string]string

	flusher    http.Flusher
	writeEvent func(event string, payload any) bool
}

func (t *anthropicStreamTranslator) Translate(reader *stream.Reader) {
	flusher, ok := t.w.(http.Flusher)
	if !ok {
		return
	}
	t.flusher = flusher
	t.messageID = newAnthropicMessageID()
	t.textBlockIndex = -1
	t.toolArgs = map[string]any{}
	t.toolArgDeltas = map[string]string{}

	t.writeEvent = func(event string, payload any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		fmt.Fprintf(t.w, "event: %s\n", event)
		fmt.Fprintf(t.w, "data: %s\n\n", data)
		t.flusher.Flush()
		return true
	}

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		if id := responseIDFromData(evt.Data); id != "" {
			t.messageID = id
		}

		switch evt.Type {
		case "response.output_item.added":
			t.handleOutputItemAdded(evt.Data)

		case "response.function_call_arguments.delta":
			itemID := stream.StringOr(evt.Data, "item_id", stream.StringOr(evt.Data, "call_id", stream.StringOr(evt.Data, "id")))
			delta, _ := evt.Data["delta"].(string)
			if itemID != "" && delta != "" {
				t.toolArgDeltas[itemID] += delta
			}

		case "response.function_call_arguments.done":
			itemID := stream.StringOr(evt.Data, "item_id", stream.StringOr(evt.Data, "call_id", stream.StringOr(evt.Data, "id")))
			if itemID != "" {
				if rawArgs, ok := extractToolInputFromMap(evt.Data); ok {
					t.toolArgs[itemID] = rawArgs
				}
			}

		case "response.output_text.delta":
			t.startIfNeeded()
			if !t.textBlockOpen {
				t.textBlockOpen = true
				t.textBlockIndex = t.nextBlockIndex
				t.nextBlockIndex++
				_ = t.writeEvent("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": t.textBlockIndex,
					"content_block": types.AnthropicContentOut{
						Type: "text",
						Text: "",
					},
				})
			}
			delta, _ := evt.Data["delta"].(string)
			_ = t.writeEvent("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": t.textBlockIndex,
				"delta": map[string]any{
					"type": "text_delta",
					"text": delta,
				},
			})

		case "response.output_text.done":
			t.closeTextBlock()

		case "response.output_item.done":
			t.handleOutputItemDone(evt.Data)

		case "response.failed":
			t.startIfNeeded()
			t.closeTextBlock()
			msg := "response.failed"
			if r, ok := evt.Data["response"].(map[string]any); ok {
				if e, ok := r["error"].(map[string]any); ok {
					if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
						msg = strings.TrimSpace(m)
					}
				}
			}
			_ = t.writeEvent("error", types.AnthropicErrorResponse{
				Type: "error",
				Error: types.AnthropicErrorBody{
					Type:    "api_error",
					Message: msg,
				},
			})
			return

		case "response.completed":
			t.startIfNeeded()
			t.closeTextBlock()

			usage := stream.ExtractUsageFromEvent(evt.Data)
			u := types.AnthropicUsage{}
			if usage != nil {
				u.InputTokens = usage.PromptTokens
				u.OutputTokens = usage.CompletionTokens
			}

			stopReason := "end_turn"
			if t.sawToolUse {
				stopReason = "tool_use"
			}
			_ = t.writeEvent("message_delta", map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   stopReason,
					"stop_sequence": nil,
				},
				"usage": u,
			})
			_ = t.writeEvent("message_stop", map[string]any{
				"type": "message_stop",
			})
			return
		}
	}

	if !t.started {
		t.startIfNeeded()
		_ = t.writeEvent("error", types.AnthropicErrorResponse{
			Type: "error",
			Error: types.AnthropicErrorBody{
				Type:    "api_error",
				Message: "upstream returned empty response",
			},
		})
		return
	}

	t.closeTextBlock()
	_ = t.writeEvent("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": types.AnthropicUsage{},
	})
	_ = t.writeEvent("message_stop", map[string]any{"type": "message_stop"})
}

func (t *anthropicStreamTranslator) startIfNeeded() {
	if t.started {
		return
	}
	t.started = true
	_ = t.writeEvent("message_start", map[string]any{
		"type": "message_start",
		"message": types.AnthropicMessageResponse{
			ID:           t.messageID,
			Type:         "message",
			Role:         "assistant",
			Model:        t.model,
			Content:      []types.AnthropicContentOut{},
			StopReason:   nil,
			StopSequence: nil,
			Usage:        types.AnthropicUsage{},
		},
	})
	_ = t.writeEvent("ping", map[string]any{
		"type": "ping",
	})
}

func (t *anthropicStreamTranslator) closeTextBlock() {
	if !t.textBlockOpen {
		return
	}
	_ = t.writeEvent("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": t.textBlockIndex,
	})
	t.textBlockOpen = false
	t.textBlockIndex = -1
}

func (t *anthropicStreamTranslator) handleOutputItemAdded(data map[string]any) {
	item, _ := data["item"].(map[string]any)
	itemType, _ := item["type"].(string)
	if itemType == "function_call" {
		if rawArgs, ok := extractToolInputFromMap(item); ok {
			for _, key := range functionCallItemKeys(item) {
				t.toolArgs[key] = rawArgs
			}
		}
	}
}

func (t *anthropicStreamTranslator) handleOutputItemDone(data map[string]any) {
	item, _ := data["item"].(map[string]any)
	itemType, _ := item["type"].(string)
	if itemType != "function_call" {
		return
	}
	t.startIfNeeded()
	t.closeTextBlock()
	t.sawToolUse = true

	callID := stream.StringOr(item, "call_id", stream.StringOr(item, "id", "tool_use"))
	name := stream.StringOr(item, "name")
	rawArgs, _ := extractToolInputFromMap(item)
	if rawArgs == nil {
		rawArgs = bufferedToolInput(functionCallItemKeys(item), t.toolArgs, t.toolArgDeltas)
	}

	blockIndex := t.nextBlockIndex
	_ = t.writeEvent("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": blockIndex,
		"content_block": types.AnthropicContentOut{
			Type:  "tool_use",
			ID:    callID,
			Name:  name,
			Input: map[string]any{},
		},
	})
	if partialJSON, ok := toolInputPartialJSON(rawArgs); ok {
		_ = t.writeEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": blockIndex,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": partialJSON,
			},
		})
	}
	_ = t.writeEvent("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": blockIndex,
	})
	t.nextBlockIndex++
}

// --- helpers ---

func responseIDFromData(data map[string]any) string {
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
		return s, true
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}

func newAnthropicMessageID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "msg_" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("msg_%d", time.Now().UnixNano())
}

// extractToolInputFromMap extracts tool arguments from known fields.
// Empty string placeholders are ignored.
func extractToolInputFromMap(m map[string]any) (any, bool) {
	if m == nil {
		return nil, false
	}
	for _, k := range []string{"arguments", "parameters", "input", "args"} {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		return v, true
	}
	return nil, false
}

// functionCallItemKeys returns known IDs that can correlate a function call
// item with argument delta events.
func functionCallItemKeys(item map[string]any) []string {
	var keys []string
	for _, k := range []string{"id", "call_id", "item_id"} {
		if v, ok := item[k].(string); ok && strings.TrimSpace(v) != "" {
			keys = append(keys, strings.TrimSpace(v))
		}
	}
	return keys
}

// bufferedToolInput returns the best-known tool args for the given keys.
// Deltas are preferred over placeholders from response.output_item.added.
func bufferedToolInput(keys []string, args map[string]any, deltas map[string]string) any {
	for _, key := range keys {
		if d := strings.TrimSpace(deltas[key]); d != "" {
			return d
		}
	}
	for _, key := range keys {
		v, ok := args[key]
		if !ok || v == nil {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		return v
	}
	return nil
}

func parseAnthropicToolInputAny(raw any) any {
	switch v := raw.(type) {
	case map[string]any:
		return v
	case []any:
		return v
	case string:
		arguments := strings.TrimSpace(v)
		if arguments == "" {
			return map[string]any{}
		}
		var parsed any
		if err := json.Unmarshal([]byte(arguments), &parsed); err == nil {
			return parsed
		}
		return map[string]any{"raw": arguments}
	default:
		if raw == nil {
			return map[string]any{}
		}
		if b, err := json.Marshal(raw); err == nil {
			var parsed any
			if err := json.Unmarshal(b, &parsed); err == nil {
				return parsed
			}
			return map[string]any{"raw": string(b)}
		}
		return map[string]any{}
	}
}
