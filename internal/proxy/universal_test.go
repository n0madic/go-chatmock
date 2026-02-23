package proxy

import (
	"encoding/json"
	"testing"

	"github.com/n0madic/go-chatmock/internal/types"
)

func TestNormalizeUniversalInputPrecedenceChatPrefersMessages(t *testing.T) {
	raw := mustDecodeMap(t, `{
		"messages":[{"role":"user","content":"from_messages"}],
		"input":[{"role":"user","content":"from_input"}]
	}`)

	items, _, _, source, usedPromptFallback, usedInputFallback, err := normalizeUniversalInput(raw, universalRouteChat, "")
	if err != nil {
		t.Fatalf("normalizeUniversalInput returned error: %+v", err)
	}
	if source != "messages" {
		t.Fatalf("source: got %q, want %q", source, "messages")
	}
	if usedPromptFallback {
		t.Fatal("usedPromptFallback should be false")
	}
	if usedInputFallback {
		t.Fatal("usedInputFallback should be false")
	}
	if got := firstText(items); got != "from_messages" {
		t.Fatalf("first text: got %q, want %q", got, "from_messages")
	}
}

func TestNormalizeUniversalInputPrecedenceResponsesPrefersInput(t *testing.T) {
	raw := mustDecodeMap(t, `{
		"messages":[{"role":"user","content":"from_messages"}],
		"input":[{"role":"user","content":"from_input"}]
	}`)

	items, _, _, source, _, _, err := normalizeUniversalInput(raw, universalRouteResponses, "")
	if err != nil {
		t.Fatalf("normalizeUniversalInput returned error: %+v", err)
	}
	if source != "input" {
		t.Fatalf("source: got %q, want %q", source, "input")
	}
	if got := firstText(items); got != "from_input" {
		t.Fatalf("first text: got %q, want %q", got, "from_input")
	}
}

func TestNormalizeUniversalInputFallbackChatUsesInputWhenMessagesInvalid(t *testing.T) {
	raw := mustDecodeMap(t, `{
		"messages":"invalid",
		"input":[{"role":"user","content":"fallback_input"}]
	}`)

	items, _, _, source, _, usedInputFallback, err := normalizeUniversalInput(raw, universalRouteChat, "")
	if err != nil {
		t.Fatalf("normalizeUniversalInput returned error: %+v", err)
	}
	if source != "input" {
		t.Fatalf("source: got %q, want %q", source, "input")
	}
	if !usedInputFallback {
		t.Fatal("usedInputFallback should be true")
	}
	if got := firstText(items); got != "fallback_input" {
		t.Fatalf("first text: got %q, want %q", got, "fallback_input")
	}
}

func TestNormalizeUniversalInputFallbackResponsesUsesMessagesWhenInputInvalid(t *testing.T) {
	raw := mustDecodeMap(t, `{
		"messages":[{"role":"user","content":"fallback_messages"}],
		"input":123
	}`)

	items, _, _, source, _, _, err := normalizeUniversalInput(raw, universalRouteResponses, "")
	if err != nil {
		t.Fatalf("normalizeUniversalInput returned error: %+v", err)
	}
	if source != "messages" {
		t.Fatalf("source: got %q, want %q", source, "messages")
	}
	if got := firstText(items); got != "fallback_messages" {
		t.Fatalf("first text: got %q, want %q", got, "fallback_messages")
	}
}

func mustDecodeMap(t *testing.T, rawJSON string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &m); err != nil {
		t.Fatalf("unmarshal map: %v", err)
	}
	return m
}

func firstText(items []types.ResponsesInputItem) string {
	for _, item := range items {
		for _, content := range item.Content {
			if content.Text != "" {
				return content.Text
			}
		}
	}
	return ""
}
