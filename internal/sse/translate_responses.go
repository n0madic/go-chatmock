package sse

import (
	"fmt"
	"io"
	"net/http"
)

// TranslateResponses reads upstream SSE events and re-emits them as-is to the response writer.
// This is a near-passthrough since the upstream already speaks Responses API format.
// It emits data: [DONE] after response.completed or response.failed.
func TranslateResponses(w http.ResponseWriter, body io.ReadCloser) {
	defer body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	reader := NewReader(body)
	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		fmt.Fprintf(w, "data: %s\n\n", evt.Raw)
		flusher.Flush()

		if evt.Type == "response.completed" || evt.Type == "response.failed" {
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
	}

	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}
