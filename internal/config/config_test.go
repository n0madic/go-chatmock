package config

import (
	"os"
	"testing"
)

// setenv sets an env var for the duration of a test, restoring the original on cleanup.
func setenv(t *testing.T, key, value string) {
	t.Helper()
	original, had := os.LookupEnv(key)
	os.Setenv(key, value) //nolint:errcheck
	t.Cleanup(func() {
		if had {
			os.Setenv(key, original) //nolint:errcheck
		} else {
			os.Unsetenv(key) //nolint:errcheck
		}
	})
}

// TestDefaultFromEnvDefaults checks that DefaultFromEnv returns expected defaults
// when no environment variables are set.
func TestDefaultFromEnvDefaults(t *testing.T) {
	for _, key := range []string{
		"CHATGPT_LOCAL_DEBUG",
		"CHATGPT_LOCAL_ACCESS_TOKEN",
		"CHATGPT_LOCAL_REASONING_EFFORT",
		"CHATGPT_LOCAL_REASONING_SUMMARY",
		"CHATGPT_LOCAL_REASONING_COMPAT",
		"CHATGPT_LOCAL_DEBUG_MODEL",
		"CHATGPT_LOCAL_EXPOSE_REASONING_MODELS",
		"CHATGPT_LOCAL_ENABLE_WEB_SEARCH",
	} {
		os.Unsetenv(key) //nolint:errcheck
	}

	cfg := DefaultFromEnv()

	if cfg.Host != "127.0.0.1" {
		t.Errorf("Host: got %q, want %q", cfg.Host, "127.0.0.1")
	}
	if cfg.Port != 8000 {
		t.Errorf("Port: got %d, want 8000", cfg.Port)
	}
	if cfg.Debug {
		t.Error("Debug should be false by default")
	}
	if cfg.AccessToken != "" {
		t.Errorf("AccessToken: got %q, want empty", cfg.AccessToken)
	}
	if cfg.ReasoningEffort != "medium" {
		t.Errorf("ReasoningEffort: got %q, want %q", cfg.ReasoningEffort, "medium")
	}
	if cfg.ReasoningSummary != "auto" {
		t.Errorf("ReasoningSummary: got %q, want %q", cfg.ReasoningSummary, "auto")
	}
	if cfg.ReasoningCompat != "think-tags" {
		t.Errorf("ReasoningCompat: got %q, want %q", cfg.ReasoningCompat, "think-tags")
	}
	if cfg.DebugModel != "" {
		t.Errorf("DebugModel: got %q, want empty", cfg.DebugModel)
	}
	if cfg.ExposeReasoningModels {
		t.Error("ExposeReasoningModels should be false by default")
	}
	if cfg.DefaultWebSearch {
		t.Error("DefaultWebSearch should be false by default")
	}
}

// TestDefaultFromEnvOverrides verifies that environment variables override defaults.
func TestDefaultFromEnvOverrides(t *testing.T) {
	setenv(t, "CHATGPT_LOCAL_DEBUG", "yes")
	setenv(t, "CHATGPT_LOCAL_ACCESS_TOKEN", "secret-token")
	setenv(t, "CHATGPT_LOCAL_REASONING_EFFORT", "HIGH")
	setenv(t, "CHATGPT_LOCAL_REASONING_SUMMARY", "DETAILED")
	setenv(t, "CHATGPT_LOCAL_REASONING_COMPAT", "O3")
	setenv(t, "CHATGPT_LOCAL_DEBUG_MODEL", "test-model")
	setenv(t, "CHATGPT_LOCAL_EXPOSE_REASONING_MODELS", "true")
	setenv(t, "CHATGPT_LOCAL_ENABLE_WEB_SEARCH", "1")

	cfg := DefaultFromEnv()

	if !cfg.Debug {
		t.Error("Debug should be true when env is 'yes'")
	}
	if cfg.AccessToken != "secret-token" {
		t.Errorf("AccessToken: got %q, want %q", cfg.AccessToken, "secret-token")
	}

	// envOrDefault lowercases and trims values
	if cfg.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort: got %q, want %q", cfg.ReasoningEffort, "high")
	}
	if cfg.ReasoningSummary != "detailed" {
		t.Errorf("ReasoningSummary: got %q, want %q", cfg.ReasoningSummary, "detailed")
	}
	if cfg.ReasoningCompat != "o3" {
		t.Errorf("ReasoningCompat: got %q, want %q", cfg.ReasoningCompat, "o3")
	}
	if cfg.DebugModel != "test-model" {
		t.Errorf("DebugModel: got %q, want %q", cfg.DebugModel, "test-model")
	}
	if !cfg.ExposeReasoningModels {
		t.Error("ExposeReasoningModels should be true when env is 'true'")
	}
	if !cfg.DefaultWebSearch {
		t.Error("DefaultWebSearch should be true when env is '1'")
	}
}

// TestEnvBoolVariants checks all accepted truthy values for boolean env vars.
func TestEnvBoolVariants(t *testing.T) {
	truthy := []string{"1", "true", "yes", "on", "TRUE", "YES", "ON"}
	for _, val := range truthy {
		t.Run(val, func(t *testing.T) {
			setenv(t, "CHATGPT_LOCAL_ENABLE_WEB_SEARCH", val)
			cfg := DefaultFromEnv()
			if !cfg.DefaultWebSearch {
				t.Errorf("expected DefaultWebSearch=true for env value %q", val)
			}
		})
	}

	falsy := []string{"0", "false", "no", "off", ""}
	for _, val := range falsy {
		t.Run("false_"+val, func(t *testing.T) {
			setenv(t, "CHATGPT_LOCAL_ENABLE_WEB_SEARCH", val)
			cfg := DefaultFromEnv()
			if cfg.DefaultWebSearch {
				t.Errorf("expected DefaultWebSearch=false for env value %q", val)
			}
		})
	}
}

// TestClientIDDefault checks the default client ID is returned when no env var is set.
func TestClientIDDefault(t *testing.T) {
	os.Unsetenv("CHATGPT_LOCAL_CLIENT_ID") //nolint:errcheck
	got := ClientID()
	if got != ClientIDDefault {
		t.Errorf("ClientID: got %q, want %q", got, ClientIDDefault)
	}
}

// TestClientIDFromEnv checks that the env var overrides the default client ID.
func TestClientIDFromEnv(t *testing.T) {
	setenv(t, "CHATGPT_LOCAL_CLIENT_ID", "custom_client_id")
	got := ClientID()
	if got != "custom_client_id" {
		t.Errorf("ClientID: got %q, want %q", got, "custom_client_id")
	}
}

// TestOAuthIssuerDefault checks the default OAuth issuer.
func TestOAuthIssuerDefault(t *testing.T) {
	os.Unsetenv("CHATGPT_LOCAL_ISSUER") //nolint:errcheck
	got := OAuthIssuer()
	if got != OAuthIssuerDefault {
		t.Errorf("OAuthIssuer: got %q, want %q", got, OAuthIssuerDefault)
	}
}

// TestTokenURL checks that TokenURL appends the correct path to the issuer.
func TestTokenURL(t *testing.T) {
	os.Unsetenv("CHATGPT_LOCAL_ISSUER") //nolint:errcheck
	got := TokenURL()
	want := OAuthIssuerDefault + "/oauth/token"
	if got != want {
		t.Errorf("TokenURL: got %q, want %q", got, want)
	}
}

// TestInstructionsForModelCodex verifies that codex models get codex instructions.
func TestInstructionsForModelCodex(t *testing.T) {
	cfg := &ServerConfig{
		BaseInstructions:  "base",
		CodexInstructions: "codex",
	}

	codexModels := []string{
		"gpt-5-codex",
		"gpt-5-codex-high",
		"gpt-5.1-codex",
		"gpt-5.1-codex-medium",
		"gpt-5.2-codex",
	}
	for _, m := range codexModels {
		t.Run(m, func(t *testing.T) {
			got := cfg.InstructionsForModel(m)
			if got != "codex" {
				t.Errorf("InstructionsForModel(%q): got %q, want %q", m, got, "codex")
			}
		})
	}
}

// TestInstructionsForModelBase verifies that non-codex models get base instructions.
func TestInstructionsForModelBase(t *testing.T) {
	cfg := &ServerConfig{
		BaseInstructions:  "base",
		CodexInstructions: "codex",
	}

	baseModels := []string{
		"gpt-4o",
		"gpt-4.1",
		"o3",
		"o4-mini",
	}
	for _, m := range baseModels {
		t.Run(m, func(t *testing.T) {
			got := cfg.InstructionsForModel(m)
			if got != "base" {
				t.Errorf("InstructionsForModel(%q): got %q, want %q", m, got, "base")
			}
		})
	}
}

// TestInstructionsForModelEmptyCodex verifies that if CodexInstructions is empty,
// base instructions are returned even for codex models.
func TestInstructionsForModelEmptyCodex(t *testing.T) {
	cfg := &ServerConfig{BaseInstructions: "base"}
	got := cfg.InstructionsForModel("gpt-5-codex")
	if got != "base" {
		t.Errorf("expected base instructions when CodexInstructions is empty, got %q", got)
	}
}
