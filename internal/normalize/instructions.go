package normalize

import (
	"strings"

	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/state"
)

// ComposeInstructions builds the final instructions string for a request.
func ComposeInstructions(
	cfg *config.ServerConfig,
	store *state.Store,
	route string,
	model string,
	clientInstructions string,
	inputSystemInstructions string,
	previousResponseID string,
) string {
	client := joinNonEmpty("\n\n", strings.TrimSpace(clientInstructions), strings.TrimSpace(inputSystemInstructions))

	if route == "responses" && previousResponseID != "" && client == "" {
		if prevInstructions, ok := store.GetInstructions(previousResponseID); ok {
			return prevInstructions
		}
	}

	if client != "" {
		return client
	}
	return strings.TrimSpace(cfg.InstructionsForModel(model))
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, strings.TrimSpace(p))
	}
	return strings.Join(out, sep)
}
