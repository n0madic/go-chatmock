package sse

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type flusherRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flusherRecorder) Flush() {
	f.flushed = true
}

func newFlusherRecorder() *flusherRecorder {
	return &flusherRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func TestTranslateResponsesPassthrough(t *testing.T) {
	stream := `data: {"type":"response.output_text.delta","delta":"Hello"}

data: {"type":"response.output_text.done","text":"Hello"}

data: {"type":"response.completed","response":{"id":"resp_123","usage":{"input_tokens":5,"output_tokens":1}}}

data: [DONE]

`
	w := newFlusherRecorder()
	body := io.NopCloser(strings.NewReader(stream))
	TranslateResponses(w, body)

	result := w.Body.String()

	// Each event should be re-emitted
	if !strings.Contains(result, `"type":"response.output_text.delta"`) {
		t.Errorf("expected output_text.delta event in output, got: %s", result)
	}
	if !strings.Contains(result, `"type":"response.completed"`) {
		t.Errorf("expected response.completed event in output, got: %s", result)
	}
	if !strings.Contains(result, "data: [DONE]") {
		t.Errorf("expected [DONE] in output, got: %s", result)
	}
}

func TestTranslateResponsesFailedEmitsDone(t *testing.T) {
	stream := `data: {"type":"response.failed","response":{"error":{"message":"something went wrong"}}}

data: [DONE]

`
	w := newFlusherRecorder()
	body := io.NopCloser(strings.NewReader(stream))
	TranslateResponses(w, body)

	result := w.Body.String()

	if !strings.Contains(result, `"type":"response.failed"`) {
		t.Errorf("expected response.failed event in output, got: %s", result)
	}
	if !strings.Contains(result, "data: [DONE]") {
		t.Errorf("expected [DONE] in output, got: %s", result)
	}
}

func TestTranslateResponsesEmptyStream(t *testing.T) {
	stream := `data: [DONE]

`
	w := newFlusherRecorder()
	body := io.NopCloser(strings.NewReader(stream))
	TranslateResponses(w, body)

	result := w.Body.String()
	if !strings.Contains(result, "data: [DONE]") {
		t.Errorf("expected [DONE] even for empty stream, got: %s", result)
	}
}

func TestTranslateResponsesIntermediateEvents(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_abc"}}

data: {"type":"response.output_item.added","item":{"type":"message"}}

data: {"type":"response.output_text.delta","delta":"Hi"}

data: {"type":"response.output_text.done","text":"Hi"}

data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}}

data: {"type":"response.completed","response":{"id":"resp_abc","usage":{"input_tokens":2,"output_tokens":1}}}

data: [DONE]

`
	w := newFlusherRecorder()
	body := io.NopCloser(strings.NewReader(stream))
	TranslateResponses(w, body)

	result := w.Body.String()

	// All events before completed should be in output
	for _, expected := range []string{
		`"type":"response.created"`,
		`"type":"response.output_item.added"`,
		`"type":"response.output_text.delta"`,
		`"type":"response.output_item.done"`,
		`"type":"response.completed"`,
		"data: [DONE]",
	} {
		if !strings.Contains(result, expected) {
			t.Errorf("expected %q in output, got: %s", expected, result)
		}
	}
}
