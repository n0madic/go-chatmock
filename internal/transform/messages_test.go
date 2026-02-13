package transform

import (
	"testing"

	"github.com/n0madic/go-chatmock/internal/types"
)

func TestChatMessagesToResponsesInput(t *testing.T) {
	tests := []struct {
		name     string
		messages []types.ChatMessage
		wantLen  int
		check    func([]types.ResponsesInputItem) bool
	}{
		{
			name:     "empty messages",
			messages: nil,
			wantLen:  0,
		},
		{
			name: "system messages skipped",
			messages: []types.ChatMessage{
				{Role: "system", Content: "You are helpful"},
			},
			wantLen: 0,
		},
		{
			name: "simple user message",
			messages: []types.ChatMessage{
				{Role: "user", Content: "Hello"},
			},
			wantLen: 1,
			check: func(items []types.ResponsesInputItem) bool {
				return items[0].Type == "message" && items[0].Role == "user"
			},
		},
		{
			name: "assistant message",
			messages: []types.ChatMessage{
				{Role: "assistant", Content: "Hi there"},
			},
			wantLen: 1,
			check: func(items []types.ResponsesInputItem) bool {
				return items[0].Role == "assistant"
			},
		},
		{
			name: "tool message",
			messages: []types.ChatMessage{
				{Role: "tool", ToolCallID: "call_123", Content: "result"},
			},
			wantLen: 1,
			check: func(items []types.ResponsesInputItem) bool {
				return items[0].Type == "function_call_output" &&
					items[0].CallID == "call_123" &&
					items[0].Output == "result"
			},
		},
		{
			name: "assistant with tool_calls",
			messages: []types.ChatMessage{
				{
					Role:    "assistant",
					Content: "",
					ToolCalls: []types.ToolCall{
						{
							ID:   "call_abc",
							Type: "function",
							Function: types.FunctionCall{
								Name:      "get_weather",
								Arguments: `{"city":"NYC"}`,
							},
						},
					},
				},
			},
			wantLen: 1,
			check: func(items []types.ResponsesInputItem) bool {
				return items[0].Type == "function_call" && items[0].Name == "get_weather"
			},
		},
		{
			name: "multimodal content",
			messages: []types.ChatMessage{
				{
					Role: "user",
					Content: []any{
						map[string]any{"type": "text", "text": "What is this?"},
						map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/img.png"}},
					},
				},
			},
			wantLen: 1,
			check: func(items []types.ResponsesInputItem) bool {
				return len(items[0].Content) == 2
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChatMessagesToResponsesInput(tt.messages)
			if len(got) != tt.wantLen {
				t.Errorf("got %d items, want %d", len(got), tt.wantLen)
			}
			if tt.check != nil && !tt.check(got) {
				t.Errorf("check failed for %v", got)
			}
		})
	}
}
