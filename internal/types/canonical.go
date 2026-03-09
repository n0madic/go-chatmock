package types

// CanonicalRequest is the unified internal representation of any API request
// after decoding. It is format-agnostic and carries all information needed
// for normalization, upstream dispatch, and response encoding.
type CanonicalRequest struct {
	// Format tracks the response format, derived from the route (endpoint).
	ResponseFormat string // "chat", "responses", "text", "anthropic", "ollama"

	// Model fields
	RequestedModel string // original model string from the client
	Model          string // normalized model for upstream

	// Streaming
	Stream       bool
	IncludeUsage bool

	// Input
	InputItems    []ResponsesInputItem
	Instructions  string
	InputSource   string // "messages", "input", "prompt"
	MessagesCount int

	// Tools
	Tools             []ResponsesTool
	BaseTools         []ResponsesTool // tools before responses_tools additions
	HadResponsesTools bool
	ToolChoice        any
	ParallelToolCalls bool

	// Responses API fields
	PreviousResponseID     string
	ConversationID         string
	AutoPreviousResponseID bool
	Include                []string

	// Reasoning
	ReasoningParam  *ReasoningParam
	ReasoningCompat string

	// Store
	StoreRequested   *bool
	StoreForUpstream *bool
	StoreForced      bool

	// Session
	SessionID string

	// Diagnostics
	UsedPromptFallback      bool
	UsedInputFallback       bool
	DefaultWebSearchApplied bool
}
