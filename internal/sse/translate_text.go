package sse

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/n0madic/go-chatmock/internal/types"
)

// TranslateTextOptions holds options for text completion SSE translation.
type TranslateTextOptions struct {
	IncludeUsage bool
}

// TranslateText reads upstream SSE events and writes OpenAI text completion SSE chunks.
func TranslateText(w http.ResponseWriter, body io.ReadCloser, model string, created int64, opts TranslateTextOptions) {
	defer body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	reader := NewReader(body)
	responseID := "cmpl-stream"
	var upstreamUsage *types.Usage

	writeChunk := func(chunk any) {
		data, err := json.Marshal(chunk)
		if err != nil {
			slog.Error("failed to marshal SSE chunk", "error", err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
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
				ID: responseID, Object: "text_completion.chunk", Created: created, Model: model,
				Choices: []types.TextChunkChoice{{Index: 0, Text: delta, FinishReason: nil}},
			})

		case "response.output_text.done":
			writeChunk(types.TextCompletionChunk{
				ID: responseID, Object: "text_completion.chunk", Created: created, Model: model,
				Choices: []types.TextChunkChoice{{Index: 0, Text: "", FinishReason: types.StringPtr("stop")}},
			})

		case "response.completed":
			upstreamUsage = types.ExtractUsageFromEvent(evt.Data)
			if opts.IncludeUsage && upstreamUsage != nil {
				writeChunk(types.TextCompletionChunk{
					ID: responseID, Object: "text_completion.chunk", Created: created, Model: model,
					Choices: []types.TextChunkChoice{{Index: 0, Text: "", FinishReason: nil}},
					Usage:   upstreamUsage,
				})
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
	}

	// Stream ended without response.completed
	if !gotEvents {
		writeChunk(types.ErrorResponse{Error: types.ErrorDetail{Message: "upstream returned empty response"}})
	} else {
		writeChunk(types.TextCompletionChunk{
			ID: responseID, Object: "text_completion.chunk", Created: created, Model: model,
			Choices: []types.TextChunkChoice{{Index: 0, Text: "", FinishReason: types.StringPtr("stop")}},
		})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}
