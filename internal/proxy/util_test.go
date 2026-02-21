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
