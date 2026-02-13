package transform

import (
	"encoding/base64"
	"net/url"
	"strings"

	"github.com/n0madic/go-chatmock/internal/types"
)

// ChatMessagesToResponsesInput converts OpenAI chat messages to the Responses API input format.
func ChatMessagesToResponsesInput(messages []types.ChatMessage) []types.ResponsesInputItem {
	var inputItems []types.ResponsesInputItem

	for _, message := range messages {
		role := message.Role

		if role == "system" {
			continue
		}

		if role == "tool" {
			callID := message.ToolCallID
			if callID == "" {
				callID = message.Name
			}
			if callID != "" {
				content := extractToolContent(message.Content)
				inputItems = append(inputItems, types.ResponsesInputItem{
					Type:   "function_call_output",
					CallID: callID,
					Output: content,
				})
			}
			continue
		}

		if role == "assistant" {
			for _, tc := range message.ToolCalls {
				tcType := tc.Type
				if tcType != "" && tcType != "function" {
					continue
				}
				callID := tc.ID
				name := tc.Function.Name
				args := tc.Function.Arguments
				if callID != "" && name != "" && args != "" {
					inputItems = append(inputItems, types.ResponsesInputItem{
						Type:      "function_call",
						Name:      name,
						Arguments: args,
						CallID:    callID,
					})
				}
			}
		}

		contentItems := extractContentItems(message.Content, role)
		if len(contentItems) == 0 {
			continue
		}

		roleOut := "user"
		if role == "assistant" {
			roleOut = "assistant"
		}
		inputItems = append(inputItems, types.ResponsesInputItem{
			Type:    "message",
			Role:    roleOut,
			Content: contentItems,
		})
	}

	return inputItems
}

func extractToolContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var texts []string
		for _, part := range c {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			t := stringOr(p, "text", "")
			if t == "" {
				t, _ = p["content"].(string)
			}
			if t != "" {
				texts = append(texts, t)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

func extractContentItems(content any, role string) []types.ResponsesContent {
	var items []types.ResponsesContent

	switch c := content.(type) {
	case []any:
		for _, part := range c {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			ptype, _ := p["type"].(string)
			switch ptype {
			case "text":
				text := stringOrMap(p, "text", "")
				if text == "" {
					text, _ = p["content"].(string)
				}
				if text != "" {
					kind := "input_text"
					if role == "assistant" {
						kind = "output_text"
					}
					items = append(items, types.ResponsesContent{Type: kind, Text: text})
				}
			case "image_url":
				var imgURL string
				if img, ok := p["image_url"].(map[string]any); ok {
					imgURL, _ = img["url"].(string)
				} else if s, ok := p["image_url"].(string); ok {
					imgURL = s
				}
				if imgURL != "" {
					items = append(items, types.ResponsesContent{
						Type:     "input_image",
						ImageURL: normalizeImageDataURL(imgURL),
					})
				}
			}
		}
	case string:
		if c != "" {
			kind := "input_text"
			if role == "assistant" {
				kind = "output_text"
			}
			items = append(items, types.ResponsesContent{Type: kind, Text: c})
		}
	}

	return items
}

func normalizeImageDataURL(u string) string {
	if !strings.HasPrefix(u, "data:image/") {
		return u
	}
	if !strings.Contains(u, ";base64,") {
		return u
	}
	parts := strings.SplitN(u, ",", 2)
	if len(parts) != 2 {
		return u
	}
	header := parts[0]
	data := parts[1]
	data, _ = url.QueryUnescape(data)
	data = strings.NewReplacer("\n", "", "\r", "", "-", "+", "_", "/").Replace(data)
	if pad := len(data) % 4; pad != 0 {
		data += strings.Repeat("=", 4-pad)
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return u
	}
	return header + "," + data
}

func stringOr(m map[string]any, key, fallback string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

func stringOrMap(m map[string]any, key, fallback string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}
