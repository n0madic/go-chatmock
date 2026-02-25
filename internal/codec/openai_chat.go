package codec

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/reasoning"
	"github.com/n0madic/go-chatmock/internal/stream"
	"github.com/n0madic/go-chatmock/internal/types"
)

// ChatEncoder encodes responses in OpenAI Chat Completions format.
type ChatEncoder struct{}

func (e *ChatEncoder) WriteStreamHeaders(w http.ResponseWriter, statusCode int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(statusCode)
}

func (e *ChatEncoder) StreamTranslator(w http.ResponseWriter, model string, opts StreamOpts) Translator {
	return &chatStreamTranslator{w: w, model: model, opts: opts}
}

func (e *ChatEncoder) WriteCollected(w http.ResponseWriter, statusCode int, resp *CollectedResponse, model string) {
	if resp.ErrorMessage != "" {
		WriteOpenAIError(w, http.StatusBadGateway, resp.ErrorMessage)
		return
	}
	message := types.ChatResponseMsg{Role: "assistant", Content: resp.FullText}
	if len(resp.ToolCalls) > 0 {
		message.ToolCalls = resp.ToolCalls
	}
	reasoning.ApplyReasoningToMessage(&message, resp.ReasoningSummary, resp.ReasoningFull, resp.RawResponse["_reasoning_compat"].(string))
	completion := types.ChatCompletionResponse{
		ID:      resp.ResponseID,
		Object:  "chat.completion",
		Created: 0, // caller fills in
		Model:   model,
		Choices: []types.ChatChoice{
			{Index: 0, Message: message, FinishReason: types.StringPtr("stop")},
		},
		Usage: resp.Usage,
	}
	WriteJSON(w, statusCode, completion)
}

func (e *ChatEncoder) WriteError(w http.ResponseWriter, statusCode int, message string) {
	WriteOpenAIError(w, statusCode, message)
}

// chatStreamTranslator translates upstream SSE into OpenAI chat completion chunks.
type chatStreamTranslator struct {
	w     http.ResponseWriter
	model string
	opts  StreamOpts

	responseID              string
	thinkOpen               bool
	thinkClosed             bool
	sentStopChunk           bool
	sawAnySummary           bool
	pendingSummaryParagraph bool
	upstreamUsage           *types.Usage

	wsState     map[string]map[string]any
	wsIndex     map[string]int
	wsNextIndex int
	hiddenText  map[string]bool

	tb          *stream.ToolBuffer
	writeChunk  func(any)
	writeDone   func()
	writeFailed bool
	compat      string
}

func (t *chatStreamTranslator) Translate(reader *stream.Reader) {
	flusher, ok := t.w.(http.Flusher)
	if !ok {
		return
	}

	t.compat = strings.ToLower(strings.TrimSpace(t.opts.ReasoningCompat))
	if t.compat == "" {
		t.compat = "think-tags"
	}
	t.responseID = "chatcmpl-stream"
	t.wsState = map[string]map[string]any{}
	t.wsIndex = map[string]int{}
	t.hiddenText = map[string]bool{}
	t.tb = stream.NewToolBuffer()

	t.writeChunk = func(chunk any) {
		if t.writeFailed {
			return
		}
		data, err := json.Marshal(chunk)
		if err != nil {
			slog.Error("failed to marshal SSE chunk", "error", err)
			return
		}
		if _, err := fmt.Fprintf(t.w, "data: %s\n\n", data); err != nil {
			slog.Debug("client disconnected during SSE write", "error", err)
			t.writeFailed = true
			return
		}
		flusher.Flush()
	}

	t.writeDone = func() {
		if t.writeFailed {
			return
		}
		if _, err := fmt.Fprint(t.w, "data: [DONE]\n\n"); err != nil {
			slog.Debug("client disconnected during SSE done", "error", err)
			t.writeFailed = true
			return
		}
		flusher.Flush()
	}

	gotEvents := false
	for {
		if t.writeFailed {
			break
		}
		evt, err := reader.Next()
		if err != nil {
			break
		}
		gotEvents = true

		kind := evt.Type

		if resp, ok := evt.Data["response"].(map[string]any); ok {
			if id, ok := resp["id"].(string); ok && id != "" {
				t.responseID = id
			}
		}

		if strings.Contains(kind, "web_search_call") {
			t.handleWebSearchEvent(kind, evt.Data)
		}

		switch kind {
		case "response.output_item.added":
			t.handleOutputItemAdded(evt.Data)
		case "response.function_call_arguments.delta":
			t.tb.OnArgumentsDelta(evt.Data)
		case "response.function_call_arguments.done":
			t.tb.OnArgumentsDone(evt.Data)
		case "response.output_text.delta":
			itemID := strings.TrimSpace(stream.StringOr(evt.Data, "item_id"))
			if itemID != "" && t.hiddenText[itemID] {
				continue
			}
			delta, _ := evt.Data["delta"].(string)
			if t.compat == "think-tags" && t.thinkOpen && !t.thinkClosed {
				t.writeChunk(t.makeDelta(types.ChatDelta{Content: "</think>"}))
				t.thinkOpen = false
				t.thinkClosed = true
			}
			t.writeChunk(t.makeDelta(types.ChatDelta{Content: delta}))
		case "response.output_item.done":
			t.handleOutputItemDone(evt.Data)
		case "response.reasoning_summary_part.added":
			if t.compat == "think-tags" || t.compat == "o3" {
				if t.sawAnySummary {
					t.pendingSummaryParagraph = true
				} else {
					t.sawAnySummary = true
				}
			}
		case "response.reasoning_summary_text.delta":
			t.handleReasoningDelta(kind, evt.Data)
		case "response.reasoning_text.delta":
			continue
		case "response.output_text.done":
			continue
		case "response.failed":
			errMsg := "response.failed"
			if resp, ok := evt.Data["response"].(map[string]any); ok {
				if e, ok := resp["error"].(map[string]any); ok {
					if m, ok := e["message"].(string); ok {
						errMsg = m
					}
				}
			}
			t.writeChunk(types.ErrorResponse{Error: types.ErrorDetail{Message: errMsg}})
		case "response.completed":
			t.upstreamUsage = stream.ExtractUsageFromEvent(evt.Data)
			if t.compat == "think-tags" && t.thinkOpen && !t.thinkClosed {
				t.writeChunk(t.makeDelta(types.ChatDelta{Content: "</think>"}))
				t.thinkOpen = false
				t.thinkClosed = true
			}
			if !t.sentStopChunk {
				t.writeChunk(types.ChatCompletionChunk{
					ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
					Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("stop")}},
				})
				t.sentStopChunk = true
			}
			if t.opts.IncludeUsage && t.upstreamUsage != nil {
				t.writeChunk(types.ChatCompletionChunk{
					ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
					Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: nil}},
					Usage:   t.upstreamUsage,
				})
			}
			t.writeDone()
			return
		}
	}

	if !gotEvents {
		t.writeChunk(types.ErrorResponse{Error: types.ErrorDetail{Message: "upstream returned empty response"}})
		t.writeDone()
		return
	}
	if t.compat == "think-tags" && t.thinkOpen && !t.thinkClosed {
		t.writeChunk(t.makeDelta(types.ChatDelta{Content: "</think>"}))
	}
	if !t.sentStopChunk {
		t.writeChunk(types.ChatCompletionChunk{
			ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
			Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("stop")}},
		})
	}
	t.writeDone()
}

func (t *chatStreamTranslator) makeDelta(delta types.ChatDelta) types.ChatCompletionChunk {
	return types.ChatCompletionChunk{
		ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
		Choices: []types.ChatChunkChoice{{Index: 0, Delta: delta, FinishReason: nil}},
	}
}

func (t *chatStreamTranslator) handleWebSearchEvent(kind string, data map[string]any) {
	callID, _ := data["item_id"].(string)
	if callID == "" {
		callID = "ws_call"
	}
	if _, ok := t.wsState[callID]; !ok {
		t.wsState[callID] = map[string]any{}
	}
	mergeWebSearchParams(t.wsState[callID], data)
	if item, ok := data["item"].(map[string]any); ok {
		mergeWebSearchParams(t.wsState[callID], item)
	}
	argsStr := stream.SerializeToolArgs(t.wsState[callID], true)
	if _, ok := t.wsIndex[callID]; !ok {
		t.wsIndex[callID] = t.wsNextIndex
		t.wsNextIndex++
	}
	idx := t.wsIndex[callID]
	t.writeChunk(types.ChatCompletionChunk{
		ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
		Choices: []types.ChatChunkChoice{{
			Index: 0,
			Delta: types.ChatDelta{ToolCalls: []types.ToolCall{{
				Index: idx, ID: callID, Type: "function",
				Function: types.FunctionCall{Name: "web_search", Arguments: argsStr},
			}}},
			FinishReason: nil,
		}},
	})
	if strings.HasSuffix(kind, ".completed") || strings.HasSuffix(kind, ".done") {
		t.writeChunk(types.ChatCompletionChunk{
			ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
			Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("tool_calls")}},
		})
	}
}

func (t *chatStreamTranslator) handleOutputItemAdded(data map[string]any) {
	item, _ := data["item"].(map[string]any)
	itemType, _ := item["type"].(string)

	if itemType == "message" {
		itemID := strings.TrimSpace(stream.StringOr(item, "id"))
		phase := strings.ToLower(strings.TrimSpace(stream.StringOr(item, "phase")))
		if itemID != "" {
			t.hiddenText[itemID] = phase == "commentary"
		}
		return
	}

	if itemType != "function_call" && itemType != "web_search_call" {
		return
	}
	t.tb.OnOutputItemAdded(item)
}

func (t *chatStreamTranslator) handleOutputItemDone(data map[string]any) {
	item, _ := data["item"].(map[string]any)
	itemType, _ := item["type"].(string)
	if itemType != "function_call" && itemType != "web_search_call" {
		return
	}

	callID := stream.StringOr(item, "call_id", stream.StringOr(item, "id", ""))
	name := stream.StringOr(item, "name", "")
	if itemType == "web_search_call" && name == "" {
		name = "web_search"
	}

	rawArgs := item["arguments"]
	if rawArgs == nil {
		rawArgs = item["parameters"]
	}
	if rawArgs == nil {
		rawArgs = item["input"]
	}
	if stream.IsEmptyToolArgs(rawArgs) {
		if bufferedArgs, ok := t.tb.ResolveArgs(item); ok {
			rawArgs = bufferedArgs
		}
	}
	var argsSource any
	if argsMap, ok := rawArgs.(map[string]any); ok {
		if _, ok := t.wsState[callID]; !ok {
			t.wsState[callID] = map[string]any{}
		}
		maps.Copy(t.wsState[callID], argsMap)
		argsSource = t.wsState[callID]
	} else if rawArgs != nil {
		argsSource = rawArgs
	} else if stateArgs := t.wsState[callID]; stateArgs != nil {
		argsSource = stateArgs
	} else {
		argsSource = map[string]any{}
	}

	argsStr := stream.SerializeToolArgs(argsSource, itemType == "web_search_call")
	if _, ok := t.wsIndex[callID]; !ok {
		t.wsIndex[callID] = t.wsNextIndex
		t.wsNextIndex++
	}
	idx := t.wsIndex[callID]

	if callID != "" && name != "" {
		t.writeChunk(types.ChatCompletionChunk{
			ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
			Choices: []types.ChatChunkChoice{{
				Index: 0,
				Delta: types.ChatDelta{ToolCalls: []types.ToolCall{{
					Index: idx, ID: callID, Type: "function",
					Function: types.FunctionCall{Name: name, Arguments: argsStr},
				}}},
				FinishReason: nil,
			}},
		})
		t.writeChunk(types.ChatCompletionChunk{
			ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
			Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("tool_calls")}},
		})
		t.sentStopChunk = true
	}
}

func (t *chatStreamTranslator) handleReasoningDelta(kind string, data map[string]any) {
	deltaTxt, _ := data["delta"].(string)
	switch t.compat {
	case "o3":
		if kind == "response.reasoning_summary_text.delta" && t.pendingSummaryParagraph {
			t.writeChunk(types.ChatCompletionChunk{
				ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
				Choices: []types.ChatChunkChoice{{Index: 0,
					Delta: types.ChatDelta{Reasoning: types.ReasoningContent{
						Content: []types.ReasoningPart{{Type: "text", Text: "\n"}},
					}}, FinishReason: nil}},
			})
			t.pendingSummaryParagraph = false
		}
		t.writeChunk(types.ChatCompletionChunk{
			ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
			Choices: []types.ChatChunkChoice{{Index: 0,
				Delta: types.ChatDelta{Reasoning: types.ReasoningContent{
					Content: []types.ReasoningPart{{Type: "text", Text: deltaTxt}},
				}}, FinishReason: nil}},
		})
	case "think-tags":
		if !t.thinkOpen && !t.thinkClosed {
			t.writeChunk(t.makeDelta(types.ChatDelta{Content: "<think>"}))
			t.thinkOpen = true
		}
		if t.thinkOpen && !t.thinkClosed {
			if kind == "response.reasoning_summary_text.delta" && t.pendingSummaryParagraph {
				t.writeChunk(t.makeDelta(types.ChatDelta{Content: "\n"}))
				t.pendingSummaryParagraph = false
			}
			t.writeChunk(t.makeDelta(types.ChatDelta{Content: deltaTxt}))
		}
	default: // legacy
		if kind == "response.reasoning_summary_text.delta" {
			t.writeChunk(types.ChatCompletionChunk{
				ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
				Choices: []types.ChatChunkChoice{{Index: 0,
					Delta: types.ChatDelta{ReasoningSummary: deltaTxt, Reasoning: deltaTxt}, FinishReason: nil}},
			})
		} else {
			t.writeChunk(types.ChatCompletionChunk{
				ID: t.responseID, Object: "chat.completion.chunk", Created: 0, Model: t.model,
				Choices: []types.ChatChunkChoice{{Index: 0,
					Delta: types.ChatDelta{Reasoning: deltaTxt}, FinishReason: nil}},
			})
		}
	}
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
