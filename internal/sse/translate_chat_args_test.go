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
