package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n0madic/go-chatmock/internal/config"
	responsesstate "github.com/n0madic/go-chatmock/internal/responses-state"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

type queuedUpstreamResult struct {
	status  int
	body    string
	headers http.Header
	err     error
}

type queuedUpstreamClient struct {
	mu      sync.Mutex
	results []queuedUpstreamResult
	calls   []*upstream.Request
}

func (c *queuedUpstreamClient) Do(_ context.Context, req *upstream.Request) (*upstream.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.calls = append(c.calls, cloneUpstreamRequest(req))
	callIdx := len(c.calls) - 1
	if callIdx >= len(c.results) {
		return nil, errors.New("no queued upstream response")
	}

	result := c.results[callIdx]
	if result.err != nil {
		return nil, result.err
	}

	status := result.status
	if status == 0 {
		status = http.StatusOK
	}

	headers := result.headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}

	httpResp := &http.Response{
		StatusCode: status,
		Header:     headers.Clone(),
		Body:       io.NopCloser(strings.NewReader(result.body)),
	}
	return &upstream.Response{
		StatusCode: status,
		Body:       httpResp,
		Headers:    headers,
	}, nil
}

func cloneUpstreamRequest(in *upstream.Request) *upstream.Request {
	if in == nil {
		return nil
	}

	out := *in
	out.InputItems = cloneResponsesInputItems(in.InputItems)
	if len(in.Tools) > 0 {
		out.Tools = make([]types.ResponsesTool, len(in.Tools))
		copy(out.Tools, in.Tools)
	}
	if len(in.Include) > 0 {
		out.Include = append([]string(nil), in.Include...)
	}
	if in.Store != nil {
		store := *in.Store
		out.Store = &store
	}
	return &out
}

func newCompatTestServer(client *queuedUpstreamClient) *Server {
	return &Server{
		Config: &config.ServerConfig{
			DebugModel: "gpt-5",
		},
		upstreamClient: client,
		responsesState: responsesstate.NewStore(5*time.Minute, 100),
	}
}

func TestOpenAIClientCompatChatCompletionsNonStream(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_chat_1"}}

data: {"type":"response.output_text.delta","delta":"Hello from assistant"}

data: {"type":"response.completed","response":{"id":"resp_chat_1","usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}}
`,
			},
		},
	}
	s := newCompatTestServer(up)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"messages":[{"role":"user","content":"Hello!"}],
		"temperature":0.2,
		"max_tokens":128,
		"stream":false
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	var out types.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if out.Object != "chat.completion" {
		t.Fatalf("object: got %q want %q", out.Object, "chat.completion")
	}
	if out.Model != "gpt-5" {
		t.Fatalf("model: got %q want %q", out.Model, "gpt-5")
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "Hello from assistant" {
		t.Fatalf("unexpected choices: %+v", out.Choices)
	}
	if out.Usage == nil || out.Usage.TotalTokens != 7 {
		t.Fatalf("unexpected usage: %+v", out.Usage)
	}

	if len(up.calls) != 1 {
		t.Fatalf("upstream call count: got %d want %d", len(up.calls), 1)
	}
	if len(up.calls[0].InputItems) != 1 || up.calls[0].InputItems[0].Role != "user" {
		t.Fatalf("unexpected upstream input: %+v", up.calls[0].InputItems)
	}
	if got := up.calls[0].InputItems[0].Content[0].Text; got != "Hello!" {
		t.Fatalf("upstream text: got %q want %q", got, "Hello!")
	}
}

func TestOpenAIClientCompatChatCompletionsAcceptsResponsesShape(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_chat_input"}}

data: {"type":"response.output_text.delta","delta":"Accepted input field"}

data: {"type":"response.completed","response":{"id":"resp_chat_input"}}
`,
			},
		},
	}
	s := newCompatTestServer(up)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"input":"hello via input",
		"stream":false
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	var out types.ChatCompletionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "Accepted input field" {
		t.Fatalf("unexpected chat response: %+v", out)
	}
	if len(up.calls) != 1 || len(up.calls[0].InputItems) != 1 {
		t.Fatalf("unexpected upstream calls: %+v", up.calls)
	}
	if got := up.calls[0].InputItems[0].Content[0].Text; got != "hello via input" {
		t.Fatalf("upstream input text: got %q want %q", got, "hello via input")
	}
}

func TestOpenAIClientCompatResponsesNonStream(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_1","created_at":1730000000}}

data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}}

data: {"type":"response.completed","response":{"id":"resp_1","created_at":1730000000,"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}
`,
			},
		},
	}
	s := newCompatTestServer(up)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"input":"Check compatibility",
		"instructions":"be short",
		"store":true,
		"stream":false
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleResponses(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	var out types.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if out.Object != "response" || out.ID != "resp_1" || out.Status != "completed" {
		t.Fatalf("unexpected response envelope: %+v", out)
	}
	if len(out.Output) != 1 || out.Output[0].Type != "message" {
		t.Fatalf("unexpected output items: %+v", out.Output)
	}
	if out.Usage == nil || out.Usage.TotalTokens != 7 {
		t.Fatalf("unexpected usage: %+v", out.Usage)
	}

	if len(up.calls) != 1 {
		t.Fatalf("upstream call count: got %d want %d", len(up.calls), 1)
	}
	if up.calls[0].Store == nil || *up.calls[0].Store {
		t.Fatalf("expected upstream store=false compatibility mode, got %+v", up.calls[0].Store)
	}
}

func TestOpenAIClientCompatResponsesStreaming(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_stream_1"}}

data: {"type":"response.output_text.delta","delta":"hi"}

data: {"type":"response.completed","response":{"id":"resp_stream_1"}}
`,
			},
		},
	}
	s := newCompatTestServer(up)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"input":"stream it",
		"stream":true
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleResponses(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type: got %q want %q", ct, "text/event-stream")
	}
	body := w.Body.String()
	if !strings.Contains(body, `data: {"type":"response.output_text.delta","delta":"hi"}`) {
		t.Fatalf("missing upstream delta event in stream body: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing [DONE] marker in stream body: %s", body)
	}
}

func TestOpenAIClientCompatChatStreamingToolCalls(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_tool_stream"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}

data: {"type":"response.completed","response":{"id":"resp_tool_stream"}}
`,
			},
		},
	}
	s := newCompatTestServer(up)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"Weather in Paris?"}],
		"tools":[
			{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type: got %q want %q", ct, "text/event-stream")
	}
	body := w.Body.String()
	if !strings.Contains(body, `"tool_calls":[`) {
		t.Fatalf("expected tool_calls in chat stream, got: %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected tool_calls finish_reason in chat stream, got: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing [DONE] marker in chat stream: %s", body)
	}
}

func TestOpenAIClientCompatResponsesToolLoopWithPreviousResponseID(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_loop_1"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}

data: {"type":"response.completed","response":{"id":"resp_loop_1"}}
`,
			},
			{
				body: `data: {"type":"response.created","response":{"id":"resp_loop_2"}}

data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"21C"}]}}

data: {"type":"response.completed","response":{"id":"resp_loop_2"}}
`,
			},
		},
	}
	s := newCompatTestServer(up)

	req1 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"input":"Weather in Paris?",
		"stream":false
	}`))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	s.handleResponses(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first status: got %d want %d body=%s", w1.Code, http.StatusOK, w1.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"previous_response_id":"resp_loop_1",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"{\"temp_c\":21}"}],
		"stream":false
	}`))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	s.handleResponses(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second status: got %d want %d body=%s", w2.Code, http.StatusOK, w2.Body.String())
	}

	if len(up.calls) != 2 {
		t.Fatalf("upstream call count: got %d want %d", len(up.calls), 2)
	}

	secondInput := up.calls[1].InputItems
	if len(secondInput) < 3 {
		t.Fatalf("expected restored tool-loop context in second input, got %+v", secondInput)
	}
	if secondInput[0].Type != "message" || secondInput[0].Role != "user" {
		t.Fatalf("expected previous user context as first item, got %+v", secondInput[0])
	}
	if secondInput[1].Type != "function_call" || secondInput[1].CallID != "call_1" {
		t.Fatalf("expected previous function_call as second item, got %+v", secondInput[1])
	}
	if secondInput[len(secondInput)-1].Type != "function_call_output" || secondInput[len(secondInput)-1].CallID != "call_1" {
		t.Fatalf("expected function_call_output in second request tail, got %+v", secondInput[len(secondInput)-1])
	}
}
