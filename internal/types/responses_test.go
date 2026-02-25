package types

import (
	"encoding/json"
	"testing"
)

func TestParseInputString(t *testing.T) {
	req := &ResponsesRequest{
		Input: json.RawMessage(`"Hello, world!"`),
	}
	items, err := req.ParseInput()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Type != "message" {
		t.Errorf("expected type 'message', got %q", items[0].Type)
	}
	if items[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", items[0].Role)
	}
	if len(items[0].Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(items[0].Content))
	}
	if items[0].Content[0].Text != "Hello, world!" {
		t.Errorf("expected text 'Hello, world!', got %q", items[0].Content[0].Text)
	}
}

func TestParseInputArray(t *testing.T) {
	raw := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}]`
	req := &ResponsesRequest{
		Input: json.RawMessage(raw),
	}
	items, err := req.ParseInput()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", items[0].Role)
	}
	if len(items[0].Content) != 1 || items[0].Content[0].Text != "Hi" {
		t.Errorf("unexpected content: %+v", items[0].Content)
	}
}

func TestParseInputNil(t *testing.T) {
	req := &ResponsesRequest{}
	items, err := req.ParseInput()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items != nil {
		t.Errorf("expected nil items, got %v", items)
	}
}

func TestParseInputArrayStringContent(t *testing.T) {
	raw := `[
		{"role": "system", "content": "You are helpful."},
		{"role": "user", "content": "Hello!"}
	]`
	req := &ResponsesRequest{
		Input: json.RawMessage(raw),
	}
	items, err := req.ParseInput()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	// type should be defaulted to "message"
	for i, item := range items {
		if item.Type != "message" {
			t.Errorf("item[%d]: expected type 'message', got %q", i, item.Type)
		}
	}
	if items[0].Role != "system" {
		t.Errorf("expected role 'system', got %q", items[0].Role)
	}
	if len(items[0].Content) != 1 || items[0].Content[0].Text != "You are helpful." {
		t.Errorf("unexpected content for system item: %+v", items[0].Content)
	}
	if items[0].Content[0].Type != "input_text" {
		t.Errorf("expected content type 'input_text', got %q", items[0].Content[0].Type)
	}
	if items[1].Role != "user" {
		t.Errorf("expected role 'user', got %q", items[1].Role)
	}
	if len(items[1].Content) != 1 || items[1].Content[0].Text != "Hello!" {
		t.Errorf("unexpected content for user item: %+v", items[1].Content)
	}
}

func TestParseInputAssistantStringContent(t *testing.T) {
	raw := `[{"role": "assistant", "content": "I can help."}]`
	req := &ResponsesRequest{
		Input: json.RawMessage(raw),
	}
	items, err := req.ParseInput()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Content[0].Type != "output_text" {
		t.Errorf("expected content type 'output_text' for assistant, got %q", items[0].Content[0].Type)
	}
	if items[0].Content[0].Text != "I can help." {
		t.Errorf("expected text 'I can help.', got %q", items[0].Content[0].Text)
	}
}

func TestParseInputInvalid(t *testing.T) {
	req := &ResponsesRequest{
		Input: json.RawMessage(`123`), // not string or array
	}
	_, err := req.ParseInput()
	if err == nil {
		t.Fatal("expected error for invalid input, got nil")
	}
}

func TestParseInputMultipleItems(t *testing.T) {
	raw := `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi there"}]}
	]`
	req := &ResponsesRequest{
		Input: json.RawMessage(raw),
	}
	items, err := req.ParseInput()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Role != "user" {
		t.Errorf("expected first item role 'user', got %q", items[0].Role)
	}
	if items[1].Role != "assistant" {
		t.Errorf("expected second item role 'assistant', got %q", items[1].Role)
	}
}

func TestParseInputFunctionCallOutputArrayOutput(t *testing.T) {
	raw := `[
		{"type":"function_call","call_id":"call_1","name":"Shell","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"Error: command required"}]}
	]`
	req := &ResponsesRequest{
		Input: json.RawMessage(raw),
	}
	items, err := req.ParseInput()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[1].Type != "function_call_output" {
		t.Fatalf("expected type function_call_output, got %q", items[1].Type)
	}
	if items[1].Output != "Error: command required" {
		t.Fatalf("expected normalized output text, got %q", items[1].Output)
	}
}

func TestCustomToolCallUnmarshalWithStringInput(t *testing.T) {
	raw := `{"type":"custom_tool_call","call_id":"call_ct1","name":"ApplyPatch","input":"--- a/file.go\n+++ b/file.go"}`
	var item ResponsesInputItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if item.Type != "custom_tool_call" {
		t.Fatalf("expected type custom_tool_call, got %q", item.Type)
	}
	if item.CallID != "call_ct1" {
		t.Fatalf("expected call_id call_ct1, got %q", item.CallID)
	}
	if item.Name != "ApplyPatch" {
		t.Fatalf("expected name ApplyPatch, got %q", item.Name)
	}
	s, ok := item.Input.(string)
	if !ok {
		t.Fatalf("expected string input, got %T", item.Input)
	}
	if s != "--- a/file.go\n+++ b/file.go" {
		t.Fatalf("unexpected input value: %q", s)
	}
}

func TestCustomToolCallUnmarshalWithObjectInput(t *testing.T) {
	raw := `{"type":"custom_tool_call","call_id":"call_ct2","name":"Tool","input":{"key":"value"}}`
	var item ResponsesInputItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	m, ok := item.Input.(map[string]any)
	if !ok {
		t.Fatalf("expected map input, got %T", item.Input)
	}
	if m["key"] != "value" {
		t.Fatalf("unexpected input map: %v", m)
	}
}

func TestCustomToolCallUnmarshalFallbackArgumentsToInput(t *testing.T) {
	raw := `{"type":"custom_tool_call","call_id":"call_ct3","name":"Tool","arguments":"{\"a\":1}"}`
	var item ResponsesInputItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if item.Input == nil {
		t.Fatal("expected Input to be set via Arguments fallback")
	}
	if item.Input != `{"a":1}` {
		t.Fatalf("unexpected input: %v", item.Input)
	}
}

func TestCustomToolCallMarshalEmitsInput(t *testing.T) {
	item := ResponsesInputItem{
		Type:   "custom_tool_call",
		CallID: "call_ct1",
		Name:   "ApplyPatch",
		Input:  "patch content",
	}
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["arguments"] != nil {
		t.Fatalf("custom_tool_call should not emit arguments, got: %v", m["arguments"])
	}
	if m["input"] != "patch content" {
		t.Fatalf("expected input field, got: %v", m["input"])
	}
}

func TestCustomToolCallMarshalFallbackArgumentsToInput(t *testing.T) {
	item := ResponsesInputItem{
		Type:      "custom_tool_call",
		CallID:    "call_ct2",
		Name:      "Tool",
		Arguments: `{"key":"val"}`,
	}
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["arguments"] != nil {
		t.Fatalf("custom_tool_call should not emit arguments")
	}
	if m["input"] != `{"key":"val"}` {
		t.Fatalf("expected input from Arguments fallback, got: %v", m["input"])
	}
}

func TestFunctionCallMarshalDoesNotEmitInput(t *testing.T) {
	item := ResponsesInputItem{
		Type:      "function_call",
		CallID:    "call_fc1",
		Name:      "read_file",
		Arguments: `{"path":"x"}`,
	}
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["input"] != nil {
		t.Fatalf("function_call should not emit input, got: %v", m["input"])
	}
	if m["arguments"] != `{"path":"x"}` {
		t.Fatalf("expected arguments, got: %v", m["arguments"])
	}
}
