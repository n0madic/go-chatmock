package proxy

import (
	"testing"

	"github.com/n0madic/go-chatmock/internal/types"
)

func TestMoveResponsesSystemMessagesToInstructions(t *testing.T) {
	items := []types.ResponsesInputItem{
		{
			Type:    "message",
			Role:    "system",
			Content: []types.ResponsesContent{{Type: "input_text", Text: "You are a strict reviewer"}},
		},
		{
			Type:    "message",
			Role:    "user",
			Content: []types.ResponsesContent{{Type: "input_text", Text: "Check this code"}},
		},
		{
			Role:    "system",
			Content: []types.ResponsesContent{{Type: "input_text", Text: "No type provided"}},
		},
	}

	gotItems, instructions := moveResponsesSystemMessagesToInstructions(items)

	if instructions != "You are a strict reviewer\n\nNo type provided" {
		t.Fatalf("unexpected instructions: %q", instructions)
	}
	if len(gotItems) != 1 {
		t.Fatalf("expected only non-system item in input, got %d", len(gotItems))
	}
	if gotItems[0].Role != "user" {
		t.Fatalf("expected remaining item role=user, got %q", gotItems[0].Role)
	}
}

func TestMoveResponsesSystemMessagesToInstructionsKeepsNonTextAsUser(t *testing.T) {
	items := []types.ResponsesInputItem{
		{
			Type: "message",
			Role: "system",
			Content: []types.ResponsesContent{
				{Type: "input_image", ImageURL: "https://example.com/image.png"},
			},
		},
	}

	gotItems, instructions := moveResponsesSystemMessagesToInstructions(items)

	if instructions != "" {
		t.Fatalf("expected empty instructions, got %q", instructions)
	}
	if len(gotItems) != 1 {
		t.Fatalf("expected one item, got %d", len(gotItems))
	}
	if gotItems[0].Role != "user" {
		t.Fatalf("expected role converted to user, got %q", gotItems[0].Role)
	}
}
