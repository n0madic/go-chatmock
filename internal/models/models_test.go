package models

import "testing"

func TestNormalizeModelName(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		debugModel string
		want       string
	}{
		{"empty", "", "", "gpt-5"},
		{"gpt5 alias", "gpt5", "", "gpt-5"},
		{"gpt-5", "gpt-5", "", "gpt-5"},
		{"gpt-5-latest", "gpt-5-latest", "", "gpt-5"},
		{"gpt-5.2", "gpt-5.2", "", "gpt-5.2"},
		{"gpt5.2", "gpt5.2", "", "gpt-5.2"},
		{"codex-mini", "codex-mini", "", "codex-mini-latest"},
		{"codex", "codex", "", "codex-mini-latest"},
		{"gpt-5.1-codex", "gpt-5.1-codex", "", "gpt-5.1-codex"},
		{"gpt-5.1-codex-max", "gpt-5.1-codex-max", "", "gpt-5.1-codex-max"},
		{"strip effort suffix", "gpt-5-high", "", "gpt-5"},
		{"strip effort with underscore", "gpt-5_medium", "", "gpt-5"},
		{"strip xhigh", "gpt-5.2-xhigh", "", "gpt-5.2"},
		{"debug model override", "gpt-5", "custom-model", "custom-model"},
		{"colon separator", "gpt-5:high", "", "gpt-5"},
		{"gpt-5.2-codex", "gpt-5.2-codex", "", "gpt-5.2-codex"},
		{"gpt-5.2-codex-latest", "gpt-5.2-codex-latest", "", "gpt-5.2-codex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeModelName(tt.input, tt.debugModel)
			if got != tt.want {
				t.Errorf("NormalizeModelName(%q, %q) = %q, want %q", tt.input, tt.debugModel, got, tt.want)
			}
		})
	}
}

func TestAllowedEfforts(t *testing.T) {
	tests := []struct {
		model    string
		contains []string
		absent   []string
	}{
		{"gpt-5", []string{"minimal", "low", "medium", "high", "xhigh"}, nil},
		{"gpt-5.1", []string{"low", "medium", "high"}, []string{"minimal", "xhigh"}},
		{"gpt-5.2", []string{"low", "medium", "high", "xhigh"}, []string{"minimal"}},
		{"gpt-5.1-codex-max", []string{"low", "medium", "high", "xhigh"}, []string{"minimal"}},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			efforts := AllowedEfforts(tt.model)
			for _, e := range tt.contains {
				if !efforts[e] {
					t.Errorf("AllowedEfforts(%q) should contain %q", tt.model, e)
				}
			}
			for _, e := range tt.absent {
				if efforts[e] {
					t.Errorf("AllowedEfforts(%q) should not contain %q", tt.model, e)
				}
			}
		})
	}
}

func TestModelCatalog(t *testing.T) {
	ids := ModelCatalog(false)
	if len(ids) != 10 {
		t.Errorf("expected 10 base models, got %d", len(ids))
	}

	idsWithVariants := ModelCatalog(true)
	if len(idsWithVariants) <= len(ids) {
		t.Errorf("expected more models with variants, got %d vs %d", len(idsWithVariants), len(ids))
	}
}
