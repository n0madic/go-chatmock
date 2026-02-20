package proxy

import (
	"testing"

	"github.com/n0madic/go-chatmock/internal/types"
)

func TestNormalizeResponsesSystemToUser(t *testing.T) {
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
			Type:   "function_call_output",
			Role:   "system",
			Output: "tool output",
		},
		{
			Role:    "system",
			Content: []types.ResponsesContent{{Type: "input_text", Text: "No type provided"}},
		},
	}

	normalizeResponsesSystemToUser(items)

	if items[0].Role != "user" {
		t.Fatalf("expected first item role to be user, got %q", items[0].Role)
	}
	if items[1].Role != "user" {
		t.Fatalf("expected second item role to stay user, got %q", items[1].Role)
	}
	if items[2].Role != "system" {
		t.Fatalf("expected non-message item role to stay system, got %q", items[2].Role)
	}
	if items[3].Role != "user" {
		t.Fatalf("expected empty-type system message role to be user, got %q", items[3].Role)
	}
}
