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
