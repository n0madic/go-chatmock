package types

// OllamaMessage represents a message in the Ollama format.
type OllamaMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// OllamaStreamChunk represents a single Ollama streaming NDJSON chunk.
type OllamaStreamChunk struct {
	Model      string        `json:"model"`
	CreatedAt  string        `json:"created_at"`
	Message    OllamaMessage `json:"message"`
	Done       bool          `json:"done"`
	DoneReason string        `json:"done_reason,omitempty"`
	OllamaFakeEval
}

// OllamaFakeEval holds fake timing fields for Ollama-compatible responses.
type OllamaFakeEval struct {
	TotalDuration      int64 `json:"total_duration,omitempty"`
	LoadDuration       int64 `json:"load_duration,omitempty"`
	PromptEvalCount    int   `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64 `json:"prompt_eval_duration,omitempty"`
	EvalCount          int   `json:"eval_count,omitempty"`
	EvalDuration       int64 `json:"eval_duration,omitempty"`
}

// OllamaFakeEvalDefaults holds default fake timing values.
var OllamaFakeEvalDefaults = OllamaFakeEval{
	TotalDuration:      8497226791,
	LoadDuration:       1747193958,
	PromptEvalCount:    24,
	PromptEvalDuration: 269219750,
	EvalCount:          247,
	EvalDuration:       6413802458,
}

// OllamaModelEntry represents a model in the Ollama tags list.
type OllamaModelEntry struct {
	Name       string             `json:"name"`
	Model      string             `json:"model"`
	ModifiedAt string             `json:"modified_at"`
	Size       int64              `json:"size"`
	Digest     string             `json:"digest"`
	Details    OllamaModelDetails `json:"details"`
}

// OllamaModelDetails holds model metadata.
type OllamaModelDetails struct {
	ParentModel       string   `json:"parent_model"`
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

// OllamaModelList is the response for GET /api/tags.
type OllamaModelList struct {
	Models []OllamaModelEntry `json:"models"`
}

// OllamaShowResponse is the response for POST /api/show.
type OllamaShowResponse struct {
	Modelfile    string             `json:"modelfile"`
	Parameters   string             `json:"parameters"`
	Template     string             `json:"template"`
	Details      OllamaModelDetails `json:"details"`
	ModelInfo    map[string]any     `json:"model_info"`
	Capabilities []string           `json:"capabilities"`
}

// OllamaVersionResponse is the response for GET /api/version.
type OllamaVersionResponse struct {
	Version string `json:"version"`
}
