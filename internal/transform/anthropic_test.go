package transform

import (
	"encoding/json"
	"testing"

	"github.com/n0madic/go-chatmock/internal/types"
)

func TestAnthropicMessagesToResponsesInput(t *testing.T) {
	messages := []types.AnthropicMessage{
		{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		},
		{
			Role: "assistant",
			Content: json.RawMessage(`[
				{"type":"text","text":"Calling tool"},
				{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"README.md"}}
			]`),
		},
		{
			Role: "user",
			Content: json.RawMessage(`[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"file content"}
			]`),
		},
	}

	got, err := AnthropicMessagesToResponsesInput(messages)
	if err != nil {
		t.Fatalf("AnthropicMessagesToResponsesInput returned error: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("expected 4 input items, got %d", len(got))
	}

	if got[0].Type != "message" || got[0].Role != "user" || got[0].Content[0].Text != "hello" {
		t.Fatalf("unexpected first item: %+v", got[0])
	}
	if got[1].Type != "message" || got[1].Role != "assistant" || got[1].Content[0].Type != "output_text" {
		t.Fatalf("unexpected second item: %+v", got[1])
	}
	if got[2].Type != "function_call" || got[2].CallID != "toolu_1" || got[2].Name != "read_file" {
		t.Fatalf("unexpected tool_use item: %+v", got[2])
	}
	if got[3].Type != "function_call_output" || got[3].CallID != "toolu_1" || got[3].Output != "file content" {
		t.Fatalf("unexpected tool_result item: %+v", got[3])
	}
}

func TestAnthropicToolChoiceToResponses(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want any
	}{
		{"nil", nil, "auto"},
		{"string none", "none", "none"},
		{"map auto", map[string]any{"type": "auto"}, "auto"},
		{"map any", map[string]any{"type": "any"}, map[string]any{"type": "required"}},
		{"map tool", map[string]any{"type": "tool", "name": "read_file"}, map[string]any{"type": "function", "name": "read_file"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AnthropicToolChoiceToResponses(tt.in)
			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(tt.want)
			if string(gb) != string(wb) {
				t.Fatalf("AnthropicToolChoiceToResponses(%v) = %s, want %s", tt.in, gb, wb)
			}
		})
	}
}

func TestAnthropicToolsToResponsesPassesSchemaAsIs(t *testing.T) {
	tools := []types.AnthropicTool{
		{
			Name:        "Glob",
			Description: "Find files",
			InputSchema: map[string]any{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type":    "object",
				"properties": map[string]any{
					"pattern": map[string]any{"type": "string"},
					"path":    map[string]any{"type": "string", "default": "."},
					"url":     map[string]any{"type": "string", "format": "uri"},
				},
				"required": []string{"pattern"},
			},
		},
	}

	got := AnthropicToolsToResponses(tools)
	if len(got) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(got))
	}
	if got[0].Strict != nil {
		t.Fatalf("expected strict to be omitted, got %+v", got[0].Strict)
	}

	params, ok := got[0].Parameters.(map[string]any)
	if !ok {
		t.Fatalf("expected map schema, got %T", got[0].Parameters)
	}
	if _, exists := params["$schema"]; !exists {
		t.Fatalf("expected $schema to remain in passthrough schema")
	}

	props, _ := params["properties"].(map[string]any)
	if _, exists := props["path"]; !exists {
		t.Fatalf("expected optional field 'path' to remain, got %+v", props)
	}
	urlSpec, _ := props["url"].(map[string]any)
	if _, exists := urlSpec["format"]; !exists {
		t.Fatalf("expected format to remain in passthrough schema, got %+v", urlSpec)
	}
	pathSpec, _ := props["path"].(map[string]any)
	if _, exists := pathSpec["default"]; !exists {
		t.Fatalf("expected default to remain in passthrough schema, got %+v", pathSpec)
	}
}

func TestEstimateResponsesInputTokensDeterministic(t *testing.T) {
	input := []types.ResponsesInputItem{
		{
			Type:    "message",
			Role:    "user",
			Content: []types.ResponsesContent{{Type: "input_text", Text: "hello world"}},
		},
	}
	tools := []types.ResponsesTool{
		{
			Type:        "function",
			Name:        "read_file",
			Description: "Read a file",
			Parameters: map[string]any{
				"type": "object",
			},
		},
	}

	got1 := EstimateResponsesInputTokens("system text", input, tools)
	got2 := EstimateResponsesInputTokens("system text", input, tools)

	if got1 <= 0 {
		t.Fatalf("expected positive token estimate, got %d", got1)
	}
	if got1 != got2 {
		t.Fatalf("expected deterministic estimate, got %d and %d", got1, got2)
	}
}
