package types

import (
	"encoding/json"
	"testing"
)

func TestInt64FromAnyHandlesAllNumericTypes(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want int64
	}{
		{"float64", float64(42), 42},
		{"int", int(99), 99},
		{"int64", int64(1234567890123), 1234567890123},
		{"json.Number", json.Number("999999999999"), 999999999999},
		{"nil", nil, 0},
		{"string", "not a number", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Int64FromAny(tt.val)
			if got != tt.want {
				t.Fatalf("Int64FromAny(%v) = %d, want %d", tt.val, got, tt.want)
			}
		})
	}
}

func TestExtractUsageFromEventBasicFields(t *testing.T) {
	data := map[string]any{
		"response": map[string]any{
			"usage": map[string]any{
				"input_tokens":  float64(100),
				"output_tokens": float64(50),
				"total_tokens":  float64(150),
			},
		},
	}

	u := ExtractUsageFromEvent(data)
	if u == nil {
		t.Fatal("expected non-nil usage")
	}
	if u.PromptTokens != 100 {
		t.Fatalf("PromptTokens: got %d, want 100", u.PromptTokens)
	}
	if u.CompletionTokens != 50 {
		t.Fatalf("CompletionTokens: got %d, want 50", u.CompletionTokens)
	}
	if u.TotalTokens != 150 {
		t.Fatalf("TotalTokens: got %d, want 150", u.TotalTokens)
	}
}

func TestExtractUsageFromEventComputesTotalWhenAbsent(t *testing.T) {
	data := map[string]any{
		"response": map[string]any{
			"usage": map[string]any{
				"input_tokens":  float64(10),
				"output_tokens": float64(20),
			},
		},
	}

	u := ExtractUsageFromEvent(data)
	if u == nil {
		t.Fatal("expected non-nil usage")
	}
	if u.TotalTokens != 30 {
		t.Fatalf("TotalTokens: got %d, want 30", u.TotalTokens)
	}
}

func TestExtractUsageFromEventParsesDetails(t *testing.T) {
	data := map[string]any{
		"response": map[string]any{
			"usage": map[string]any{
				"input_tokens":  float64(100),
				"output_tokens": float64(50),
				"total_tokens":  float64(150),
				"completion_tokens_details": map[string]any{
					"reasoning_tokens":           float64(30),
					"accepted_prediction_tokens": float64(5),
				},
				"prompt_tokens_details": map[string]any{
					"cached_tokens": float64(80),
					"audio_tokens":  float64(10),
				},
			},
		},
	}

	u := ExtractUsageFromEvent(data)
	if u == nil {
		t.Fatal("expected non-nil usage")
	}

	if u.CompletionTokensDetails == nil {
		t.Fatal("expected non-nil CompletionTokensDetails")
	}
	if u.CompletionTokensDetails.ReasoningTokens != 30 {
		t.Fatalf("ReasoningTokens: got %d, want 30", u.CompletionTokensDetails.ReasoningTokens)
	}
	if u.CompletionTokensDetails.AcceptedPredictionTokens != 5 {
		t.Fatalf("AcceptedPredictionTokens: got %d, want 5", u.CompletionTokensDetails.AcceptedPredictionTokens)
	}

	if u.PromptTokensDetails == nil {
		t.Fatal("expected non-nil PromptTokensDetails")
	}
	if u.PromptTokensDetails.CachedTokens != 80 {
		t.Fatalf("CachedTokens: got %d, want 80", u.PromptTokensDetails.CachedTokens)
	}
	if u.PromptTokensDetails.AudioTokens != 10 {
		t.Fatalf("AudioTokens: got %d, want 10", u.PromptTokensDetails.AudioTokens)
	}
}

func TestExtractUsageFromEventNilWhenNoUsage(t *testing.T) {
	data := map[string]any{
		"response": map[string]any{
			"id": "resp_1",
		},
	}
	if u := ExtractUsageFromEvent(data); u != nil {
		t.Fatalf("expected nil usage, got %+v", u)
	}
}
