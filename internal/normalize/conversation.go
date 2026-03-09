package normalize

import "strings"

// ExtractConversationID reads a stable conversation identifier from the request payload.
func ExtractConversationID(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	if md, ok := raw["metadata"].(map[string]any); ok {
		for _, key := range []string{"cursorConversationId", "conversation_id", "conversationId"} {
			if id := strings.TrimSpace(stringFromAny(md[key])); id != "" {
				return id
			}
		}
	}
	for _, key := range []string{"cursorConversationId", "conversation_id", "conversationId"} {
		if id := strings.TrimSpace(stringFromAny(raw[key])); id != "" {
			return id
		}
	}
	return ""
}
