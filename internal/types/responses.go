package types

import "encoding/json"

// ResponsesRequest is the incoming request body for POST /v1/responses.
type ResponsesRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input"`
	Instructions       string          `json:"instructions,omitempty"`
	Tools              []ResponsesTool `json:"tools,omitempty"`
	ToolChoice         any             `json:"tool_choice,omitempty"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls,omitempty"`
	Reasoning          *ReasoningParam `json:"reasoning,omitempty"`
	Stream             bool            `json:"stream,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Store              *bool           `json:"store,omitempty"`
	Include            []string        `json:"include,omitempty"`
}

// ParseInput parses the Input field, which may be a string or []ResponsesInputItem.
func (req *ResponsesRequest) ParseInput() ([]ResponsesInputItem, error) {
	if req.Input == nil {
		return nil, nil
	}
	// Try as string
	var s string
	if err := json.Unmarshal(req.Input, &s); err == nil {
		return []ResponsesInputItem{
			{Type: "message", Role: "user", Content: []ResponsesContent{{Type: "input_text", Text: s}}},
		}, nil
	}
	// Try as array
	var items []ResponsesInputItem
	if err := json.Unmarshal(req.Input, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// ResponsesResponse is the non-streaming response for POST /v1/responses.
type ResponsesResponse struct {
	ID        string                `json:"id"`
	Object    string                `json:"object"`
	CreatedAt int64                 `json:"created_at"`
	Model     string                `json:"model"`
	Output    []ResponsesOutputItem `json:"output"`
	Status    string                `json:"status"`
	Usage     *ResponsesUsage       `json:"usage,omitempty"`
	Error     *ErrorDetail          `json:"error,omitempty"`
}

// ResponsesOutputItem represents a single output item in the Responses API response.
type ResponsesOutputItem struct {
	Type      string             `json:"type"`
	ID        string             `json:"id,omitempty"`
	Role      string             `json:"role,omitempty"`
	Content   []ResponsesContent `json:"content,omitempty"`
	Status    string             `json:"status,omitempty"`
	Name      string             `json:"name,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
}

// ResponsesUsage holds token usage for a Responses API response.
type ResponsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

// ResponsesInputItem represents a single item in the Responses API input array.
// Uses a flat discriminated union pattern: Type determines which fields are relevant.
type ResponsesInputItem struct {
	Type      string             `json:"type"`
	Role      string             `json:"role,omitempty"`
	Content   []ResponsesContent `json:"content,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Output    string             `json:"output,omitempty"`
}

// Alias is an internal struct used for custom unmarshaling of ResponsesInputItem.
type Alias struct {
	Type      string          `json:"type"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling for ResponsesInputItem.
// It handles `content` as either a plain string or an array of ResponsesContent,
// and defaults `type` to "message" when `role` is present but `type` is absent.
func (item *ResponsesInputItem) UnmarshalJSON(data []byte) error {
	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	item.Type = alias.Type
	item.Role = alias.Role
	item.Name = alias.Name
	item.Arguments = alias.Arguments
	item.CallID = alias.CallID
	item.Output = parseResponsesOutput(alias.Output)

	if alias.Content != nil {
		var s string
		if err := json.Unmarshal(alias.Content, &s); err == nil {
			// String content: wrap into a single content item.
			contentType := "input_text"
			if alias.Role == "assistant" {
				contentType = "output_text"
			}
			item.Content = []ResponsesContent{{Type: contentType, Text: s}}
		} else {
			if err := json.Unmarshal(alias.Content, &item.Content); err != nil {
				return err
			}
		}
	}

	// Default type to "message" for role-based items that omit the type field.
	if item.Type == "" && item.Role != "" {
		item.Type = "message"
	}

	return nil
}

func parseResponsesOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var content []ResponsesContent
	if err := json.Unmarshal(raw, &content); err == nil {
		var out string
		for _, c := range content {
			if c.Text == "" {
				continue
			}
			if out != "" {
				out += "\n"
			}
			out += c.Text
		}
		if out != "" {
			return out
		}
	}

	// Preserve compatibility with non-string output values by encoding them.
	return string(raw)
}

// ResponsesContent represents a content item in a Responses API input message.
type ResponsesContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// ResponsesTool represents a tool in the Responses API format.
type ResponsesTool struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Strict      *bool  `json:"strict,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// UpstreamPayload represents the full payload sent to the ChatGPT backend.
type UpstreamPayload struct {
	Model             string               `json:"model"`
	Instructions      string               `json:"instructions"`
	Input             []ResponsesInputItem `json:"input"`
	Tools             []ResponsesTool      `json:"tools"`
	ToolChoice        any                  `json:"tool_choice"`
	ParallelToolCalls bool                 `json:"parallel_tool_calls"`
	Store             *bool                `json:"store,omitempty"`
	Stream            bool                 `json:"stream"`
	PromptCacheKey    string               `json:"prompt_cache_key"`
	Include           []string             `json:"include,omitempty"`
	Reasoning         *ReasoningParam      `json:"reasoning,omitempty"`
}
