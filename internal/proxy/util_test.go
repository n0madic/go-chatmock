package proxy

import (
	"io"
	"strings"
	"testing"
)

func TestCollectTextResponseFromSSECollectsFields(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_1"}}
data: {"type":"response.output_text.delta","delta":"Hel"}
data: {"type":"response.output_text.delta","delta":"lo"}
data: {"type":"response.reasoning_summary_text.delta","delta":"summary"}
data: {"type":"response.reasoning_text.delta","delta":"trace"}
data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"}}
data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}}
`

	got := collectTextResponseFromSSE(io.NopCloser(strings.NewReader(stream)), collectTextResponseOptions{
		InitialResponseID: "fallback",
		CollectUsage:      true,
		CollectReasoning:  true,
		CollectToolCalls:  true,
		StopOnFailed:      true,
	})

	if got.ResponseID != "resp_1" {
		t.Fatalf("expected response id resp_1, got %q", got.ResponseID)
	}
	if got.FullText != "Hello" {
		t.Fatalf("expected text Hello, got %q", got.FullText)
	}
	if got.ReasoningSummary != "summary" {
		t.Fatalf("expected reasoning summary, got %q", got.ReasoningSummary)
	}
	if got.ReasoningFull != "trace" {
		t.Fatalf("expected reasoning full, got %q", got.ReasoningFull)
	}
	if got.ErrorMessage != "" {
		t.Fatalf("unexpected error message: %q", got.ErrorMessage)
	}
	if got.Usage == nil || got.Usage.TotalTokens != 8 {
		t.Fatalf("expected usage total_tokens=8, got %+v", got.Usage)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(got.ToolCalls))
	}
	if got.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("expected tool name read_file, got %q", got.ToolCalls[0].Function.Name)
	}
}

func TestOutputItemArgumentsString(t *testing.T) {
	tests := []struct {
		name string
		item map[string]any
		want string
	}{
		{"arguments_string", map[string]any{"arguments": `{"a":1}`}, `{"a":1}`},
		{"input_string", map[string]any{"input": "patch text"}, "patch text"},
		{"input_object", map[string]any{"input": map[string]any{"key": "val"}}, `{"key":"val"}`},
		{"parameters_string", map[string]any{"parameters": `{"b":2}`}, `{"b":2}`},
		{"arguments_over_input", map[string]any{"arguments": `{"a":1}`, "input": "fallback"}, `{"a":1}`},
		{"empty", map[string]any{}, ""},
		{"nil_values", map[string]any{"arguments": nil, "input": nil}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := outputItemArgumentsString(tt.item)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFunctionToolCallFromOutputItemCustomToolCall(t *testing.T) {
	item := map[string]any{
		"type":    "custom_tool_call",
		"call_id": "call_ct1",
		"name":    "ApplyPatch",
		"input":   "patch content",
	}
	tc, ok := functionToolCallFromOutputItem(item)
	if !ok {
		t.Fatal("expected ok=true for custom_tool_call")
	}
	if tc.ID != "call_ct1" {
		t.Fatalf("expected call_id call_ct1, got %q", tc.ID)
	}
	if tc.Function.Name != "ApplyPatch" {
		t.Fatalf("expected name ApplyPatch, got %q", tc.Function.Name)
	}
	if tc.Function.Arguments != "patch content" {
		t.Fatalf("expected arguments 'patch content', got %q", tc.Function.Arguments)
	}
}

func TestFunctionToolCallFromOutputItemCustomToolCallObjectInput(t *testing.T) {
	item := map[string]any{
		"type":    "custom_tool_call",
		"call_id": "call_ct2",
		"name":    "Tool",
		"input":   map[string]any{"key": "value"},
	}
	tc, ok := functionToolCallFromOutputItem(item)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if tc.Function.Arguments != `{"key":"value"}` {
		t.Fatalf("expected JSON arguments, got %q", tc.Function.Arguments)
	}
}

func TestCollectTextResponseFromSSECustomToolCall(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_ct"}}
data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"call_ct1","name":"ApplyPatch","input":"patch"}}
data: {"type":"response.completed","response":{"id":"resp_ct","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}
`
	got := collectTextResponseFromSSE(io.NopCloser(strings.NewReader(stream)), collectTextResponseOptions{
		InitialResponseID: "fallback",
		CollectUsage:      true,
		CollectToolCalls:  true,
	})

	if len(got.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(got.ToolCalls))
	}
	if got.ToolCalls[0].Function.Name != "ApplyPatch" {
		t.Fatalf("expected name ApplyPatch, got %q", got.ToolCalls[0].Function.Name)
	}
	if got.ToolCalls[0].Function.Arguments != "patch" {
		t.Fatalf("expected arguments 'patch', got %q", got.ToolCalls[0].Function.Arguments)
	}
	if len(got.OutputItems) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(got.OutputItems))
	}
	if got.OutputItems[0].Type != "custom_tool_call" {
		t.Fatalf("expected output item type custom_tool_call, got %q", got.OutputItems[0].Type)
	}
}

func TestCollectTextResponseFromSSEStopOnFailed(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_fail"}}
data: {"type":"response.output_text.delta","delta":"a"}
data: {"type":"response.failed","response":{"error":{"message":"boom"}}}
data: {"type":"response.output_text.delta","delta":"b"}
data: {"type":"response.completed","response":{"id":"resp_fail"}}
`

	stopOnFailed := collectTextResponseFromSSE(io.NopCloser(strings.NewReader(stream)), collectTextResponseOptions{
		InitialResponseID: "fallback",
		StopOnFailed:      true,
	})
	if stopOnFailed.FullText != "a" {
		t.Fatalf("expected text a when stopping on failed, got %q", stopOnFailed.FullText)
	}
	if stopOnFailed.ErrorMessage != "boom" {
		t.Fatalf("expected error message boom, got %q", stopOnFailed.ErrorMessage)
	}

	keepReading := collectTextResponseFromSSE(io.NopCloser(strings.NewReader(stream)), collectTextResponseOptions{
		InitialResponseID: "fallback",
		StopOnFailed:      false,
	})
	if keepReading.FullText != "ab" {
		t.Fatalf("expected text ab when not stopping on failed, got %q", keepReading.FullText)
	}
	if keepReading.ErrorMessage != "boom" {
		t.Fatalf("expected error message boom, got %q", keepReading.ErrorMessage)
	}
}
