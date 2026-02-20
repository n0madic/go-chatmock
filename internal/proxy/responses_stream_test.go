package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n0madic/go-chatmock/internal/responses-state"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

func TestStreamResponsesWithStateCapturesToolCalls(t *testing.T) {
	s := &Server{responsesState: responsesstate.NewStore(5*time.Minute, 100)}

	stream := `data: {"type":"response.created","response":{"id":"resp_123"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"}}

data: {"type":"response.completed","response":{"id":"resp_123"}}
`
	resp := &upstream.Response{Body: &http.Response{Body: io.NopCloser(strings.NewReader(stream))}}
	w := httptest.NewRecorder()
	requestInput := []types.ResponsesInputItem{
		{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: "Read README"}}},
	}

	s.streamResponsesWithState(w, resp, requestInput, "stay in context")

	body := w.Body.String()
	if !strings.Contains(body, `"type":"response.output_item.done"`) {
		t.Fatalf("expected stream passthrough, got %q", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected [DONE], got %q", body)
	}

	calls, ok := s.responsesState.Get("resp_123")
	if !ok {
		t.Fatal("expected captured state for resp_123")
	}
	if len(calls) != 1 || calls[0].CallID != "call_1" || calls[0].Name != "read_file" {
		t.Fatalf("unexpected captured calls: %+v", calls)
	}

	context, ok := s.responsesState.GetContext("resp_123")
	if !ok {
		t.Fatal("expected captured context for resp_123")
	}
	if len(context) != 2 {
		t.Fatalf("expected request + function_call in context, got %d items", len(context))
	}
	if context[1].Type != "function_call" || context[1].CallID != "call_1" {
		t.Fatalf("unexpected context item: %+v", context[1])
	}
	instructions, ok := s.responsesState.GetInstructions("resp_123")
	if !ok || instructions != "stay in context" {
		t.Fatalf("unexpected stored instructions: ok=%v value=%q", ok, instructions)
	}
}

func TestCollectResponsesResponseCapturesToolCalls(t *testing.T) {
	s := &Server{responsesState: responsesstate.NewStore(5*time.Minute, 100)}

	stream := `data: {"type":"response.created","response":{"id":"resp_456","created_at":1730000000}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_2","name":"search_files","arguments":"{\"pattern\":\"TODO\"}"}}

data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}}

data: {"type":"response.completed","response":{"id":"resp_456","created_at":1730000000}}
`
	resp := &upstream.Response{
		StatusCode: 200,
		Body:       &http.Response{Body: io.NopCloser(strings.NewReader(stream))},
	}
	w := httptest.NewRecorder()
	requestInput := []types.ResponsesInputItem{
		{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: "Find TODO"}}},
	}

	s.collectResponsesResponse(w, resp, "gpt-5", requestInput, "stay in context")

	calls, ok := s.responsesState.Get("resp_456")
	if !ok {
		t.Fatal("expected captured state for resp_456")
	}
	if len(calls) != 1 || calls[0].CallID != "call_2" || calls[0].Name != "search_files" {
		t.Fatalf("unexpected captured calls: %+v", calls)
	}

	context, ok := s.responsesState.GetContext("resp_456")
	if !ok {
		t.Fatal("expected captured context for resp_456")
	}
	if len(context) != 3 {
		t.Fatalf("expected request + output items in context, got %d items", len(context))
	}
	if context[1].Type != "function_call" || context[1].CallID != "call_2" {
		t.Fatalf("unexpected function_call context item: %+v", context[1])
	}
	if context[2].Type != "message" || context[2].Role != "assistant" {
		t.Fatalf("unexpected assistant context item: %+v", context[2])
	}
	instructions, ok := s.responsesState.GetInstructions("resp_456")
	if !ok || instructions != "stay in context" {
		t.Fatalf("unexpected stored instructions: ok=%v value=%q", ok, instructions)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"id":"resp_456"`) {
		t.Fatalf("expected response id in JSON output, got %q", body)
	}
}
