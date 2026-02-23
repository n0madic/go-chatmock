package config

import (
	"os"
	"strings"
)

const (
	ClientIDDefault     = "app_EMoamEEZ73f0CkXaXp7hrann"
	OAuthIssuerDefault  = "https://auth.openai.com"
	ResponsesURL        = "https://chatgpt.com/backend-api/codex/responses"
	ModelsURL           = "https://chatgpt.com/backend-api/codex/models"
	OllamaVersionString = "0.12.10"
)

// ServerConfig holds all server configuration.
type ServerConfig struct {
	Host                  string
	Port                  int
	Verbose               bool
	Debug                 bool
	AccessToken           string
	ReasoningEffort       string
	ReasoningSummary      string
	ReasoningCompat       string
	DebugModel            string
	ExposeReasoningModels bool
	DefaultWebSearch      bool
	BaseInstructions      string
	CodexInstructions     string
}

// ClientID returns the OAuth client ID from env or default.
func ClientID() string {
	if id := os.Getenv("CHATGPT_LOCAL_CLIENT_ID"); id != "" {
		return id
	}
	return ClientIDDefault
}

// OAuthIssuer returns the OAuth issuer URL.
func OAuthIssuer() string {
	if iss := os.Getenv("CHATGPT_LOCAL_ISSUER"); iss != "" {
		return iss
	}
	return OAuthIssuerDefault
}

// TokenURL returns the OAuth token endpoint.
func TokenURL() string {
	return OAuthIssuer() + "/oauth/token"
}

// DefaultFromEnv creates a ServerConfig with defaults from environment variables.
func DefaultFromEnv() *ServerConfig {
	return &ServerConfig{
		Host:                  "127.0.0.1",
		Port:                  8000,
		Debug:                 envBool("CHATGPT_LOCAL_DEBUG"),
		AccessToken:           strings.TrimSpace(os.Getenv("CHATGPT_LOCAL_ACCESS_TOKEN")),
		ReasoningEffort:       envOrDefault("CHATGPT_LOCAL_REASONING_EFFORT", "medium"),
		ReasoningSummary:      envOrDefault("CHATGPT_LOCAL_REASONING_SUMMARY", "auto"),
		ReasoningCompat:       envOrDefault("CHATGPT_LOCAL_REASONING_COMPAT", "think-tags"),
		DebugModel:            os.Getenv("CHATGPT_LOCAL_DEBUG_MODEL"),
		ExposeReasoningModels: envBool("CHATGPT_LOCAL_EXPOSE_REASONING_MODELS"),
		DefaultWebSearch:      envBool("CHATGPT_LOCAL_ENABLE_WEB_SEARCH"),
	}
}

// InstructionsForModel returns the appropriate instructions for a given model name.
func (c *ServerConfig) InstructionsForModel(model string) string {
	if strings.HasPrefix(model, "gpt-5-codex") ||
		strings.HasPrefix(model, "gpt-5.1-codex") ||
		strings.HasPrefix(model, "gpt-5.2-codex") ||
		strings.HasPrefix(model, "gpt-5.3-codex") {
		if c.CodexInstructions != "" {
			return c.CodexInstructions
		}
	}
	return c.BaseInstructions
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return strings.ToLower(strings.TrimSpace(v))
	}
	return defaultVal
}

func envBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
