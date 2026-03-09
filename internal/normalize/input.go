package normalize

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/transform"
	"github.com/n0madic/go-chatmock/internal/types"
)

// NormalizeError represents a normalization error with HTTP status.
type NormalizeError struct {
	StatusCode int
	Message    string
}

type parsedInputCandidate struct {
	Present      bool
	Valid        bool
	Usable       bool
	Items        []types.ResponsesInputItem
	Instructions string
	Messages     int
}

// NormalizeInput selects the input source based on route precedence.
func NormalizeInput(raw map[string]any, route string, prompt string) ([]types.ResponsesInputItem, string, int, string, bool, bool, *NormalizeError) {
	msgCand := parseMessagesCandidate(raw, route)
	inputCand := parseResponsesInputCandidate(raw)
	prompt = strings.TrimSpace(prompt)

	preferInput := route == "responses"
	preferred := msgCand
	alternate := inputCand
	preferredName := "messages"
	alternateName := "input"
	if preferInput {
		preferred = inputCand
		alternate = msgCand
		preferredName = "input"
		alternateName = "messages"
	}

	if preferred.Usable {
		return preferred.Items, preferred.Instructions, preferred.Messages, preferredName, false, false, nil
	}
	if alternate.Usable {
		usedInputFallback := !preferInput && alternateName == "input"
		return alternate.Items, alternate.Instructions, alternate.Messages, alternateName, false, usedInputFallback, nil
	}
	if prompt != "" {
		return []types.ResponsesInputItem{
			{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: prompt}}},
		}, "", 0, "prompt", true, false, nil
	}

	anyInvalid := (preferred.Present && !preferred.Valid) || (alternate.Present && !alternate.Valid)
	if anyInvalid {
		msg := "Request must include valid messages or input"
		if route == "responses" {
			msg = "Request must include valid input or messages"
		}
		return nil, "", 0, "", false, false, &NormalizeError{StatusCode: http.StatusBadRequest, Message: msg}
	}

	msg := "Request must include messages or input"
	if route == "responses" {
		msg = "Request must include input or messages"
	}
	return nil, "", 0, "", false, false, &NormalizeError{StatusCode: http.StatusBadRequest, Message: msg}
}

func parseMessagesCandidate(raw map[string]any, route string) parsedInputCandidate {
	rawMessages, present := raw["messages"]
	if !present {
		return parsedInputCandidate{}
	}
	msgs, ok := parseMessagesFromRaw(rawMessages)
	if !ok {
		return parsedInputCandidate{Present: true, Valid: false}
	}

	switch route {
	case "responses":
		items, instructions := ChatMessagesToResponsesInputWithSystem(msgs)
		usable := len(items) > 0 || strings.TrimSpace(instructions) != ""
		return parsedInputCandidate{
			Present: true, Valid: true, Usable: usable,
			Items: items, Instructions: instructions, Messages: len(msgs),
		}
	default:
		normalized := append([]types.ChatMessage(nil), msgs...)
		ConvertSystemToUser(normalized)
		items := transform.ChatMessagesToResponsesInput(normalized)
		return parsedInputCandidate{
			Present: true, Valid: true, Usable: len(items) > 0,
			Items: items, Messages: len(normalized),
		}
	}
}

func parseResponsesInputCandidate(raw map[string]any) parsedInputCandidate {
	rawInput, present := raw["input"]
	if !present {
		return parsedInputCandidate{}
	}
	items, instructions, ok := ParseResponsesInputFromRaw(rawInput)
	if !ok {
		return parsedInputCandidate{Present: true, Valid: false}
	}
	usable := len(items) > 0 || strings.TrimSpace(instructions) != ""
	return parsedInputCandidate{
		Present: true, Valid: true, Usable: usable,
		Items: items, Instructions: instructions,
	}
}

func parseMessagesFromRaw(rawMessages any) ([]types.ChatMessage, bool) {
	if rawMessages == nil {
		return nil, true
	}
	b, err := json.Marshal(rawMessages)
	if err != nil {
		return nil, false
	}
	var msgs []types.ChatMessage
	if err := json.Unmarshal(b, &msgs); err != nil {
		return nil, false
	}
	return msgs, true
}

// ChatMessagesToResponsesInputWithSystem extracts system messages as instructions.
func ChatMessagesToResponsesInputWithSystem(messages []types.ChatMessage) ([]types.ResponsesInputItem, string) {
	if len(messages) == 0 {
		return nil, ""
	}
	normalized := make([]types.ChatMessage, 0, len(messages))
	var instructions []string
	for _, m := range messages {
		if m.Role != "system" {
			normalized = append(normalized, m)
			continue
		}
		if txt, ok := ExtractSystemTextFromChatContent(m.Content); ok {
			instructions = append(instructions, txt)
			continue
		}
		m.Role = "user"
		normalized = append(normalized, m)
	}
	items := transform.ChatMessagesToResponsesInput(normalized)
	return items, strings.Join(instructions, "\n\n")
}

// ExtractSystemTextFromChatContent extracts text from system message content.
func ExtractSystemTextFromChatContent(content any) (string, bool) {
	switch c := content.(type) {
	case string:
		text := strings.TrimSpace(c)
		if text == "" {
			return "", false
		}
		return text, true
	case []any:
		var parts []string
		for _, rawPart := range c {
			part, ok := rawPart.(map[string]any)
			if !ok {
				return "", false
			}
			typ := strings.TrimSpace(stringFromAny(part["type"]))
			if typ != "" && typ != "text" && typ != "input_text" {
				return "", false
			}
			text := strings.TrimSpace(stringFromAny(part["text"]))
			if text == "" {
				text = strings.TrimSpace(stringFromAny(part["content"]))
			}
			if text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) == 0 {
			return "", false
		}
		return strings.Join(parts, "\n"), true
	default:
		return "", false
	}
}

// ConvertSystemToUser converts the first system message to user role.
func ConvertSystemToUser(messages []types.ChatMessage) {
	for i, m := range messages {
		if m.Role == "system" {
			messages[i] = types.ChatMessage{Role: "user", Content: m.Content}
			if i > 0 {
				msg := messages[i]
				copy(messages[1:i+1], messages[:i])
				messages[0] = msg
			}
			return
		}
	}
}

// ParseResponsesInputFromRaw parses input from raw JSON value.
func ParseResponsesInputFromRaw(rawInput any) ([]types.ResponsesInputItem, string, bool) {
	if rawInput == nil {
		return nil, "", false
	}
	inputBytes, err := json.Marshal(rawInput)
	if err != nil {
		return nil, "", false
	}
	req := types.ResponsesRequest{Input: inputBytes}
	items, err := req.ParseInput()
	if err != nil {
		return nil, "", false
	}
	items, systemInstructions := MoveResponsesSystemMessagesToInstructions(items)
	return items, systemInstructions, true
}

// MoveResponsesSystemMessagesToInstructions extracts text-only system messages
// into instructions.
func MoveResponsesSystemMessagesToInstructions(items []types.ResponsesInputItem) ([]types.ResponsesInputItem, string) {
	if len(items) == 0 {
		return nil, ""
	}
	out := make([]types.ResponsesInputItem, 0, len(items))
	var instructionParts []string
	for _, item := range items {
		if item.Role != "system" || (item.Type != "" && item.Type != "message") {
			out = append(out, item)
			continue
		}
		if text, ok := extractSystemInstructionText(item.Content); ok {
			instructionParts = append(instructionParts, text)
			continue
		}
		item.Role = "user"
		out = append(out, item)
	}
	return out, strings.Join(instructionParts, "\n\n")
}

func extractSystemInstructionText(content []types.ResponsesContent) (string, bool) {
	if len(content) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(content))
	for _, c := range content {
		if c.ImageURL != "" {
			return "", false
		}
		typ := strings.TrimSpace(c.Type)
		if typ != "" && typ != "input_text" && typ != "text" {
			return "", false
		}
		text := strings.TrimSpace(c.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return s
}
