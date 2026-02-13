package reasoning

import (
	"strings"

	"github.com/n0madic/go-chatmock/internal/types"
)

// ApplyReasoningToMessage adds reasoning data to a non-streaming message based on the compat mode.
func ApplyReasoningToMessage(message *types.ChatResponseMsg, reasoningSummaryText, reasoningFullText, compat string) {
	compat = strings.ToLower(strings.TrimSpace(compat))
	if compat == "" {
		compat = "think-tags"
	}

	switch compat {
	case "o3":
		var parts []string
		if reasoningSummaryText != "" {
			parts = append(parts, reasoningSummaryText)
		}
		if reasoningFullText != "" {
			parts = append(parts, reasoningFullText)
		}
		rtxt := strings.Join(parts, "\n\n")
		if rtxt != "" {
			message.Reasoning = types.ReasoningContent{
				Content: []types.ReasoningPart{
					{Type: "text", Text: rtxt},
				},
			}
		}

	case "legacy", "current":
		if reasoningSummaryText != "" {
			message.ReasoningSummary = reasoningSummaryText
		}
		if reasoningFullText != "" {
			message.Reasoning = reasoningFullText
		}

	default: // think-tags
		var parts []string
		if reasoningSummaryText != "" {
			parts = append(parts, reasoningSummaryText)
		}
		if reasoningFullText != "" {
			parts = append(parts, reasoningFullText)
		}
		rtxt := strings.Join(parts, "\n\n")
		if rtxt != "" {
			message.Content = "<think>" + rtxt + "</think>" + message.Content
		}
	}
}
