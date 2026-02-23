package proxy

import (
	"strings"
	"testing"
	"time"

	"github.com/n0madic/go-chatmock/internal/responses-state"
	"github.com/n0madic/go-chatmock/internal/types"
)

func TestMissingFunctionCallOutputIDs(t *testing.T) {
	items := []types.ResponsesInputItem{
		{Type: "function_call", CallID: "call_1", Name: "read_file", Arguments: "{}"},
		{Type: "function_call_output", CallID: "call_1", Output: "ok"},
		{Type: "function_call_output", CallID: "call_2", Output: "miss"},
		{Type: "function_call_output", CallID: "call_2", Output: "miss-again"},
	}

	got := missingFunctionCallOutputIDs(items)
	if len(got) != 1 || got[0] != "call_2" {
		t.Fatalf("expected [call_2], got %v", got)
	}
}

func TestRestoreFunctionCallContextSuccess(t *testing.T) {
	s := &Server{
		responsesState: responsesstate.NewStore(5*time.Minute, 100),
	}
	s.responsesState.PutContext("resp_1", []types.ResponsesInputItem{
		{
			Type:    "message",
			Role:    "user",
			Content: []types.ResponsesContent{{Type: "input_text", Text: "Start task"}},
		},
	})
	s.responsesState.Put("resp_1", []responsesstate.FunctionCall{
		{CallID: "call_1", Name: "read_file", Arguments: `{"path":"README.md"}`},
	})

	input := []types.ResponsesInputItem{
		{Type: "function_call_output", CallID: "call_1", Output: "content"},
	}

	got, err := s.restoreFunctionCallContext(input, "resp_1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 items after restore, got %d", len(got))
	}
	if got[0].Type != "message" || got[0].Role != "user" {
		t.Fatalf("unexpected prepended context item: %+v", got[0])
	}
	if got[1].Type != "function_call" || got[1].CallID != "call_1" || got[1].Name != "read_file" {
		t.Fatalf("unexpected restored function_call item: %+v", got[1])
	}
	if got[2].Type != "function_call_output" || got[2].CallID != "call_1" {
		t.Fatalf("unexpected final tool output item: %+v", got[2])
	}
}

func TestRestoreFunctionCallContextMissingPreviousID(t *testing.T) {
	s := &Server{responsesState: responsesstate.NewStore(5*time.Minute, 100)}
	input := []types.ResponsesInputItem{
		{Type: "function_call_output", CallID: "call_1", Output: "content"},
	}

	_, err := s.restoreFunctionCallContext(input, "", true)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "previous_response_id") {
		t.Fatalf("expected previous_response_id hint, got %q", err.Error())
	}
}

func TestRestoreFunctionCallContextUnknownPreviousID(t *testing.T) {
	s := &Server{responsesState: responsesstate.NewStore(5*time.Minute, 100)}
	input := []types.ResponsesInputItem{
		{Type: "function_call_output", CallID: "call_1", Output: "content"},
	}

	_, err := s.restoreFunctionCallContext(input, "resp_missing", true)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown or expired previous_response_id") {
		t.Fatalf("unexpected error: %q", err.Error())
	}
}

func TestRestoreFunctionCallContextUnknownPreviousIDWithoutToolOutput(t *testing.T) {
	s := &Server{responsesState: responsesstate.NewStore(5*time.Minute, 100)}
	input := []types.ResponsesInputItem{
		{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: "hello"}}},
	}

	_, err := s.restoreFunctionCallContext(input, "resp_missing", true)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown or expired previous_response_id") {
		t.Fatalf("unexpected error: %q", err.Error())
	}
}

func TestRestoreFunctionCallContextAcceptsPreviousIDWithInstructionsOnlyState(t *testing.T) {
	s := &Server{responsesState: responsesstate.NewStore(5*time.Minute, 100)}
	s.responsesState.PutInstructions("resp_1", "system policy")
	input := []types.ResponsesInputItem{
		{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: "hello"}}},
	}

	got, err := s.restoreFunctionCallContext(input, "resp_1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("unexpected restored input: %+v", got)
	}
}

func TestRestoreFunctionCallContextMissingCallInState(t *testing.T) {
	s := &Server{
		responsesState: responsesstate.NewStore(5*time.Minute, 100),
	}
	s.responsesState.Put("resp_1", []responsesstate.FunctionCall{
		{CallID: "call_2", Name: "read_file", Arguments: "{}"},
	})
	input := []types.ResponsesInputItem{
		{Type: "function_call_output", CallID: "call_1", Output: "content"},
	}

	_, err := s.restoreFunctionCallContext(input, "resp_1", true)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does not contain required call_id") {
		t.Fatalf("unexpected error: %q", err.Error())
	}
}

func TestRestoreFunctionCallContextNoPrependSkipsContextMerge(t *testing.T) {
	s := &Server{
		responsesState: responsesstate.NewStore(5*time.Minute, 100),
	}
	s.responsesState.PutContext("resp_1", []types.ResponsesInputItem{
		{
			Type:    "message",
			Role:    "user",
			Content: []types.ResponsesContent{{Type: "input_text", Text: "old history"}},
		},
	})

	input := []types.ResponsesInputItem{
		{
			Type:    "message",
			Role:    "user",
			Content: []types.ResponsesContent{{Type: "input_text", Text: "current message"}},
		},
	}

	got, err := s.restoreFunctionCallContext(input, "resp_1", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected input without prepended history, got %d items", len(got))
	}
	if got[0].Role != "user" || got[0].Content[0].Text != "current message" {
		t.Fatalf("unexpected restored item: %+v", got[0])
	}
}

func TestRestoreFunctionCallContextNoPrependAllowsUnknownPreviousIDWithoutToolOutput(t *testing.T) {
	s := &Server{responsesState: responsesstate.NewStore(5*time.Minute, 100)}
	input := []types.ResponsesInputItem{
		{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: "hello"}}},
	}

	got, err := s.restoreFunctionCallContext(input, "resp_missing", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("unexpected restored input: %+v", got)
	}
}

func TestExtractFunctionCallFromOutputItem(t *testing.T) {
	item := map[string]any{
		"type":      "function_call",
		"call_id":   "call_1",
		"name":      "search_files",
		"arguments": map[string]any{"pattern": "TODO", "directory": "."},
	}

	call, ok := extractFunctionCallFromOutputItem(item)
	if !ok {
		t.Fatal("expected function call")
	}
	if call.CallID != "call_1" || call.Name != "search_files" {
		t.Fatalf("unexpected call: %+v", call)
	}
	if !strings.Contains(call.Arguments, `"pattern":"TODO"`) {
		t.Fatalf("expected JSON arguments, got %q", call.Arguments)
	}
}

func TestIsUnsupportedParameterError(t *testing.T) {
	raw := []byte(`{"error":{"message":"Unsupported parameter: store"}}`)
	if !isUnsupportedParameterError(raw, "store") {
		t.Fatal("expected true for store unsupported")
	}
	if isUnsupportedParameterError(raw, "include") {
		t.Fatal("expected false for include")
	}
}

func TestNormalizeStoreForUpstream(t *testing.T) {
	storeTrue := true
	storeFalse := false

	tests := []struct {
		name       string
		in         *bool
		wantValue  bool
		wantForced bool
	}{
		{name: "nil", in: nil, wantValue: false, wantForced: false},
		{name: "false", in: &storeFalse, wantValue: false, wantForced: false},
		{name: "true", in: &storeTrue, wantValue: false, wantForced: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, forced := normalizeStoreForUpstream(tt.in)
			if got == nil {
				t.Fatal("expected non-nil bool pointer")
			}
			if *got != tt.wantValue {
				t.Fatalf("got value=%v, want %v", *got, tt.wantValue)
			}
			if forced != tt.wantForced {
				t.Fatalf("got forced=%v, want %v", forced, tt.wantForced)
			}
		})
	}
}
