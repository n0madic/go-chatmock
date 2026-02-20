package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AnthropicMessagesRequest is the incoming request body for POST /v1/messages.
type AnthropicMessagesRequest struct {
	Model         string             `json:"model"`
	Messages      []AnthropicMessage `json:"messages"`
	System        json.RawMessage    `json:"system,omitempty"`
	Tools         []AnthropicTool    `json:"tools,omitempty"`
	ToolChoice    any                `json:"tool_choice,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	MaxTokens     int                `json:"max_tokens,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
}

// AnthropicCountTokensRequest is the incoming body for POST /v1/messages/count_tokens.
type AnthropicCountTokensRequest struct {
	Model    string             `json:"model"`
	Messages []AnthropicMessage `json:"messages"`
	System   json.RawMessage    `json:"system,omitempty"`
	Tools    []AnthropicTool    `json:"tools,omitempty"`
}

// AnthropicCountTokensResponse is the response body for token counting.
type AnthropicCountTokensResponse struct {
	InputTokens int `json:"input_tokens"`
}

// AnthropicMessage represents a single user/assistant message.
type AnthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// AnthropicTool is a Messages API tool definition.
type AnthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema,omitempty"`
}

// AnthropicContentBlock represents a content block used by Messages API.
type AnthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     any             `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// AnthropicMessageResponse is the non-streaming response for POST /v1/messages.
type AnthropicMessageResponse struct {
	ID           string                `json:"id"`
	Type         string                `json:"type"`
	Role         string                `json:"role"`
	Model        string                `json:"model"`
	Content      []AnthropicContentOut `json:"content"`
	StopReason   *string               `json:"stop_reason"`
	StopSequence *string               `json:"stop_sequence"`
	Usage        AnthropicUsage        `json:"usage"`
}

// AnthropicContentOut represents response content blocks.
type AnthropicContentOut struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
}

// AnthropicUsage holds Messages API usage.
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicModelListResponse is the response for GET /v1/models in Anthropic mode.
type AnthropicModelListResponse struct {
	Data    []AnthropicModel `json:"data"`
	HasMore bool             `json:"has_more"`
	FirstID string           `json:"first_id,omitempty"`
	LastID  string           `json:"last_id,omitempty"`
}

// AnthropicModel is a single model entry.
type AnthropicModel struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// AnthropicErrorResponse is the canonical error envelope for Anthropic-compatible routes.
type AnthropicErrorResponse struct {
	Type  string             `json:"type"`
	Error AnthropicErrorBody `json:"error"`
}

// AnthropicErrorBody is the nested error payload.
type AnthropicErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ParseSystemText parses "system" which may be string or array of text blocks.
func ParseSystemText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s), nil
	}

	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("invalid system field")
	}

	var parts []string
	for _, b := range blocks {
		if b.Type == "" || b.Type == "text" {
			txt := strings.TrimSpace(b.Text)
			if txt != "" {
				parts = append(parts, txt)
			}
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// ParseContent parses message content that may be a string or array of blocks.
func (m *AnthropicMessage) ParseContent() ([]AnthropicContentBlock, error) {
	if len(m.Content) == 0 {
		return nil, nil
	}

	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return []AnthropicContentBlock{{Type: "text", Text: s}}, nil
	}

	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil, fmt.Errorf("invalid message content for role %q", m.Role)
	}
	return blocks, nil
}

// ParseToolResultText parses tool_result.content into plain text.
func ParseToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var out strings.Builder
		for _, b := range blocks {
			if b.Type == "" || b.Type == "text" {
				out.WriteString(b.Text)
			}
		}
		return out.String()
	}
	return strings.TrimSpace(string(raw))
}
