package proxy

import (
	"encoding/json"
	"testing"
)

func TestParseResponsesInputFromRaw(t *testing.T) {
	rawJSON := `[{"role":"system","content":"sys rules"},{"role":"user","content":"hello"}]`
	var rawInput any
	if err := json.Unmarshal([]byte(rawJSON), &rawInput); err != nil {
		t.Fatalf("unmarshal raw input: %v", err)
	}

	items, systemInstructions, ok := parseResponsesInputFromRaw(rawInput)
	if !ok {
		t.Fatal("expected parseResponsesInputFromRaw to succeed")
	}
	if systemInstructions != "sys rules" {
		t.Fatalf("systemInstructions: got %q, want %q", systemInstructions, "sys rules")
	}
	if len(items) != 1 {
		t.Fatalf("expected one non-system item, got %d", len(items))
	}
	if items[0].Type != "message" || items[0].Role != "user" {
		t.Fatalf("unexpected first item: %+v", items[0])
	}
	if len(items[0].Content) != 1 || items[0].Content[0].Text != "hello" {
		t.Fatalf("unexpected content: %+v", items[0].Content)
	}
}

func TestParseResponsesStyleToolsFromRaw(t *testing.T) {
	rawJSON := `[{"type":"function","name":"read_file","description":"Read file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]`
	var rawTools any
	if err := json.Unmarshal([]byte(rawJSON), &rawTools); err != nil {
		t.Fatalf("unmarshal raw tools: %v", err)
	}

	tools := parseResponsesStyleToolsFromRaw(rawTools)
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got %d", len(tools))
	}
	if tools[0].Type != "function" || tools[0].Name != "read_file" {
		t.Fatalf("unexpected parsed tool: %+v", tools[0])
	}
	if tools[0].Strict == nil || *tools[0].Strict {
		t.Fatalf("expected strict=false default, got %+v", tools[0].Strict)
	}
}

func TestParseResponsesStyleToolsFromRawIgnoresChatFormat(t *testing.T) {
	rawJSON := `[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]`
	var rawTools any
	if err := json.Unmarshal([]byte(rawJSON), &rawTools); err != nil {
		t.Fatalf("unmarshal raw tools: %v", err)
	}

	tools := parseResponsesStyleToolsFromRaw(rawTools)
	if len(tools) != 0 {
		t.Fatalf("expected no tools for chat-format payload, got %d", len(tools))
	}
}
