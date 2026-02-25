package proxy

import (
	"testing"

	"github.com/n0madic/go-chatmock/internal/types"
)

func TestInputItemFromOutputItemSkipsCommentaryMessage(t *testing.T) {
	item := types.ResponsesOutputItem{
		Type:  "message",
		Role:  "assistant",
		Phase: "commentary",
		Content: []types.ResponsesContent{
			{Type: "output_text", Text: "internal planning"},
		},
	}

	got, ok := inputItemFromOutputItem(item)
	if ok {
		t.Fatalf("expected commentary message to be skipped, got: %+v", got)
	}
}

func TestInputItemFromOutputItemCustomToolCall(t *testing.T) {
	item := types.ResponsesOutputItem{
		Type:   "custom_tool_call",
		ID:     "item_1",
		CallID: "call_ct1",
		Name:   "ApplyPatch",
		Input:  "patch content",
	}

	got, ok := inputItemFromOutputItem(item)
	if !ok {
		t.Fatal("expected custom_tool_call to be converted")
	}
	if got.Type != "custom_tool_call" {
		t.Fatalf("expected type custom_tool_call, got %q", got.Type)
	}
	if got.CallID != "call_ct1" {
		t.Fatalf("expected call_id call_ct1, got %q", got.CallID)
	}
	if got.Name != "ApplyPatch" {
		t.Fatalf("expected name ApplyPatch, got %q", got.Name)
	}
	if got.Input != "patch content" {
		t.Fatalf("expected Input, got %v", got.Input)
	}
}

func TestInputItemFromOutputItemCustomToolCallUsesIDFallback(t *testing.T) {
	item := types.ResponsesOutputItem{
		Type:  "custom_tool_call",
		ID:    "item_as_call_id",
		Name:  "Tool",
		Input: map[string]any{"a": "b"},
	}

	got, ok := inputItemFromOutputItem(item)
	if !ok {
		t.Fatal("expected conversion")
	}
	if got.CallID != "item_as_call_id" {
		t.Fatalf("expected ID to be used as CallID, got %q", got.CallID)
	}
}

func TestInputItemFromOutputItemKeepsAssistantMessage(t *testing.T) {
	item := types.ResponsesOutputItem{
		Type: "message",
		Role: "assistant",
		Content: []types.ResponsesContent{
			{Type: "output_text", Text: "final answer"},
		},
	}

	got, ok := inputItemFromOutputItem(item)
	if !ok {
		t.Fatalf("expected assistant message to be kept")
	}
	if got.Type != "message" || got.Role != "assistant" {
		t.Fatalf("unexpected converted item: %+v", got)
	}
	if len(got.Content) != 1 || got.Content[0].Text != "final answer" {
		t.Fatalf("unexpected content in converted item: %+v", got)
	}
}
