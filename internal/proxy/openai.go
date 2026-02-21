package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n0madic/go-chatmock/internal/limits"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/reasoning"
	"github.com/n0madic/go-chatmock/internal/sse"
	"github.com/n0madic/go-chatmock/internal/transform"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, ok := readLimitedRequestBody(w, r, writeError, "Failed to read request body")
	if !ok {
		return
	}

	var req types.ChatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		// Try stripping newlines
		cleaned := strings.ReplaceAll(strings.ReplaceAll(string(body), "\r", ""), "\n", "")
		if err := json.Unmarshal([]byte(cleaned), &req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
	}

	requestedModel := req.Model
	model := models.NormalizeModelName(requestedModel, s.Config.DebugModel)

	if !s.validateModel(w, model) {
		return
	}

	messages := req.Messages
	usedPromptFallback := false
	usedInputFallback := false
	if messages == nil {
		// Try prompt or input fallback
		if req.Prompt != "" {
			messages = []types.ChatMessage{{Role: "user", Content: req.Prompt}}
			usedPromptFallback = true
		} else {
			// Try raw payload for "input" field
			var raw map[string]any
			json.Unmarshal(body, &raw)
			if input, ok := raw["input"].(string); ok {
				messages = []types.ChatMessage{{Role: "user", Content: input}}
				usedInputFallback = true
			}
		}
		if messages == nil {
			writeError(w, http.StatusBadRequest, "Request must include messages: []")
			return
		}
	}

	// Convert system message to user message
	convertSystemToUser(messages)

	isStream := req.Stream
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage

	// Tools
	toolsResponses := transform.ToolsChatToResponses(req.Tools)
	toolChoice := req.ToolChoice
	if toolChoice == nil {
		toolChoice = "auto"
	}
	parallelToolCalls := req.ParallelToolCalls

	// Passthrough responses_tools (web_search)
	extraTools, hadResponsesTools := s.extractResponsesTools(req.ResponsesTools, req.ResponsesToolChoice)
	if extraTools == nil && hadResponsesTools {
		writeError(w, http.StatusBadRequest, "Only web_search/web_search_preview are supported in responses_tools")
		return
	}
	if len(extraTools) > 0 {
		toolsResponses = append(toolsResponses, extraTools...)
	}
	defaultWebSearchApplied := req.ResponsesTools == nil && len(extraTools) > 0 && s.Config.DefaultWebSearch && req.ResponsesToolChoice != "none"

	if rtc := req.ResponsesToolChoice; rtc == "auto" || rtc == "none" {
		toolChoice = rtc
	}

	inputItems := transform.ChatMessagesToResponsesInput(messages)
	if len(inputItems) == 0 {
		if req.Prompt != "" {
			inputItems = []types.ResponsesInputItem{
				{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: req.Prompt}}},
			}
		}
	}

	reasoningParam := buildReasoningWithModelFallback(s.Config, requestedModel, model, req.Reasoning)
	reasoningEffort, reasoningSummary := reasoningLogFields(reasoningParam)
	if s.Config.Verbose {
		slog.Info("openai.chat.request",
			"requested_model", requestedModel,
			"upstream_model", model,
			"stream", isStream,
			"include_usage", includeUsage,
			"messages", len(messages),
			"input_items", len(inputItems),
			"tools", len(toolsResponses),
			"tool_choice", summarizeToolChoice(toolChoice),
			"responses_tools", len(extraTools),
			"default_web_search", defaultWebSearchApplied,
			"parallel_tool_calls", parallelToolCalls,
			"reasoning_effort", reasoningEffort,
			"reasoning_summary", reasoningSummary,
			"prompt_fallback", usedPromptFallback,
			"input_fallback", usedInputFallback,
			"session_override", strings.TrimSpace(r.Header.Get("X-Session-Id")) != "",
		)
	}

	upReq := &upstream.Request{
		Model:             model,
		Instructions:      s.Config.InstructionsForModel(model),
		InputItems:        inputItems,
		Tools:             toolsResponses,
		ToolChoice:        toolChoice,
		ParallelToolCalls: parallelToolCalls,
		ReasoningParam:    reasoningParam,
		SessionID:         r.Header.Get("X-Session-Id"),
	}

	baseTools := transform.ToolsChatToResponses(req.Tools)
	resp, ok := s.doUpstreamWithResponsesToolsRetry(r.Context(), w, upReq, hadResponsesTools, baseTools, writeError)
	if !ok {
		return
	}

	created := time.Now().Unix()
	outputModel := requestedModel
	if outputModel == "" {
		outputModel = model
	}

	if isStream {
		writeSSEHeaders(w, resp.StatusCode)
		sse.TranslateChat(w, resp.Body.Body, outputModel, created, sse.TranslateChatOptions{
			ReasoningCompat: s.Config.ReasoningCompat,
			IncludeUsage:    includeUsage,
		})
		return
	}

	// Non-streaming: collect full response
	s.collectChatCompletion(w, resp, outputModel, created)
}

func (s *Server) collectChatCompletion(w http.ResponseWriter, resp *upstream.Response, model string, created int64) {
	collected := collectTextResponseFromSSE(resp.Body.Body, collectTextResponseOptions{
		InitialResponseID: "chatcmpl",
		CollectUsage:      true,
		CollectReasoning:  true,
		CollectToolCalls:  true,
		StopOnFailed:      true,
	})

	if collected.ErrorMessage != "" {
		writeError(w, http.StatusBadGateway, collected.ErrorMessage)
		return
	}

	message := types.ChatResponseMsg{Role: "assistant", Content: collected.FullText}
	if len(collected.ToolCalls) > 0 {
		message.ToolCalls = collected.ToolCalls
	}
	reasoning.ApplyReasoningToMessage(&message, collected.ReasoningSummary, collected.ReasoningFull, s.Config.ReasoningCompat)

	completion := types.ChatCompletionResponse{
		ID:      collected.ResponseID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []types.ChatChoice{
			{Index: 0, Message: message, FinishReason: types.StringPtr("stop")},
		},
		Usage: collected.Usage,
	}

	writeJSON(w, resp.StatusCode, completion)
}

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if _, ok := parseJSONRequest(w, r, &payload, writeError, "Failed to read request body", "Invalid JSON body"); !ok {
		return
	}

	requestedModel, _ := payload["model"].(string)
	model := models.NormalizeModelName(requestedModel, s.Config.DebugModel)

	if !s.validateModel(w, model) {
		return
	}

	prompt, _ := payload["prompt"].(string)
	if prompt == "" {
		if prompts, ok := payload["prompt"].([]any); ok {
			var parts []string
			for _, p := range prompts {
				if s, ok := p.(string); ok {
					parts = append(parts, s)
				}
			}
			prompt = strings.Join(parts, "")
		}
	}
	if prompt == "" {
		prompt, _ = payload["suffix"].(string)
	}

	isStream := boolVal(payload, "stream")
	streamOpts, _ := payload["stream_options"].(map[string]any)
	includeUsage := streamOpts != nil && boolVal(streamOpts, "include_usage")

	messages := []types.ChatMessage{{Role: "user", Content: prompt}}
	inputItems := transform.ChatMessagesToResponsesInput(messages)

	var reasoningOverrides *types.ReasoningParam
	if ro, ok := payload["reasoning"].(map[string]any); ok {
		reasoningOverrides = &types.ReasoningParam{}
		if e, ok := ro["effort"].(string); ok {
			reasoningOverrides.Effort = e
		}
		if s, ok := ro["summary"].(string); ok {
			reasoningOverrides.Summary = s
		}
	}
	reasoningParam := buildReasoningWithModelFallback(s.Config, requestedModel, model, reasoningOverrides)
	reasoningEffort, reasoningSummary := reasoningLogFields(reasoningParam)
	if s.Config.Verbose {
		slog.Info("openai.completions.request",
			"requested_model", requestedModel,
			"upstream_model", model,
			"stream", isStream,
			"include_usage", includeUsage,
			"prompt_chars", len(prompt),
			"input_items", len(inputItems),
			"reasoning_effort", reasoningEffort,
			"reasoning_summary", reasoningSummary,
		)
	}

	upReq := &upstream.Request{
		Model:          model,
		Instructions:   s.Config.InstructionsForModel(model),
		InputItems:     inputItems,
		ReasoningParam: reasoningParam,
	}

	resp, err := s.upstreamClient.Do(r.Context(), upReq)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	limits.RecordFromResponse(resp.Headers)

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body.Body)
		resp.Body.Body.Close()
		writeError(w, resp.StatusCode, formatUpstreamError(resp.StatusCode, errBody))
		return
	}

	created := time.Now().Unix()
	outputModel := requestedModel
	if outputModel == "" {
		outputModel = model
	}

	if isStream {
		writeSSEHeaders(w, resp.StatusCode)
		sse.TranslateText(w, resp.Body.Body, outputModel, created, sse.TranslateTextOptions{
			IncludeUsage: includeUsage,
		})
		return
	}

	// Non-streaming
	collected := collectTextResponseFromSSE(resp.Body.Body, collectTextResponseOptions{
		InitialResponseID: "cmpl",
		CollectUsage:      true,
	})
	completion := types.TextCompletionResponse{
		ID:      collected.ResponseID,
		Object:  "text_completion",
		Created: created,
		Model:   outputModel,
		Choices: []types.TextChoice{
			{Index: 0, Text: collected.FullText, FinishReason: types.StringPtr("stop"), Logprobs: nil},
		},
		Usage: collected.Usage,
	}
	writeJSON(w, resp.StatusCode, completion)
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	if isAnthropicRequest(r) {
		s.handleListModelsAnthropic(w, r)
		return
	}

	mods := s.Registry.GetModels()
	var data []types.ModelObject
	for _, m := range mods {
		if m.Visibility == "hidden" {
			continue
		}
		data = append(data, types.ModelObject{ID: m.Slug, Object: "model", OwnedBy: "owner"})
		if s.Config.ExposeReasoningModels {
			for _, lvl := range m.SupportedReasoningLevels {
				data = append(data, types.ModelObject{
					ID:      m.Slug + "-" + lvl.Effort,
					Object:  "model",
					OwnedBy: "owner",
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, types.ModelList{Object: "list", Data: data})
}

func (s *Server) extractResponsesTools(responsesTools []any, responsesToolChoice string) ([]types.ResponsesTool, bool) {
	if responsesTools == nil {
		if s.Config.DefaultWebSearch {
			if responsesToolChoice != "none" {
				return []types.ResponsesTool{{Type: "web_search"}}, true
			}
		}
		return nil, false
	}

	var extraTools []types.ResponsesTool
	for _, t := range responsesTools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		ttype, _ := tm["type"].(string)
		if ttype != "web_search" && ttype != "web_search_preview" {
			return nil, true // signal error
		}
		extraTools = append(extraTools, types.ResponsesTool{Type: ttype})
	}

	if len(extraTools) == 0 && s.Config.DefaultWebSearch {
		if responsesToolChoice != "none" {
			extraTools = []types.ResponsesTool{{Type: "web_search"}}
		}
	}

	return extraTools, len(extraTools) > 0
}

func convertSystemToUser(messages []types.ChatMessage) {
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

func boolVal(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}
