package sse

import (
	"encoding/json"
	"fmt"
	"io"
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

// TranslateChat reads upstream SSE events and writes OpenAI chat completion SSE chunks to the response writer.
func TranslateChat(w http.ResponseWriter, body io.ReadCloser, model string, created int64, opts TranslateChatOptions) {
	defer body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	reader := NewReader(body)
	responseID := "chatcmpl-stream"
	compat := strings.ToLower(strings.TrimSpace(opts.ReasoningCompat))
	if compat == "" {
		compat = "think-tags"
	}

	thinkOpen := false
	thinkClosed := false
	sentStopChunk := false
	sawAnySummary := false
	pendingSummaryParagraph := false
	var upstreamUsage *types.Usage

	// Web search state — stays map[string]any (dynamic upstream parameters)
	wsState := map[string]map[string]any{}
	wsIndex := map[string]int{}
	wsNextIndex := 0

	writeChunk := func(chunk any) {
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	writeDone := func() {
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}

	makeDelta := func(delta types.ChatDelta) types.ChatCompletionChunk {
		return types.ChatCompletionChunk{
			ID:      responseID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []types.ChatChunkChoice{
				{Index: 0, Delta: delta, FinishReason: nil},
			},
		}
	}

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		kind := evt.Type

		// Track response ID
		if resp, ok := evt.Data["response"].(map[string]any); ok {
			if id, ok := resp["id"].(string); ok && id != "" {
				responseID = id
			}
		}

		// Web search events
		if strings.Contains(kind, "web_search_call") {
			callID, _ := evt.Data["item_id"].(string)
			if callID == "" {
				callID = "ws_call"
			}
			if _, ok := wsState[callID]; !ok {
				wsState[callID] = map[string]any{}
			}
			mergeWebSearchParams(wsState[callID], evt.Data)
			if item, ok := evt.Data["item"].(map[string]any); ok {
				mergeWebSearchParams(wsState[callID], item)
			}
			argsStr := serializeToolArgs(wsState[callID])
			if _, ok := wsIndex[callID]; !ok {
				wsIndex[callID] = wsNextIndex
				wsNextIndex++
			}
			idx := wsIndex[callID]
			writeChunk(types.ChatCompletionChunk{
				ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
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
				writeChunk(types.ChatCompletionChunk{
					ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
					Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("tool_calls")}},
				})
			}
		}

		switch kind {
		case "response.output_text.delta":
			delta, _ := evt.Data["delta"].(string)
			if compat == "think-tags" && thinkOpen && !thinkClosed {
				writeChunk(makeDelta(types.ChatDelta{Content: "</think>"}))
				thinkOpen = false
				thinkClosed = true
			}
			writeChunk(makeDelta(types.ChatDelta{Content: delta}))

		case "response.output_item.done":
			item, _ := evt.Data["item"].(map[string]any)
			itemType, _ := item["type"].(string)
			if itemType == "function_call" || itemType == "web_search_call" {
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
					if _, ok := wsState[callID]; !ok {
						wsState[callID] = map[string]any{}
					}
					for k, v := range argsMap {
						wsState[callID][k] = v
					}
				}
				effArgs := wsState[callID]
				if effArgs == nil {
					switch a := rawArgs.(type) {
					case map[string]any:
						effArgs = a
					default:
						effArgs = map[string]any{}
					}
				}
				argsStr := serializeToolArgs(effArgs)
				if _, ok := wsIndex[callID]; !ok {
					wsIndex[callID] = wsNextIndex
					wsNextIndex++
				}
				idx := wsIndex[callID]
				if callID != "" && name != "" {
					writeChunk(types.ChatCompletionChunk{
						ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
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
					writeChunk(types.ChatCompletionChunk{
						ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
						Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("tool_calls")}},
					})
				}
			}

		case "response.reasoning_summary_part.added":
			if compat == "think-tags" || compat == "o3" {
				if sawAnySummary {
					pendingSummaryParagraph = true
				} else {
					sawAnySummary = true
				}
			}

		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			deltaTxt, _ := evt.Data["delta"].(string)
			switch compat {
			case "o3":
				if kind == "response.reasoning_summary_text.delta" && pendingSummaryParagraph {
					writeChunk(types.ChatCompletionChunk{
						ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
						Choices: []types.ChatChunkChoice{{
							Index: 0,
							Delta: types.ChatDelta{Reasoning: types.ReasoningContent{
								Content: []types.ReasoningPart{{Type: "text", Text: "\n"}},
							}},
							FinishReason: nil,
						}},
					})
					pendingSummaryParagraph = false
				}
				writeChunk(types.ChatCompletionChunk{
					ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
					Choices: []types.ChatChunkChoice{{
						Index: 0,
						Delta: types.ChatDelta{Reasoning: types.ReasoningContent{
							Content: []types.ReasoningPart{{Type: "text", Text: deltaTxt}},
						}},
						FinishReason: nil,
					}},
				})

			case "think-tags":
				if !thinkOpen && !thinkClosed {
					writeChunk(makeDelta(types.ChatDelta{Content: "<think>"}))
					thinkOpen = true
				}
				if thinkOpen && !thinkClosed {
					if kind == "response.reasoning_summary_text.delta" && pendingSummaryParagraph {
						writeChunk(makeDelta(types.ChatDelta{Content: "\n"}))
						pendingSummaryParagraph = false
					}
					writeChunk(makeDelta(types.ChatDelta{Content: deltaTxt}))
				}

			default: // legacy
				if kind == "response.reasoning_summary_text.delta" {
					writeChunk(types.ChatCompletionChunk{
						ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
						Choices: []types.ChatChunkChoice{{
							Index:        0,
							Delta:        types.ChatDelta{ReasoningSummary: deltaTxt, Reasoning: deltaTxt},
							FinishReason: nil,
						}},
					})
				} else {
					writeChunk(types.ChatCompletionChunk{
						ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
						Choices: []types.ChatChunkChoice{{
							Index: 0, Delta: types.ChatDelta{Reasoning: deltaTxt}, FinishReason: nil,
						}},
					})
				}
			}

		case "response.output_text.done":
			writeChunk(types.ChatCompletionChunk{
				ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
				Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("stop")}},
			})
			sentStopChunk = true

		case "response.failed":
			errMsg := "response.failed"
			if resp, ok := evt.Data["response"].(map[string]any); ok {
				if e, ok := resp["error"].(map[string]any); ok {
					if m, ok := e["message"].(string); ok {
						errMsg = m
					}
				}
			}
			writeChunk(types.ErrorResponse{Error: types.ErrorDetail{Message: errMsg}})

		case "response.completed":
			upstreamUsage = extractUsage(evt.Data)
			if compat == "think-tags" && thinkOpen && !thinkClosed {
				writeChunk(makeDelta(types.ChatDelta{Content: "</think>"}))
				thinkOpen = false
				thinkClosed = true
			}
			if !sentStopChunk {
				writeChunk(types.ChatCompletionChunk{
					ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
					Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("stop")}},
				})
				sentStopChunk = true
			}
			if opts.IncludeUsage && upstreamUsage != nil {
				writeChunk(types.ChatCompletionChunk{
					ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
					Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: nil}},
					Usage:   upstreamUsage,
				})
			}
			writeDone()
			return
		}
	}

	// Stream ended without response.completed
	if compat == "think-tags" && thinkOpen && !thinkClosed {
		writeChunk(makeDelta(types.ChatDelta{Content: "</think>"}))
	}
	if !sentStopChunk {
		writeChunk(types.ChatCompletionChunk{
			ID: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []types.ChatChunkChoice{{Index: 0, Delta: types.ChatDelta{}, FinishReason: types.StringPtr("stop")}},
		})
	}
	writeDone()
}

func extractUsage(data map[string]any) *types.Usage {
	resp, _ := data["response"].(map[string]any)
	if resp == nil {
		return nil
	}
	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		return nil
	}
	pt := toInt(usage["input_tokens"])
	ct := toInt(usage["output_tokens"])
	tt := toInt(usage["total_tokens"])
	if tt == 0 {
		tt = pt + ct
	}
	return &types.Usage{
		PromptTokens:     pt,
		CompletionTokens: ct,
		TotalTokens:      tt,
	}
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
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
			for k, v := range params {
				dst[k] = v
			}
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
