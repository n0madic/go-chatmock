package codec

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/n0madic/go-chatmock/internal/stream"
	"github.com/n0madic/go-chatmock/internal/types"
)

// ResponsesEncoder encodes responses in OpenAI Responses API format.
type ResponsesEncoder struct{}

func (e *ResponsesEncoder) WriteStreamHeaders(w http.ResponseWriter, statusCode int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(statusCode)
}

func (e *ResponsesEncoder) StreamTranslator(w http.ResponseWriter, model string, opts StreamOpts) Translator {
	return &responsesStreamTranslator{w: w}
}

func (e *ResponsesEncoder) WriteCollected(w http.ResponseWriter, statusCode int, resp *CollectedResponse, model string) {
	if resp.ErrorMessage != "" {
		WriteOpenAIError(w, http.StatusBadGateway, resp.ErrorMessage)
		return
	}

	// If we have a raw response from upstream, pass it through with model patched.
	if resp.RawResponse != nil {
		resp.RawResponse["model"] = model
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(resp.RawResponse)
		return
	}

	// Fallback: assemble from collected output items.
	result := types.ResponsesResponse{
		ID:        resp.ResponseID,
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Model:     model,
		Output:    resp.OutputItems,
		Status:    "completed",
	}
	WriteJSON(w, statusCode, result)
}

func (e *ResponsesEncoder) WriteError(w http.ResponseWriter, statusCode int, message string) {
	WriteOpenAIError(w, statusCode, message)
}

// responsesStreamTranslator is a near-passthrough: upstream already speaks
// Responses API SSE, so events are forwarded as-is with [DONE] appended.
type responsesStreamTranslator struct {
	w http.ResponseWriter
}

func (t *responsesStreamTranslator) Translate(reader *stream.Reader) {
	flusher, ok := t.w.(http.Flusher)
	if !ok {
		return
	}

	gotEvents := false
	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}
		gotEvents = true

		if evt.Type != "" {
			fmt.Fprintf(t.w, "event: %s\n", evt.Type)
		}
		fmt.Fprintf(t.w, "data: %s\n\n", evt.Raw)
		flusher.Flush()

		if evt.Type == "response.completed" || evt.Type == "response.failed" {
			fmt.Fprint(t.w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
	}

	if !gotEvents {
		fmt.Fprint(t.w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"upstream returned empty response\"}}}\n\n")
		flusher.Flush()
	}
	fmt.Fprint(t.w, "data: [DONE]\n\n")
	flusher.Flush()
}
