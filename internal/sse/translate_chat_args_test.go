package sse

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestTranslateChatPreservesStringToolArguments(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_tool"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_shell","name":"Shell","arguments":"{\"command\":\"ls\"}"}}

data: {"type":"response.completed","response":{"id":"resp_tool"}}
`

	w := newFlusherRecorder()
	TranslateChat(w, io.NopCloser(strings.NewReader(stream)), "gpt-5.3-codex", time.Now().Unix(), TranslateChatOptions{})

	out := w.Body.String()
	if !strings.Contains(out, `"name":"Shell"`) {
		t.Fatalf("expected Shell tool call in output, got: %s", out)
	}
	if !strings.Contains(out, `"arguments":"{\"command\":\"ls\"}"`) {
		t.Fatalf("expected preserved command arguments in output, got: %s", out)
	}
	if strings.Contains(out, `"arguments":"{}"`) {
		t.Fatalf("unexpected empty tool arguments in output: %s", out)
	}
}

func TestTranslateChatUsesFunctionArgumentsDeltas(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_tool_delta"}}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_shell","call_id":"call_shell","name":"Shell","arguments":"{}"}}

data: {"type":"response.function_call_arguments.delta","item_id":"item_shell","delta":"{\"command\":\"pwd\"}"}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item_shell","call_id":"call_shell","name":"Shell","arguments":"{}"}}

data: {"type":"response.completed","response":{"id":"resp_tool_delta"}}
`

	w := newFlusherRecorder()
	TranslateChat(w, io.NopCloser(strings.NewReader(stream)), "gpt-5.3-codex", time.Now().Unix(), TranslateChatOptions{})

	out := w.Body.String()
	if !strings.Contains(out, `"name":"Shell"`) {
		t.Fatalf("expected Shell tool call in output, got: %s", out)
	}
	if !strings.Contains(out, `"arguments":"{\"command\":\"pwd\"}"`) {
		t.Fatalf("expected delta-built arguments in output, got: %s", out)
	}
}

func TestTranslateChatDoesNotDuplicateDeltasWhenIDEqualsCallID(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_tool_delta_same_id"}}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"call_shell","call_id":"call_shell","name":"Shell","arguments":"{}"}}

data: {"type":"response.function_call_arguments.delta","item_id":"call_shell","delta":"{\"command\":\"pwd\"}"}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"call_shell","call_id":"call_shell","name":"Shell","arguments":"{}"}}

data: {"type":"response.completed","response":{"id":"resp_tool_delta_same_id"}}
`

	w := newFlusherRecorder()
	TranslateChat(w, io.NopCloser(strings.NewReader(stream)), "gpt-5.3-codex", time.Now().Unix(), TranslateChatOptions{})

	out := w.Body.String()
	if strings.Contains(out, `{\"command\":\"pwd\"}{\"command\":\"pwd\"}`) {
		t.Fatalf("unexpected duplicated function_call delta in arguments: %s", out)
	}
	if !strings.Contains(out, `"arguments":"{\"command\":\"pwd\"}"`) {
		t.Fatalf("expected non-duplicated arguments in output, got: %s", out)
	}
}

func TestTranslateChatKeepsInvalidFunctionArgumentsRaw(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_tool_raw"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_shell","name":"Shell","arguments":"not-json"}}

data: {"type":"response.completed","response":{"id":"resp_tool_raw"}}
`

	w := newFlusherRecorder()
	TranslateChat(w, io.NopCloser(strings.NewReader(stream)), "gpt-5.3-codex", time.Now().Unix(), TranslateChatOptions{})

	out := w.Body.String()
	if !strings.Contains(out, `"arguments":"not-json"`) {
		t.Fatalf("expected raw invalid args in output, got: %s", out)
	}
	if strings.Contains(out, `\"query\":\"not-json\"`) {
		t.Fatalf("unexpected query-wrapper for function args: %s", out)
	}
}

func TestTranslateChatCustomToolCallSSE(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_custom"}}

data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","id":"item_ct","call_id":"call_ct1","name":"ApplyPatch","input":""}}

data: {"type":"response.function_call_arguments.delta","item_id":"item_ct","delta":"patch content"}

data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"item_ct","call_id":"call_ct1","name":"ApplyPatch","input":"patch content"}}

data: {"type":"response.completed","response":{"id":"resp_custom"}}
`

	w := newFlusherRecorder()
	TranslateChat(w, io.NopCloser(strings.NewReader(stream)), "gpt-5.3-codex", time.Now().Unix(), TranslateChatOptions{})

	out := w.Body.String()
	if !strings.Contains(out, `"name":"ApplyPatch"`) {
		t.Fatalf("expected ApplyPatch tool call in output, got: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected tool_calls finish_reason, got: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("expected [DONE] marker, got: %s", out)
	}
}

func TestTranslateChatSkipsCommentaryAndEndsWithToolCalls(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_commentary"}}

data: {"type":"response.output_item.added","item":{"type":"message","id":"msg_commentary","role":"assistant","phase":"commentary"}}

data: {"type":"response.output_text.delta","item_id":"msg_commentary","delta":"<think>internal plan</think> assistant to=functions.Shell {\"command\":\"ls\"}"}

data: {"type":"response.output_text.done","item_id":"msg_commentary"}

data: {"type":"response.output_item.done","item":{"type":"message","id":"msg_commentary","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"internal"}]}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_shell","name":"Shell","arguments":"{\"command\":\"ls\"}"}}

data: {"type":"response.completed","response":{"id":"resp_commentary"}}
`

	w := newFlusherRecorder()
	TranslateChat(w, io.NopCloser(strings.NewReader(stream)), "gpt-5.3-codex", time.Now().Unix(), TranslateChatOptions{})

	out := w.Body.String()
	if strings.Contains(out, "functions.Shell") || strings.Contains(out, "internal plan") {
		t.Fatalf("unexpected commentary leaked into chat output: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected tool_calls finish_reason, got: %s", out)
	}
	if strings.Contains(out, `"finish_reason":"stop"`) {
		t.Fatalf("unexpected stop finish_reason for tool-call turn: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("expected [DONE] marker, got: %s", out)
	}
}
