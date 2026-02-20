package models

import "strings"

const (
	// DefaultAnthropicFallbackModel is used when an Anthropic model ID has no explicit mapping.
	DefaultAnthropicFallbackModel = "gpt-5.3-codex"

	// anthropicHaikuMappedModel routes Haiku-family IDs to the mini tier.
	anthropicHaikuMappedModel = "gpt-5.1-codex-mini"
)

// ResolveAnthropicModel maps an Anthropic model ID to an OpenAI/Codex model ID.
// The bool return value reports whether an explicit mapping rule matched.
func ResolveAnthropicModel(input string, fallback string) (string, bool) {
	if strings.TrimSpace(fallback) == "" {
		fallback = DefaultAnthropicFallbackModel
	}

	name := normalizeAnthropicModelID(input)
	if name == "" {
		return fallback, false
	}

	// Haiku family is intentionally routed to codex-mini tier.
	if strings.Contains(name, "haiku") {
		return anthropicHaikuMappedModel, true
	}

	switch {
	case strings.HasPrefix(name, "claude-sonnet-4"):
		return DefaultAnthropicFallbackModel, true
	case strings.HasPrefix(name, "claude-3-7-sonnet"):
		return DefaultAnthropicFallbackModel, true
	case strings.HasPrefix(name, "claude-3-5-sonnet"):
		return DefaultAnthropicFallbackModel, true
	case strings.Contains(name, "opus"):
		return DefaultAnthropicFallbackModel, true
	}

	return fallback, false
}

func normalizeAnthropicModelID(input string) string {
	name := strings.ToLower(strings.TrimSpace(input))
	if name == "" {
		return ""
	}
	name = strings.SplitN(name, "@", 2)[0]
	return name
}
