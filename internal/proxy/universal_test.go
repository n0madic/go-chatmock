package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/n0madic/go-chatmock/internal/config"
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

func TestComposeInstructionsForRouteChatPrefersClientInstructions(t *testing.T) {
	s := &Server{
		Config: &config.ServerConfig{
			BaseInstructions:  "base instructions",
			CodexInstructions: "codex instructions",
		},
	}

	got := composeInstructionsForRoute(
		s,
		universalRouteChat,
		"gpt-5.3-codex",
		"client instructions",
		"input system instructions",
		"",
	)

	want := "client instructions\n\ninput system instructions"
	if got != want {
		t.Fatalf("composeInstructionsForRoute(chat) = %q, want %q", got, want)
	}
}

func TestComposeInstructionsForRouteChatFallsBackToBasePrompt(t *testing.T) {
	s := &Server{
		Config: &config.ServerConfig{
			BaseInstructions:  "base instructions",
			CodexInstructions: "codex instructions",
		},
	}

	got := composeInstructionsForRoute(
		s,
		universalRouteChat,
		"gpt-5.3-codex",
		"",
		"",
		"",
	)

	want := "codex instructions"
	if got != want {
		t.Fatalf("composeInstructionsForRoute(chat base fallback) = %q, want %q", got, want)
	}
}

func TestParseResponsesStyleToolsFromRawCustomTool(t *testing.T) {
	raw := []any{
		map[string]any{
			"type":        "custom",
			"name":        "ApplyPatch",
			"description": "Apply a patch",
			"format":      map[string]any{"type": "text"},
		},
		map[string]any{
			"type":       "function",
			"name":       "read_file",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
	tools := parseResponsesStyleToolsFromRaw(raw)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Type != "custom" || tools[0].Name != "ApplyPatch" {
		t.Fatalf("unexpected first tool: %+v", tools[0])
	}
	if tools[1].Type != "function" || tools[1].Name != "read_file" {
		t.Fatalf("unexpected second tool: %+v", tools[1])
	}
}

func TestNormalizeUniversalToolsMergesCustomTools(t *testing.T) {
	s := &Server{
		Config: &config.ServerConfig{},
	}
	raw := mustDecodeMap(t, `{
		"tools": [
			{"type":"function","name":"read_file","parameters":{"type":"object","properties":{}}},
			{"type":"custom","name":"ApplyPatch","format":{"type":"text"}}
		]
	}`)

	var chatReq types.ChatCompletionRequest
	var responsesReq types.ResponsesRequest
	json.Unmarshal([]byte(`{"tools":[{"type":"function","name":"read_file","parameters":{"type":"object","properties":{}}}]}`), &responsesReq)

	tools, _, _, _, terr := s.normalizeUniversalTools(raw, universalRouteResponses, chatReq, responsesReq, "auto")
	if terr != nil {
		t.Fatalf("normalizeUniversalTools error: %+v", terr)
	}

	hasCustom := false
	for _, tool := range tools {
		if tool.Type == "custom" && tool.Name == "ApplyPatch" {
			hasCustom = true
		}
	}
	if !hasCustom {
		t.Fatalf("expected custom tool ApplyPatch to be merged, tools: %+v", tools)
	}
}

func TestNormalizeUniversalRequestResponsesDebugModelBypassesValidation(t *testing.T) {
	s := &Server{
		Config: &config.ServerConfig{
			DebugModel: "gpt-5",
		},
	}

	req, nerr := s.normalizeUniversalRequest([]byte(`{
		"model":"unknown-model",
		"input":"hello",
		"stream":false
	}`), universalRouteResponses)
	if nerr != nil {
		t.Fatalf("normalizeUniversalRequest returned error: %+v", nerr)
	}
	if req.RequestedModel != "unknown-model" {
		t.Fatalf("requested model: got %q, want %q", req.RequestedModel, "unknown-model")
	}
	if req.Model != "gpt-5" {
		t.Fatalf("normalized model: got %q, want %q", req.Model, "gpt-5")
	}

	w := httptest.NewRecorder()
	if ok := s.validateModel(w, req.Model); !ok {
		t.Fatal("validateModel should pass when debug model is configured")
	}
	if body := w.Body.String(); body != "" {
		t.Fatalf("expected empty validation body, got %q", body)
	}
}
