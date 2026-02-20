package sse

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTranslateAnthropicTextStream(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_1"}}

data: {"type":"response.output_text.delta","delta":"Hello"}

data: {"type":"response.output_text.done"}

data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}}
`

	w := httptest.NewRecorder()
	TranslateAnthropic(w, io.NopCloser(strings.NewReader(stream)), "claude-sonnet-4")

	out := w.Body.String()
	if !strings.Contains(out, "event: message_start") {
		t.Fatalf("expected message_start event, got: %s", out)
	}
	if !strings.Contains(out, "\"type\":\"text_delta\"") {
		t.Fatalf("expected text_delta payload, got: %s", out)
	}
	if !strings.Contains(out, "\"stop_reason\":\"end_turn\"") {
		t.Fatalf("expected stop_reason=end_turn, got: %s", out)
	}
	if !strings.Contains(out, "event: message_stop") {
		t.Fatalf("expected message_stop event, got: %s", out)
	}
}

func TestTranslateAnthropicToolUseStream(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_tool"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"}}

data: {"type":"response.completed","response":{"id":"resp_tool","usage":{"input_tokens":11,"output_tokens":4,"total_tokens":15}}}
`

	w := httptest.NewRecorder()
	TranslateAnthropic(w, io.NopCloser(strings.NewReader(stream)), "claude-3-5-sonnet")

	out := w.Body.String()
	if !strings.Contains(out, "\"type\":\"tool_use\"") {
		t.Fatalf("expected tool_use content block, got: %s", out)
	}
	if !strings.Contains(out, "\"stop_reason\":\"tool_use\"") {
		t.Fatalf("expected stop_reason=tool_use, got: %s", out)
	}
}

func TestTranslateAnthropicToolUseWithObjectArguments(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_tool_obj"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_2","name":"Glob","arguments":{"pattern":"**/*.go"}}}

data: {"type":"response.completed","response":{"id":"resp_tool_obj","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}}}
`

	w := httptest.NewRecorder()
	TranslateAnthropic(w, io.NopCloser(strings.NewReader(stream)), "claude-sonnet-4")

	out := w.Body.String()
	if !strings.Contains(out, `"name":"Glob"`) {
		t.Fatalf("expected tool name in output, got: %s", out)
	}
	if !strings.Contains(out, `"type":"input_json_delta"`) {
		t.Fatalf("expected input_json_delta event for tool arguments, got: %s", out)
	}
	if !strings.Contains(out, `\"pattern\":\"**/*.go\"`) {
		t.Fatalf("expected object arguments JSON in partial_json, got: %s", out)
	}
}

func TestTranslateAnthropicToolUseWithDeltaArguments(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_delta"}}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_1","call_id":"call_delta","name":"Bash","arguments":""}}

data: {"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"{\"command\":\"ls"}

data: {"type":"response.function_call_arguments.delta","item_id":"item_1","delta":" -la\"}"}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item_1","call_id":"call_delta","name":"Bash"}}

data: {"type":"response.completed","response":{"id":"resp_delta","usage":{"input_tokens":9,"output_tokens":3,"total_tokens":12}}}
`

	w := httptest.NewRecorder()
	TranslateAnthropic(w, io.NopCloser(strings.NewReader(stream)), "claude-sonnet-4")

	out := w.Body.String()
	if !strings.Contains(out, `"name":"Bash"`) {
		t.Fatalf("expected tool name in output, got: %s", out)
	}
	if !strings.Contains(out, `"type":"input_json_delta"`) {
		t.Fatalf("expected input_json_delta event for tool arguments, got: %s", out)
	}
	if !strings.Contains(out, `\"command\":\"ls -la\"`) {
		t.Fatalf("expected merged delta arguments in partial_json, got: %s", out)
	}
}

func TestTranslateAnthropicDeltaOverridesPlaceholderArguments(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_delta_override"}}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_2","call_id":"call_glob","name":"Glob","arguments":"{}"}}

data: {"type":"response.function_call_arguments.delta","item_id":"item_2","delta":"{\"pattern\":\"**/*.go\"}"}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item_2","call_id":"call_glob","name":"Glob"}}

data: {"type":"response.completed","response":{"id":"resp_delta_override","usage":{"input_tokens":9,"output_tokens":3,"total_tokens":12}}}
`

	w := httptest.NewRecorder()
	TranslateAnthropic(w, io.NopCloser(strings.NewReader(stream)), "claude-sonnet-4")

	out := w.Body.String()
	if !strings.Contains(out, `"name":"Glob"`) {
		t.Fatalf("expected tool name in output, got: %s", out)
	}
	if !strings.Contains(out, `"type":"input_json_delta"`) {
		t.Fatalf("expected input_json_delta event for tool arguments, got: %s", out)
	}
	if !strings.Contains(out, `\"pattern\":\"**/*.go\"`) {
		t.Fatalf("expected delta arguments to override placeholder {}, got: %s", out)
	}
}

func TestTranslateAnthropicFailedStream(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_fail"}}

data: {"type":"response.failed","response":{"id":"resp_fail","error":{"message":"boom"}}}
`

	w := httptest.NewRecorder()
	TranslateAnthropic(w, io.NopCloser(strings.NewReader(stream)), "claude-opus-4-1")

	out := w.Body.String()
	if !strings.Contains(out, "event: error") {
		t.Fatalf("expected error event, got: %s", out)
	}
	if !strings.Contains(out, "\"message\":\"boom\"") {
		t.Fatalf("expected error message in output, got: %s", out)
	}
}
