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
	mu       sync.Mutex
	results  []queuedUpstreamResult
	calls    []*upstream.Request
	rawCalls []json.RawMessage
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

func (c *queuedUpstreamClient) DoRaw(_ context.Context, body []byte, _ string) (*upstream.Response, error) {
	// Reconstruct an upstream.Request from the raw JSON for test assertions.
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)

	req := &upstream.Request{
		Model:        stringFromAny(raw["model"]),
		Instructions: stringFromAny(raw["instructions"]),
	}
	if s, ok := raw["store"].(bool); ok {
		req.Store = &s
	}
	// Parse input items from raw JSON
	if inputRaw, ok := raw["input"]; ok {
		inputBytes, _ := json.Marshal(inputRaw)
		var items []types.ResponsesInputItem
		_ = json.Unmarshal(inputBytes, &items)
		req.InputItems = items
	}

	c.mu.Lock()
	c.rawCalls = append(c.rawCalls, json.RawMessage(body))
	c.mu.Unlock()

	return c.Do(context.Background(), req)
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

func newCompatTestServer(client *queuedUpstreamClient, cleanup ...func()) *Server {
	store := responsesstate.NewStore(5*time.Minute, 100)
	if len(cleanup) > 0 && cleanup[0] != nil {
		// Caller can pass a t.Cleanup for deferred store close.
	}
	s := &Server{
		Config: &config.ServerConfig{
			DebugModel: "gpt-5",
		},
		upstreamClient: client,
		responsesState: store,
	}
	return s
}

func newCompatTestServerT(t *testing.T, client *queuedUpstreamClient) *Server {
	s := newCompatTestServer(client)
	t.Cleanup(func() { s.responsesState.Close() })
	return s
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
	s := newCompatTestServerT(t, up)

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

data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Accepted input field"}]}}

data: {"type":"response.completed","response":{"id":"resp_chat_input","object":"response","model":"gpt-5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Accepted input field"}]}]}}
`,
			},
		},
	}
	s := newCompatTestServerT(t, up)

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

	// When the request uses the "input" field, the response format should be
	// Responses API (passthrough), not Chat Completions.
	var out types.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if out.ID != "resp_chat_input" {
		t.Fatalf("unexpected response ID: got %q want %q", out.ID, "resp_chat_input")
	}
	if len(out.Output) != 1 || out.Output[0].Content[0].Text != "Accepted input field" {
		t.Fatalf("unexpected responses output: %+v", out)
	}
	if len(up.calls) != 1 || len(up.calls[0].InputItems) != 1 {
		t.Fatalf("unexpected upstream calls: %+v", up.calls)
	}
	if got := up.calls[0].InputItems[0].Content[0].Text; got != "hello via input" {
		t.Fatalf("upstream input text: got %q want %q", got, "hello via input")
	}
}

func TestOpenAIClientCompatChatCompletionsAcceptsResponsesStyleTools(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_chat_tools"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"ReadFile","arguments":"{\"path\":\"README.md\"}"}}

data: {"type":"response.completed","response":{"id":"resp_chat_tools","object":"response","model":"gpt-5","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"ReadFile","arguments":"{\"path\":\"README.md\"}"}]}}
`,
			},
		},
	}
	s := newCompatTestServerT(t, up)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":false,
		"input":[
			{"role":"system","content":"system rules"},
			{"role":"user","content":"Read README"}
		],
		"tools":[
			{"type":"function","name":"ReadFile","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	// When the request uses the "input" field, the response format should be
	// Responses API (passthrough), not Chat Completions.
	var out types.ResponsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if out.ID != "resp_chat_tools" {
		t.Fatalf("unexpected response ID: got %q want %q", out.ID, "resp_chat_tools")
	}
	if len(out.Output) != 1 {
		t.Fatalf("expected one output item, got %+v", out.Output)
	}
	if out.Output[0].Type != "function_call" || out.Output[0].Name != "ReadFile" || !strings.Contains(out.Output[0].Arguments, `"path":"README.md"`) {
		t.Fatalf("unexpected function call in responses output: %+v", out.Output[0])
	}

	if len(up.calls) != 1 {
		t.Fatalf("upstream call count: got %d want %d", len(up.calls), 1)
	}
	call := up.calls[0]
	if got := call.Instructions; got != "system rules" {
		t.Fatalf("upstream instructions: got %q want %q", got, "system rules")
	}
	if len(call.InputItems) != 1 || call.InputItems[0].Role != "user" {
		t.Fatalf("unexpected upstream input items: %+v", call.InputItems)
	}
	if got := call.InputItems[0].Content[0].Text; got != "Read README" {
		t.Fatalf("upstream input text: got %q want %q", got, "Read README")
	}
	if len(call.Tools) != 1 {
		t.Fatalf("unexpected upstream tools count: got %d want %d", len(call.Tools), 1)
	}
	if tool := call.Tools[0]; tool.Type != "function" || tool.Name != "ReadFile" || tool.Parameters == nil || tool.Strict == nil || *tool.Strict {
		t.Fatalf("unexpected normalized upstream tool: %+v", tool)
	}
}

func TestOpenAIClientCompatChatCompletionsAcceptsAccumulatedToolLoopInput(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_chat_loop"}}

data: {"type":"response.output_text.delta","delta":"Loop continued"}

data: {"type":"response.completed","response":{"id":"resp_chat_loop"}}
`,
			},
		},
	}
	s := newCompatTestServerT(t, up)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":false,
		"input":[
			{"role":"user","content":"Summarize project"},
			{"role":"assistant","content":"I will inspect files"},
			{"type":"function_call","call_id":"call_1","name":"ReadFile","arguments":"{\"path\":\"README.md\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"# README"},
			{"role":"user","content":"continue"}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	if len(up.calls) != 1 {
		t.Fatalf("upstream call count: got %d want %d", len(up.calls), 1)
	}
	items := up.calls[0].InputItems
	if len(items) != 5 {
		t.Fatalf("upstream input length: got %d want %d items=%+v", len(items), 5, items)
	}
	if items[0].Type != "message" || items[0].Role != "user" {
		t.Fatalf("unexpected first input item: %+v", items[0])
	}
	if items[1].Type != "message" || items[1].Role != "assistant" {
		t.Fatalf("unexpected second input item: %+v", items[1])
	}
	if items[2].Type != "function_call" || items[2].CallID != "call_1" || items[2].Name != "ReadFile" {
		t.Fatalf("unexpected function_call item: %+v", items[2])
	}
	if items[3].Type != "function_call_output" || items[3].CallID != "call_1" || items[3].Output != "# README" {
		t.Fatalf("unexpected function_call_output item: %+v", items[3])
	}
	if items[4].Type != "message" || items[4].Role != "user" || items[4].Content[0].Text != "continue" {
		t.Fatalf("unexpected final user item: %+v", items[4])
	}
}

func TestOpenAIClientCompatResponsesNonStream(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_1","created_at":1730000000}}

data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}}

data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1730000000,"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}
`,
			},
		},
	}
	s := newCompatTestServerT(t, up)

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

func TestOpenAIClientCompatResponsesStringInputNormalizedToArray(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_str"}}

data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}}

data: {"type":"response.completed","response":{"id":"resp_str","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}]}}
`,
			},
		},
	}
	s := newCompatTestServerT(t, up)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"input":"Hello string input",
		"stream":false
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleResponses(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	// The passthrough path must normalize string input to array before sending upstream.
	if len(up.rawCalls) != 1 {
		t.Fatalf("expected 1 raw call, got %d", len(up.rawCalls))
	}
	var rawBody map[string]any
	if err := json.Unmarshal(up.rawCalls[0], &rawBody); err != nil {
		t.Fatalf("unmarshal raw call: %v", err)
	}
	inputRaw, ok := rawBody["input"]
	if !ok {
		t.Fatal("expected input field in upstream request")
	}
	// Input must be an array, not a string.
	if _, isString := inputRaw.(string); isString {
		t.Fatal("upstream input should be array, but got string")
	}
	inputArr, ok := inputRaw.([]any)
	if !ok || len(inputArr) == 0 {
		t.Fatalf("upstream input should be non-empty array, got %T: %v", inputRaw, inputRaw)
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
	s := newCompatTestServerT(t, up)

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
	s := newCompatTestServerT(t, up)

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
	s := newCompatTestServerT(t, up)

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

func TestOpenAIClientCompatChatToolLoopAutoByCursorConversationIDMetadata(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_cursor_1"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}

data: {"type":"response.completed","response":{"id":"resp_cursor_1"}}
`,
			},
			{
				body: `data: {"type":"response.created","response":{"id":"resp_cursor_2"}}

data: {"type":"response.output_text.delta","delta":"21C"}

data: {"type":"response.completed","response":{"id":"resp_cursor_2"}}
`,
			},
		},
	}
	s := newCompatTestServerT(t, up)
	const convID = "cursor-conv-meta-1"

	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"Weather in Paris?"}],
		"metadata":{"cursorConversationId":"`+convID+`"}
	}`))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	s.handleChatCompletions(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first status: got %d want %d body=%s", w1.Code, http.StatusOK, w1.Body.String())
	}
	if body := w1.Body.String(); !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected [DONE] in first stream body, got %s", body)
	}
	if latest, ok := s.responsesState.GetConversationLatest(convID); !ok || latest != "resp_cursor_1" {
		t.Fatalf("unexpected latest response after first request: ok=%v id=%q", ok, latest)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":false,
		"input":[
			{"type":"function_call_output","call_id":"call_1","output":"{\"temp_c\":21}"},
			{"role":"user","content":"continue"}
		],
		"metadata":{"cursorConversationId":"`+convID+`"}
	}`))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	s.handleChatCompletions(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second status: got %d want %d body=%s", w2.Code, http.StatusOK, w2.Body.String())
	}

	if len(up.calls) != 2 {
		t.Fatalf("upstream call count: got %d want %d", len(up.calls), 2)
	}
	secondInput := up.calls[1].InputItems
	if len(secondInput) != 3 {
		t.Fatalf("expected restored tool loop in second input, got %+v", secondInput)
	}
	if secondInput[0].Type != "function_call" || secondInput[0].CallID != "call_1" || secondInput[0].Name != "get_weather" {
		t.Fatalf("expected injected function_call first, got %+v", secondInput[0])
	}
	if secondInput[1].Type != "function_call_output" || secondInput[1].CallID != "call_1" {
		t.Fatalf("expected function_call_output second, got %+v", secondInput[1])
	}
	if secondInput[2].Type != "message" || secondInput[2].Role != "user" || secondInput[2].Content[0].Text != "continue" {
		t.Fatalf("expected user continuation third, got %+v", secondInput[2])
	}
	if latest, ok := s.responsesState.GetConversationLatest(convID); !ok || latest != "resp_cursor_2" {
		t.Fatalf("unexpected latest response after second request: ok=%v id=%q", ok, latest)
	}
}

func TestOpenAIClientCompatChatToolLoopAutoByCursorConversationIDUsesLatest(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_cursor_latest_1"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"ReadFile","arguments":"{\"path\":\"README.md\"}"}}

data: {"type":"response.completed","response":{"id":"resp_cursor_latest_1"}}
`,
			},
			{
				body: `data: {"type":"response.created","response":{"id":"resp_cursor_latest_2"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_2","name":"Shell","arguments":"{\"command\":\"pwd\"}"}}

data: {"type":"response.completed","response":{"id":"resp_cursor_latest_2"}}
`,
			},
			{
				body: `data: {"type":"response.created","response":{"id":"resp_cursor_latest_3"}}

data: {"type":"response.output_text.delta","delta":"done"}

data: {"type":"response.completed","response":{"id":"resp_cursor_latest_3"}}
`,
			},
		},
	}
	s := newCompatTestServerT(t, up)
	const convID = "cursor-conv-top-level-1"

	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":false,
		"input":[{"role":"user","content":"step 1"}],
		"cursorConversationId":"`+convID+`"
	}`))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	s.handleChatCompletions(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first status: got %d want %d body=%s", w1.Code, http.StatusOK, w1.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":false,
		"input":[{"type":"function_call_output","call_id":"call_1","output":"# README"}],
		"cursorConversationId":"`+convID+`"
	}`))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	s.handleChatCompletions(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second status: got %d want %d body=%s", w2.Code, http.StatusOK, w2.Body.String())
	}

	req3 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":false,
		"input":[{"type":"function_call_output","call_id":"call_2","output":"/workspace"}],
		"cursorConversationId":"`+convID+`"
	}`))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	s.handleChatCompletions(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("third status: got %d want %d body=%s", w3.Code, http.StatusOK, w3.Body.String())
	}

	if len(up.calls) != 3 {
		t.Fatalf("upstream call count: got %d want %d", len(up.calls), 3)
	}

	secondInput := up.calls[1].InputItems
	if len(secondInput) != 2 {
		t.Fatalf("expected injected call + output in second input, got %+v", secondInput)
	}
	if secondInput[0].Type != "function_call" || secondInput[0].CallID != "call_1" || secondInput[0].Name != "ReadFile" {
		t.Fatalf("expected call_1 restoration in second input, got %+v", secondInput[0])
	}
	if secondInput[1].Type != "function_call_output" || secondInput[1].CallID != "call_1" {
		t.Fatalf("expected call_1 output in second input, got %+v", secondInput[1])
	}

	thirdInput := up.calls[2].InputItems
	if len(thirdInput) != 2 {
		t.Fatalf("expected injected call + output in third input, got %+v", thirdInput)
	}
	if thirdInput[0].Type != "function_call" || thirdInput[0].CallID != "call_2" || thirdInput[0].Name != "Shell" {
		t.Fatalf("expected call_2 restoration from latest response, got %+v", thirdInput[0])
	}
	if thirdInput[1].Type != "function_call_output" || thirdInput[1].CallID != "call_2" {
		t.Fatalf("expected call_2 output in third input, got %+v", thirdInput[1])
	}
	if latest, ok := s.responsesState.GetConversationLatest(convID); !ok || latest != "resp_cursor_latest_3" {
		t.Fatalf("unexpected latest response after third request: ok=%v id=%q", ok, latest)
	}
}
