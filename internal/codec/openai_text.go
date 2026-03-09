package codec

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/n0madic/go-chatmock/internal/stream"
	"github.com/n0madic/go-chatmock/internal/types"
)

// TextEncoder encodes responses in OpenAI text completions format.
type TextEncoder struct{}

func (e *TextEncoder) WriteStreamHeaders(w http.ResponseWriter, statusCode int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(statusCode)
}

func (e *TextEncoder) StreamTranslator(w http.ResponseWriter, model string, opts StreamOpts) Translator {
	return &textStreamTranslator{w: w, model: model, opts: opts}
}

func (e *TextEncoder) WriteCollected(w http.ResponseWriter, statusCode int, resp *CollectedResponse, model string) {
	if resp.ErrorMessage != "" {
		WriteOpenAIError(w, http.StatusBadGateway, resp.ErrorMessage)
		return
	}
	completion := types.TextCompletionResponse{
		ID:     resp.ResponseID,
		Object: "text_completion",
		Model:  model,
		Choices: []types.TextChoice{
			{Index: 0, Text: resp.FullText, FinishReason: types.StringPtr("stop"), Logprobs: nil},
		},
		Usage: resp.Usage,
	}
	WriteJSON(w, statusCode, completion)
}

func (e *TextEncoder) WriteError(w http.ResponseWriter, statusCode int, message string) {
	WriteOpenAIError(w, statusCode, message)
}

// textStreamTranslator translates upstream SSE into OpenAI text completion chunks.
type textStreamTranslator struct {
	w     http.ResponseWriter
	model string
	opts  StreamOpts
}

func (t *textStreamTranslator) Translate(reader *stream.Reader) {
	flusher, ok := t.w.(http.Flusher)
	if !ok {
		return
	}

	responseID := "cmpl-stream"
	var upstreamUsage *types.Usage

	writeChunk := func(chunk any) {
		data, err := json.Marshal(chunk)
		if err != nil {
			slog.Error("failed to marshal SSE chunk", "error", err)
			return
		}
		fmt.Fprintf(t.w, "data: %s\n\n", data)
		flusher.Flush()
	}

	gotEvents := false
	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}
		gotEvents = true

		if resp, ok := evt.Data["response"].(map[string]any); ok {
			if id, ok := resp["id"].(string); ok && id != "" {
				responseID = id
			}
		}

		switch evt.Type {
		case "response.output_text.delta":
			delta, _ := evt.Data["delta"].(string)
			writeChunk(types.TextCompletionChunk{
				ID: responseID, Object: "text_completion.chunk", Created: 0, Model: t.model,
				Choices: []types.TextChunkChoice{{Index: 0, Text: delta, FinishReason: nil}},
			})

		case "response.output_text.done":
			writeChunk(types.TextCompletionChunk{
				ID: responseID, Object: "text_completion.chunk", Created: 0, Model: t.model,
				Choices: []types.TextChunkChoice{{Index: 0, Text: "", FinishReason: types.StringPtr("stop")}},
			})

		case "response.completed":
			upstreamUsage = stream.ExtractUsageFromEvent(evt.Data)
			if t.opts.IncludeUsage && upstreamUsage != nil {
				writeChunk(types.TextCompletionChunk{
					ID: responseID, Object: "text_completion.chunk", Created: 0, Model: t.model,
					Choices: []types.TextChunkChoice{{Index: 0, Text: "", FinishReason: nil}},
					Usage:   upstreamUsage,
				})
			}
			fmt.Fprint(t.w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
	}

	// Stream ended without response.completed
	if !gotEvents {
		writeChunk(types.ErrorResponse{Error: types.ErrorDetail{Message: "upstream returned empty response"}})
	} else {
		writeChunk(types.TextCompletionChunk{
			ID: responseID, Object: "text_completion.chunk", Created: 0, Model: t.model,
			Choices: []types.TextChunkChoice{{Index: 0, Text: "", FinishReason: types.StringPtr("stop")}},
		})
	}
	fmt.Fprint(t.w, "data: [DONE]\n\n")
	flusher.Flush()
}
