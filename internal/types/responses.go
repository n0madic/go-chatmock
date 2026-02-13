package types

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
	Store             bool                 `json:"store"`
	Stream            bool                 `json:"stream"`
	PromptCacheKey    string               `json:"prompt_cache_key"`
	Include           []string             `json:"include,omitempty"`
	Reasoning         *ReasoningParam      `json:"reasoning,omitempty"`
}
