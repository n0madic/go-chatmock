package models

import "testing"

func TestResolveAnthropicModel(t *testing.T) {
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
			fallback:  DefaultAnthropicFallbackModel,
			wantModel: "gpt-5.1-codex-mini",
			wantMatch: true,
		},
		{
			name:      "haiku 3.5 maps to codex mini",
			input:     "claude-3.5-haiku-20241022",
			fallback:  DefaultAnthropicFallbackModel,
			wantModel: "gpt-5.1-codex-mini",
			wantMatch: true,
		},
		{
			name:      "sonnet 3.5",
			input:     "claude-3-5-sonnet-latest",
			fallback:  DefaultAnthropicFallbackModel,
			wantModel: DefaultAnthropicFallbackModel,
			wantMatch: true,
		},
		{
			name:      "sonnet 3.7",
			input:     "claude-3-7-sonnet-20250219",
			fallback:  DefaultAnthropicFallbackModel,
			wantModel: DefaultAnthropicFallbackModel,
			wantMatch: true,
		},
		{
			name:      "sonnet 4",
			input:     "claude-sonnet-4-5",
			fallback:  DefaultAnthropicFallbackModel,
			wantModel: DefaultAnthropicFallbackModel,
			wantMatch: true,
		},
		{
			name:      "opus",
			input:     "claude-opus-4-1",
			fallback:  DefaultAnthropicFallbackModel,
			wantModel: DefaultAnthropicFallbackModel,
			wantMatch: true,
		},
		{
			name:      "unknown falls back",
			input:     "claude-unknown-next",
			fallback:  "gpt-5.3-codex",
			wantModel: "gpt-5.3-codex",
			wantMatch: false,
		},
		{
			name:      "empty input uses fallback",
			input:     "",
			fallback:  "gpt-5.3-codex",
			wantModel: "gpt-5.3-codex",
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
