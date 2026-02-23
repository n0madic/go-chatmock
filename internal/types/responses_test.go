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
