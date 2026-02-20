package models

import "testing"

func TestResolveAnthropicModel(t *testing.T) {
	const customFallback = "custom-fallback-model"

	tests := []struct {
		name      string
		input     string
		fallback  string
		wantModel string
		wantMatch bool
	}{
		{
			name:      "haiku maps to codex mini",
			input:     "claude-3-haiku-20240307",
			fallback:  customFallback,
			wantModel: anthropicHaikuMappedModel,
			wantMatch: true,
		},
		{
			name:      "haiku 3.5 maps to codex mini",
			input:     "claude-3.5-haiku-20241022",
			fallback:  customFallback,
			wantModel: anthropicHaikuMappedModel,
			wantMatch: true,
		},
		{
			name:      "haiku normalization still matches",
			input:     " Claude-3-HAIKU-20240307@latest ",
			fallback:  customFallback,
			wantModel: anthropicHaikuMappedModel,
			wantMatch: true,
		},
		{
			name:      "sonnet 3.5",
			input:     "claude-3-5-sonnet-latest",
			fallback:  customFallback,
			wantModel: DefaultAnthropicFallbackModel,
			wantMatch: true,
		},
		{
			name:      "sonnet 3.7",
			input:     "claude-3-7-sonnet-20250219",
			fallback:  customFallback,
			wantModel: DefaultAnthropicFallbackModel,
			wantMatch: true,
		},
		{
			name:      "sonnet 4",
			input:     "claude-sonnet-4-5",
			fallback:  customFallback,
			wantModel: DefaultAnthropicFallbackModel,
			wantMatch: true,
		},
		{
			name:      "opus",
			input:     "claude-opus-4-1",
			fallback:  customFallback,
			wantModel: DefaultAnthropicFallbackModel,
			wantMatch: true,
		},
		{
			name:      "unknown returns provided fallback",
			input:     "claude-unknown-next",
			fallback:  customFallback,
			wantModel: customFallback,
			wantMatch: false,
		},
		{
			name:      "empty input returns provided fallback",
			input:     "",
			fallback:  customFallback,
			wantModel: customFallback,
			wantMatch: false,
		},
		{
			name:      "empty fallback uses default model",
			input:     "claude-unknown-next",
			fallback:  "   ",
			wantModel: DefaultAnthropicFallbackModel,
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotMatch := ResolveAnthropicModel(tt.input, tt.fallback)
			if gotModel != tt.wantModel {
				t.Fatalf("ResolveAnthropicModel(%q, %q) model = %q, want %q", tt.input, tt.fallback, gotModel, tt.wantModel)
			}
			if gotMatch != tt.wantMatch {
				t.Fatalf("ResolveAnthropicModel(%q, %q) match = %v, want %v", tt.input, tt.fallback, gotMatch, tt.wantMatch)
			}
		})
	}
}

func TestResolveAnthropicReasoningEffort(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantEffort string
		wantMatch  bool
	}{
		{
			name:       "opus uses xhigh",
			input:      "claude-opus-4-1",
			wantEffort: anthropicOpusReasoningEffort,
			wantMatch:  true,
		},
		{
			name:       "opus normalization still matches",
			input:      " Claude-OPUS-4-1@latest ",
			wantEffort: anthropicOpusReasoningEffort,
			wantMatch:  true,
		},
		{
			name:       "sonnet has no override",
			input:      "claude-sonnet-4-5",
			wantEffort: "",
			wantMatch:  false,
		},
		{
			name:       "empty has no override",
			input:      "",
			wantEffort: "",
			wantMatch:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEffort, gotMatch := ResolveAnthropicReasoningEffort(tt.input)
			if gotEffort != tt.wantEffort {
				t.Fatalf("ResolveAnthropicReasoningEffort(%q) effort = %q, want %q", tt.input, gotEffort, tt.wantEffort)
			}
			if gotMatch != tt.wantMatch {
				t.Fatalf("ResolveAnthropicReasoningEffort(%q) match = %v, want %v", tt.input, gotMatch, tt.wantMatch)
			}
		})
	}
}
