package sse

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/types"
)

// TranslateChatOptions holds options for SSE chat translation.
type TranslateChatOptions struct {
	ReasoningCompat string
	Verbose         bool
	IncludeUsage    bool
}

// chatTranslatorState holds all mutable state for an in-progress TranslateChat call.
type chatTranslatorState struct {
	// Per-stream config
	model   string
	created int64
	compat  string
	opts    TranslateChatOptions

	// Response state
	responseID              string
	thinkOpen               bool
	thinkClosed             bool
	sentStopChunk           bool
	sawAnySummary           bool
	pendingSummaryParagraph bool
	upstreamUsage           *types.Usage

	// Web search tracking
	wsState     map[string]map[string]any
	wsIndex     map[string]int
	wsNextIndex int

	// Output helpers (set once, closed over in TranslateChat)
	writeChunk func(any)
	writeDone  func()
}

func (st *chatTranslatorState) makeDelta(delta types.ChatDelta) types.ChatCompletionChunk {
	return types.ChatCompletionChunk{
		ID:      st.responseID,
		Object:  "chat.completion.chunk",
		Created: st.created,
		Model:   st.model,
		Choices: []types.ChatChunkChoice{
			{Index: 0, Delta: delta, FinishReason: nil},
		},
	}
}

// handleWebSearchEvent processes any event whose type contains "web_search_call".
func (st *chatTranslatorState) handleWebSearchEvent(kind string, data map[string]any) {
	callID, _ := data["item_id"].(string)
	if callID == "" {
		callID = "ws_call"
	}
	if _, ok := st.wsState[callID]; !ok {
		st.wsState[callID] = map[string]any{}
	}
	mergeWebSearchParams(st.wsState[callID], data)
	if item, ok := data["item"].(map[string]any); ok {
		mergeWebSearchParams(st.wsState[callID], item)
	}
	argsStr := serializeToolArgs(st.wsState[callID])
	if _, ok := st.wsIndex[callID]; !ok {
		st.wsIndex[callID] = st.wsNextIndex
		st.wsNextIndex++
	}
	idx := st.wsIndex[callID]
	st.writeChunk(types.ChatCompletionChunk{
		ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
		Choices: []types.ChatChunkChoice{{
			Index: 0,
			Delta: types.ChatDelta{
				ToolCalls: []types.ToolCall{{
					Index: idx, ID: callID, Type: "function",
					Function: types.FunctionCall{Name: "web_search", Arguments: argsStr},
				}},
			},
			FinishReason: nil,
		}},
	})
	if strings.HasSuffix(kind, ".completed") || strings.HasSuffix(kind, ".done") {
		st.writeChunk(types.ChatCompletionChunk{
			ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
			Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("tool_calls")}},
		})
	}
}

// handleOutputItemDone processes "response.output_item.done" events for function/web-search calls.
func (st *chatTranslatorState) handleOutputItemDone(data map[string]any) {
	item, _ := data["item"].(map[string]any)
	itemType, _ := item["type"].(string)
	if itemType != "function_call" && itemType != "web_search_call" {
		return
	}

	callID := stringOr(item, "call_id", stringOr(item, "id", ""))
	name := stringOr(item, "name", "")
	if itemType == "web_search_call" && name == "" {
		name = "web_search"
	}

	rawArgs := item["arguments"]
	if rawArgs == nil {
		rawArgs = item["parameters"]
	}
	if argsMap, ok := rawArgs.(map[string]any); ok {
		if _, ok := st.wsState[callID]; !ok {
			st.wsState[callID] = map[string]any{}
		}
		maps.Copy(st.wsState[callID], argsMap)
	}

	effArgs := st.wsState[callID]
	if effArgs == nil {
		switch a := rawArgs.(type) {
		case map[string]any:
			effArgs = a
		default:
			effArgs = map[string]any{}
		}
	}

	argsStr := serializeToolArgs(effArgs)
	if _, ok := st.wsIndex[callID]; !ok {
		st.wsIndex[callID] = st.wsNextIndex
		st.wsNextIndex++
	}
	idx := st.wsIndex[callID]

	if callID != "" && name != "" {
		st.writeChunk(types.ChatCompletionChunk{
			ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
			Choices: []types.ChatChunkChoice{{
				Index: 0,
				Delta: types.ChatDelta{
					ToolCalls: []types.ToolCall{{
						Index: idx, ID: callID, Type: "function",
						Function: types.FunctionCall{Name: name, Arguments: argsStr},
					}},
				},
				FinishReason: nil,
			}},
		})
		st.writeChunk(types.ChatCompletionChunk{
			ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
			Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("tool_calls")}},
		})
	}
}

// handleReasoningDelta processes reasoning summary/text delta events for all compat modes.
func (st *chatTranslatorState) handleReasoningDelta(kind string, data map[string]any) {
	deltaTxt, _ := data["delta"].(string)
	switch st.compat {
	case "o3":
		if kind == "response.reasoning_summary_text.delta" && st.pendingSummaryParagraph {
			st.writeChunk(types.ChatCompletionChunk{
				ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
				Choices: []types.ChatChunkChoice{{
					Index: 0,
					Delta: types.ChatDelta{Reasoning: types.ReasoningContent{
						Content: []types.ReasoningPart{{Type: "text", Text: "\n"}},
					}},
					FinishReason: nil,
				}},
			})
			st.pendingSummaryParagraph = false
		}
		st.writeChunk(types.ChatCompletionChunk{
			ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
			Choices: []types.ChatChunkChoice{{
				Index: 0,
				Delta: types.ChatDelta{Reasoning: types.ReasoningContent{
					Content: []types.ReasoningPart{{Type: "text", Text: deltaTxt}},
				}},
				FinishReason: nil,
			}},
		})

	case "think-tags":
		if !st.thinkOpen && !st.thinkClosed {
			st.writeChunk(st.makeDelta(types.ChatDelta{Content: "<think>"}))
			st.thinkOpen = true
		}
		if st.thinkOpen && !st.thinkClosed {
			if kind == "response.reasoning_summary_text.delta" && st.pendingSummaryParagraph {
				st.writeChunk(st.makeDelta(types.ChatDelta{Content: "\n"}))
				st.pendingSummaryParagraph = false
			}
			st.writeChunk(st.makeDelta(types.ChatDelta{Content: deltaTxt}))
		}

	default: // legacy
		if kind == "response.reasoning_summary_text.delta" {
			st.writeChunk(types.ChatCompletionChunk{
				ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
				Choices: []types.ChatChunkChoice{{
					Index:        0,
					Delta:        types.ChatDelta{ReasoningSummary: deltaTxt, Reasoning: deltaTxt},
					FinishReason: nil,
				}},
			})
		} else {
			st.writeChunk(types.ChatCompletionChunk{
				ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
				Choices: []types.ChatChunkChoice{{
					Index: 0, Delta: types.ChatDelta{Reasoning: deltaTxt}, FinishReason: nil,
				}},
			})
		}
	}
}

// TranslateChat reads upstream SSE events and writes OpenAI chat completion SSE chunks to the response writer.
func TranslateChat(w http.ResponseWriter, body io.ReadCloser, model string, created int64, opts TranslateChatOptions) {
	defer body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	compat := strings.ToLower(strings.TrimSpace(opts.ReasoningCompat))
	if compat == "" {
		compat = "think-tags"
	}

	st := &chatTranslatorState{
		model:       model,
		created:     created,
		compat:      compat,
		opts:        opts,
		responseID:  "chatcmpl-stream",
		wsState:     map[string]map[string]any{},
		wsIndex:     map[string]int{},
		wsNextIndex: 0,
	}

	st.writeChunk = func(chunk any) {
		data, err := json.Marshal(chunk)
		if err != nil {
			slog.Error("failed to marshal SSE chunk", "error", err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	st.writeDone = func() {
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}

	reader := NewReader(body)

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		kind := evt.Type

		// Track response ID
		if resp, ok := evt.Data["response"].(map[string]any); ok {
			if id, ok := resp["id"].(string); ok && id != "" {
				st.responseID = id
			}
		}

		// Web search events
		if strings.Contains(kind, "web_search_call") {
			st.handleWebSearchEvent(kind, evt.Data)
		}

		switch kind {
		case "response.output_text.delta":
			delta, _ := evt.Data["delta"].(string)
			if st.compat == "think-tags" && st.thinkOpen && !st.thinkClosed {
				st.writeChunk(st.makeDelta(types.ChatDelta{Content: "</think>"}))
				st.thinkOpen = false
				st.thinkClosed = true
			}
			st.writeChunk(st.makeDelta(types.ChatDelta{Content: delta}))

		case "response.output_item.done":
			st.handleOutputItemDone(evt.Data)

		case "response.reasoning_summary_part.added":
			if st.compat == "think-tags" || st.compat == "o3" {
				if st.sawAnySummary {
					st.pendingSummaryParagraph = true
				} else {
					st.sawAnySummary = true
				}
			}

		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			st.handleReasoningDelta(kind, evt.Data)

		case "response.output_text.done":
			st.writeChunk(types.ChatCompletionChunk{
				ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
				Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("stop")}},
			})
			st.sentStopChunk = true

		case "response.failed":
			errMsg := "response.failed"
			if resp, ok := evt.Data["response"].(map[string]any); ok {
				if e, ok := resp["error"].(map[string]any); ok {
					if m, ok := e["message"].(string); ok {
						errMsg = m
					}
				}
			}
			st.writeChunk(types.ErrorResponse{Error: types.ErrorDetail{Message: errMsg}})

		case "response.completed":
			st.upstreamUsage = types.ExtractUsageFromEvent(evt.Data)
			if st.compat == "think-tags" && st.thinkOpen && !st.thinkClosed {
				st.writeChunk(st.makeDelta(types.ChatDelta{Content: "</think>"}))
				st.thinkOpen = false
				st.thinkClosed = true
			}
			if !st.sentStopChunk {
				st.writeChunk(types.ChatCompletionChunk{
					ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
					Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("stop")}},
				})
				st.sentStopChunk = true
			}
			if st.opts.IncludeUsage && st.upstreamUsage != nil {
				st.writeChunk(types.ChatCompletionChunk{
					ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
					Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: nil}},
					Usage:   st.upstreamUsage,
				})
			}
			st.writeDone()
			return
		}
	}

	// Stream ended without response.completed
	if st.compat == "think-tags" && st.thinkOpen && !st.thinkClosed {
		st.writeChunk(st.makeDelta(types.ChatDelta{Content: "</think>"}))
	}
	if !st.sentStopChunk {
		st.writeChunk(types.ChatCompletionChunk{
			ID: st.responseID, Object: "chat.completion.chunk", Created: st.created, Model: st.model,
			Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("stop")}},
		})
	}
	st.writeDone()
}

func stringOr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func serializeToolArgs(args any) string {
	switch a := args.(type) {
	case map[string]any:
		b, _ := json.Marshal(a)
		return string(b)
	case []any:
		b, _ := json.Marshal(a)
		return string(b)
	case string:
		var parsed any
		if json.Unmarshal([]byte(a), &parsed) == nil {
			b, _ := json.Marshal(parsed)
			return string(b)
		}
		b, _ := json.Marshal(map[string]any{"query": a})
		return string(b)
	}
	return "{}"
}

func mergeWebSearchParams(dst map[string]any, src map[string]any) {
	for _, key := range []string{"parameters", "args", "arguments", "input"} {
		if params, ok := src[key].(map[string]any); ok {
			maps.Copy(dst, params)
		}
	}
	if q, ok := src["query"].(string); ok {
		if _, exists := dst["query"]; !exists {
			dst["query"] = q
		}
	}
	if q, ok := src["q"].(string); ok {
		if _, exists := dst["query"]; !exists {
			dst["query"] = q
		}
	}
}
