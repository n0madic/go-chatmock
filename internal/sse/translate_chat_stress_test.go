package sse

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestTranslateChatStressInterleavedDeltasForMultipleToolCalls(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_multi"}}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_a","call_id":"call_a","name":"Search","arguments":"{}"}}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_b","call_id":"call_b","name":"Read","arguments":"{}"}}

data: {"type":"response.function_call_arguments.delta","item_id":"item_a","delta":"{\"query\":\"go"}

data: {"type":"response.function_call_arguments.delta","item_id":"item_b","delta":"{\"path\":\"README"}

data: {"type":"response.function_call_arguments.delta","item_id":"item_a","delta":" proxy\"}"}

data: {"type":"response.function_call_arguments.delta","item_id":"item_b","delta":".md\"}"}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item_b","call_id":"call_b","name":"Read","arguments":"{}"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item_a","call_id":"call_a","name":"Search","arguments":"{}"}}

data: {"type":"response.completed","response":{"id":"resp_multi"}}
`

	w := newFlusherRecorder()
	TranslateChat(w, io.NopCloser(strings.NewReader(stream)), "gpt-5", time.Now().Unix(), TranslateChatOptions{})

	out := w.Body.String()
	if !strings.Contains(out, `"id":"call_a"`) || !strings.Contains(out, `"id":"call_b"`) {
		t.Fatalf("expected both tool calls in output, got: %s", out)
	}
	if !strings.Contains(out, `"name":"Search"`) || !strings.Contains(out, `"arguments":"{\"query\":\"go proxy\"}"`) {
		t.Fatalf("missing Search arguments reconstructed from interleaved deltas: %s", out)
	}
	if !strings.Contains(out, `"name":"Read"`) || !strings.Contains(out, `"arguments":"{\"path\":\"README.md\"}"`) {
		t.Fatalf("missing Read arguments reconstructed from interleaved deltas: %s", out)
	}
}

func TestTranslateChatStressDeltaBeforeOutputItemAdded(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_out_of_order"}}

data: {"type":"response.function_call_arguments.delta","item_id":"item_shell","delta":"{\"command\":\"pwd\"}"}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_shell","call_id":"call_shell","name":"Shell","arguments":"{}"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item_shell","call_id":"call_shell","name":"Shell","arguments":"{}"}}

data: {"type":"response.completed","response":{"id":"resp_out_of_order"}}
`

	w := newFlusherRecorder()
	TranslateChat(w, io.NopCloser(strings.NewReader(stream)), "gpt-5", time.Now().Unix(), TranslateChatOptions{})

	out := w.Body.String()
	if !strings.Contains(out, `"name":"Shell"`) {
		t.Fatalf("expected Shell tool call in output, got: %s", out)
	}
	if !strings.Contains(out, `"arguments":"{\"command\":\"pwd\"}"`) {
		t.Fatalf("expected buffered arguments from pre-added delta, got: %s", out)
	}
}

func TestTranslateChatStressInterleavedTextAndToolCall(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_text_tool"}}

data: {"type":"response.output_text.delta","delta":"I will use a tool."}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_shell","call_id":"call_shell","name":"Shell","arguments":"{}"}}

data: {"type":"response.function_call_arguments.delta","item_id":"item_shell","delta":"{\"command\":\"ls -la\"}"}

data: {"type":"response.output_text.delta","delta":" Preparing call..."}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item_shell","call_id":"call_shell","name":"Shell","arguments":"{}"}}

data: {"type":"response.completed","response":{"id":"resp_text_tool"}}
`

	w := newFlusherRecorder()
	TranslateChat(w, io.NopCloser(strings.NewReader(stream)), "gpt-5", time.Now().Unix(), TranslateChatOptions{})

	out := w.Body.String()
	if !strings.Contains(out, "I will use a tool.") || !strings.Contains(out, "Preparing call...") {
		t.Fatalf("expected interleaved text deltas in output, got: %s", out)
	}
	if !strings.Contains(out, `"name":"Shell"`) || !strings.Contains(out, `"arguments":"{\"command\":\"ls -la\"}"`) {
		t.Fatalf("expected tool call reconstructed in mixed text/tool stream, got: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected tool_calls finish reason in mixed stream, got: %s", out)
	}
}

func TestTranslateChatStressInterleavedWebSearchAndFunctionCall(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_ws_fn"}}

data: {"type":"response.web_search_call.in_progress","item_id":"ws_1","query":"golang maps"}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_read","call_id":"call_read","name":"Read","arguments":"{}"}}

data: {"type":"response.function_call_arguments.delta","item_id":"item_read","delta":"{\"path\":\"README.md\"}"}

data: {"type":"response.web_search_call.completed","item_id":"ws_1","query":"golang maps"}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item_read","call_id":"call_read","name":"Read","arguments":"{}"}}

data: {"type":"response.completed","response":{"id":"resp_ws_fn"}}
`

	w := newFlusherRecorder()
	TranslateChat(w, io.NopCloser(strings.NewReader(stream)), "gpt-5", time.Now().Unix(), TranslateChatOptions{})

	out := w.Body.String()
	if !strings.Contains(out, `"name":"web_search"`) {
		t.Fatalf("expected web_search tool call in output, got: %s", out)
	}
	if !strings.Contains(out, `"name":"Read"`) || !strings.Contains(out, `"arguments":"{\"path\":\"README.md\"}"`) {
		t.Fatalf("expected function call alongside web search in output, got: %s", out)
	}
}
