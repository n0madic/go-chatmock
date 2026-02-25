package types

// --- Request types ---

// ChatCompletionRequest represents an OpenAI chat completion request.
type ChatCompletionRequest struct {
	Model               string          `json:"model"`
	Messages            []ChatMessage   `json:"messages,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	StreamOptions       *StreamOptions  `json:"stream_options,omitempty"`
	Tools               []ChatTool      `json:"tools,omitempty"`
	ToolChoice          any             `json:"tool_choice,omitempty"`
	ParallelToolCalls   bool            `json:"parallel_tool_calls,omitempty"`
	Reasoning           *ReasoningParam `json:"reasoning,omitempty"`
	ResponsesTools      []any           `json:"responses_tools,omitempty"`
	ResponsesToolChoice string          `json:"responses_tool_choice,omitempty"`
	Prompt              string          `json:"prompt,omitempty"`
}

// ChatMessage represents an OpenAI chat message.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ContentPart represents a part of a multimodal content array.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL holds an image URL reference.
type ImageURL struct {
	URL string `json:"url"`
}

// ChatTool represents a tool in the OpenAI format.
type ChatTool struct {
	Type     string       `json:"type"`
	Function *FunctionDef `json:"function,omitempty"`
}

// FunctionDef defines a function tool.
type FunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// ToolCall represents a tool call in a message.
type ToolCall struct {
	Index    int          `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the function name and arguments string.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// StreamOptions holds stream-specific options.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ReasoningParam represents the reasoning parameter for the Responses API.
type ReasoningParam struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

// --- Response types ---

// ChatCompletionResponse represents a non-streaming chat completion response.
type ChatCompletionResponse struct {
	ID                string       `json:"id"`
	Object            string       `json:"object"`
	Created           int64        `json:"created"`
	Model             string       `json:"model"`
	Choices           []ChatChoice `json:"choices"`
	Usage             *Usage       `json:"usage,omitempty"`
	ServiceTier       string       `json:"service_tier,omitempty"`
	SystemFingerprint string       `json:"system_fingerprint,omitempty"`
}

// ChatChoice is a single choice in a non-streaming response.
type ChatChoice struct {
	Index        int             `json:"index"`
	Message      ChatResponseMsg `json:"message"`
	FinishReason *string         `json:"finish_reason"`
	Logprobs     any             `json:"logprobs"`
}

// ChatResponseMsg is the message in a non-streaming response choice.
type ChatResponseMsg struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	Reasoning        any        `json:"reasoning,omitempty"`
	ReasoningSummary string     `json:"reasoning_summary,omitempty"`
	Refusal          string     `json:"refusal,omitempty"`
	Annotations      []any      `json:"annotations,omitempty"`
}

// ChatCompletionChunk represents a streaming chat completion chunk.
type ChatCompletionChunk struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	Created           int64             `json:"created"`
	Model             string            `json:"model"`
	Choices           []ChatChunkChoice `json:"choices"`
	Usage             *Usage            `json:"usage,omitempty"`
	ServiceTier       string            `json:"service_tier,omitempty"`
	SystemFingerprint string            `json:"system_fingerprint,omitempty"`
}

// ChatChunkChoice is a single choice in a streaming chunk.
type ChatChunkChoice struct {
	Index        int       `json:"index"`
	Delta        ChatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
	Logprobs     any       `json:"logprobs"`
}

// ChatDelta holds the delta content in a streaming chunk choice.
type ChatDelta struct {
	Role             string     `json:"role,omitempty"`
	Content          string     `json:"content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	Reasoning        any        `json:"reasoning,omitempty"`
	ReasoningSummary string     `json:"reasoning_summary,omitempty"`
	Refusal          string     `json:"refusal,omitempty"`
}

// ReasoningContent represents o3 compat mode reasoning content.
type ReasoningContent struct {
	Content []ReasoningPart `json:"content"`
}

// ReasoningPart is a single part of reasoning content.
type ReasoningPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// TextCompletionResponse represents a non-streaming text completion response.
type TextCompletionResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []TextChoice `json:"choices"`
	Usage   *Usage       `json:"usage,omitempty"`
}

// TextChoice is a single choice in a text completion response.
type TextChoice struct {
	Index        int     `json:"index"`
	Text         string  `json:"text"`
	FinishReason *string `json:"finish_reason"`
	Logprobs     any     `json:"logprobs"`
}

// TextCompletionChunk represents a streaming text completion chunk.
type TextCompletionChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []TextChunkChoice `json:"choices"`
	Usage   *Usage            `json:"usage,omitempty"`
}

// TextChunkChoice is a single choice in a streaming text chunk.
type TextChunkChoice struct {
	Index        int     `json:"index"`
	Text         string  `json:"text"`
	FinishReason *string `json:"finish_reason"`
}

// Usage holds token usage statistics.
type Usage struct {
	PromptTokens            int64                    `json:"prompt_tokens"`
	CompletionTokens        int64                    `json:"completion_tokens"`
	TotalTokens             int64                    `json:"total_tokens"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
}

// CompletionTokensDetails holds detailed completion token breakdown.
type CompletionTokensDetails struct {
	AcceptedPredictionTokens int64 `json:"accepted_prediction_tokens,omitempty"`
	AudioTokens              int64 `json:"audio_tokens,omitempty"`
	ReasoningTokens          int64 `json:"reasoning_tokens,omitempty"`
	RejectedPredictionTokens int64 `json:"rejected_prediction_tokens,omitempty"`
}

// PromptTokensDetails holds detailed prompt token breakdown.
type PromptTokensDetails struct {
	AudioTokens  int64 `json:"audio_tokens,omitempty"`
	CachedTokens int64 `json:"cached_tokens,omitempty"`
}

// ModelList is the response for GET /v1/models.
type ModelList struct {
	Object string        `json:"object"`
	Data   []ModelObject `json:"data"`
}

// ModelObject represents a single model entry.
type ModelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// ErrorResponse wraps an API error.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail holds the error message.
type ErrorDetail struct {
	Message string `json:"message"`
}
