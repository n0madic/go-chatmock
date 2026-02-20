package types

import (
	"encoding/json"
	"testing"
)

func TestParseSystemText(t *testing.T) {
	got, err := ParseSystemText(json.RawMessage(`"Be concise"`))
	if err != nil {
		t.Fatalf("ParseSystemText returned error: %v", err)
	}
	if got != "Be concise" {
		t.Fatalf("unexpected system text: %q", got)
	}

	got, err = ParseSystemText(json.RawMessage(`[{"type":"text","text":"Rule one"},{"type":"text","text":"Rule two"}]`))
	if err != nil {
		t.Fatalf("ParseSystemText returned error for array: %v", err)
	}
	if got != "Rule one\n\nRule two" {
		t.Fatalf("unexpected joined system text: %q", got)
	}
}

func TestAnthropicMessageParseContent(t *testing.T) {
	msg := AnthropicMessage{Role: "user", Content: json.RawMessage(`"hello"`)}
	blocks, err := msg.ParseContent()
	if err != nil {
		t.Fatalf("ParseContent returned error: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Text != "hello" {
		t.Fatalf("unexpected parsed blocks: %+v", blocks)
	}

	msg = AnthropicMessage{
		Role:    "assistant",
		Content: json.RawMessage(`[{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"README.md"}}]`),
	}
	blocks, err = msg.ParseContent()
	if err != nil {
		t.Fatalf("ParseContent returned error for array: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != "tool_use" || blocks[0].Name != "read_file" {
		t.Fatalf("unexpected parsed tool block: %+v", blocks)
	}
}

func TestParseToolResultText(t *testing.T) {
	if got := ParseToolResultText(json.RawMessage(`"done"`)); got != "done" {
		t.Fatalf("unexpected string tool_result text: %q", got)
	}

	raw := json.RawMessage(`[{"type":"text","text":"line1"},{"type":"text","text":"line2"}]`)
	if got := ParseToolResultText(raw); got != "line1line2" {
		t.Fatalf("unexpected block tool_result text: %q", got)
	}
}
