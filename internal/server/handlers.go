package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n0madic/go-chatmock/internal/codec"
	"github.com/n0madic/go-chatmock/internal/limits"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/reasoning"
	"github.com/n0madic/go-chatmock/internal/stream"
	"github.com/n0madic/go-chatmock/internal/transform"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

// handleTextCompletions handles POST /v1/completions.
func (s *Server) handleTextCompletions(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, s.textEnc)
	if !ok {
		return
	}
	var payload map[string]any
	if err := decodeJSON(body, &payload); err != nil {
		s.textEnc.WriteError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	requestedModel, _ := payload["model"].(string)
	model := models.NormalizeModelName(requestedModel, s.Config.DebugModel)

	if ok, hint := s.Registry.IsKnownModel(model); !ok && s.Config.DebugModel == "" {
		msg := fmt.Sprintf("model %q is not available via this endpoint", model)
		if hint != "" {
			msg += "; available models: " + hint
		}
		s.textEnc.WriteError(w, http.StatusBadRequest, msg)
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
		if sm, ok := ro["summary"].(string); ok {
			reasoningOverrides.Summary = sm
		}
	}
	if reasoningOverrides == nil {
		reasoningOverrides = reasoning.ExtractFromModelName(requestedModel)
	}
	reasoningParam := reasoning.BuildReasoningParam(
		s.Config.ReasoningEffort,
		s.Config.ReasoningSummary,
		reasoningOverrides,
		model,
	)

	if s.Config.Verbose {
		reasoningEffort := ""
		reasoningSummary := ""
		if reasoningParam != nil {
			reasoningEffort = reasoningParam.Effort
			reasoningSummary = reasoningParam.Summary
		}
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
		Store:          types.BoolPtr(false),
		ReasoningParam: reasoningParam,
	}

	resp, err := s.Pipeline.Upstream.Do(r.Context(), upReq)
	if err != nil {
		s.textEnc.WriteError(w, http.StatusUnauthorized, err.Error())
		return
	}
	limits.RecordFromResponse(resp.Headers)

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body.Body)
		resp.Body.Body.Close()
		s.textEnc.WriteError(w, resp.StatusCode, codec.FormatUpstreamError(resp.StatusCode, errBody))
		return
	}

	outputModel := requestedModel
	if outputModel == "" {
		outputModel = model
	}

	if isStream {
		s.textEnc.WriteStreamHeaders(w, resp.StatusCode)
		translator := s.textEnc.StreamTranslator(w, outputModel, codec.StreamOpts{
			IncludeUsage: includeUsage,
		})
		reader := stream.NewReader(resp.Body.Body)
		translator.Translate(reader)
		resp.Body.Body.Close()
		return
	}

	// Non-streaming text completion
	collected := stream.CollectTextFromSSE(resp.Body.Body, stream.CollectOptions{
		InitialResponseID: "cmpl",
		CollectUsage:      true,
	})
	s.textEnc.WriteCollected(w, resp.StatusCode, &codec.CollectedResponse{
		ResponseID: collected.ResponseID,
		FullText:   collected.FullText,
		Usage:      collected.Usage,
	}, outputModel)
}

// handleAnthropicMessages handles POST /v1/messages.
func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if !validateAnthropicHeaders(w, r) {
		return
	}

	body, ok := readBody(w, r, s.anthropicEnc)
	if !ok {
		return
	}
	var req types.AnthropicMessagesRequest
	if err := decodeJSON(body, &req); err != nil {
		codec.WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON body")
		return
	}

	resolvedModel, matchedModel := models.ResolveAnthropicModel(req.Model, models.DefaultAnthropicFallbackModel)
	model := models.NormalizeModelName(resolvedModel, s.Config.DebugModel)
	if s.Config.DebugModel == "" {
		if ok, hint := s.Registry.IsKnownModel(model); !ok {
			msg := fmt.Sprintf("model %q is not available via this endpoint", model)
			if hint != "" {
				msg += "; available models: " + hint
			}
			codec.WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", msg)
			return
		}
	}

	systemText, err := types.ParseSystemText(req.System)
	if err != nil {
		codec.WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	inputItems, err := transform.AnthropicMessagesToResponsesInput(req.Messages)
	if err != nil {
		codec.WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	instructions := strings.TrimSpace(systemText)
	if instructions == "" {
		instructions = strings.TrimSpace(s.Config.InstructionsForModel(model))
	}

	tools := transform.AnthropicToolsToResponses(req.Tools)
	defaultWebSearchApplied := false
	if len(tools) == 0 && s.Config.DefaultWebSearch {
		tools = []types.ResponsesTool{{Type: "web_search"}}
		defaultWebSearchApplied = true
	}

	var reasoningOverrides *types.ReasoningParam
	if effort, ok := models.ResolveAnthropicReasoningEffort(req.Model); ok {
		reasoningOverrides = &types.ReasoningParam{Effort: effort}
	}
	reasoningParam := reasoning.BuildReasoningParam(
		s.Config.ReasoningEffort,
		s.Config.ReasoningSummary,
		reasoningOverrides,
		model,
	)

	if s.Config.Verbose {
		reasoningEffort := ""
		reasoningSummary := ""
		if reasoningParam != nil {
			reasoningEffort = reasoningParam.Effort
			reasoningSummary = reasoningParam.Summary
		}
		slog.Info("anthropic.messages.request",
			"requested_model", req.Model,
			"resolved_model", resolvedModel,
			"upstream_model", model,
			"explicit_match", matchedModel,
			"stream", req.Stream,
			"max_tokens", req.MaxTokens,
			"messages", len(req.Messages),
			"input_items", len(inputItems),
			"tools", len(tools),
			"tool_choice", summarizeToolChoice(req.ToolChoice),
			"system_chars", len(systemText),
			"instructions_chars", len(instructions),
			"default_web_search", defaultWebSearchApplied,
			"reasoning_effort", reasoningEffort,
			"reasoning_summary", reasoningSummary,
			"session_override", strings.TrimSpace(r.Header.Get("X-Session-Id")) != "",
		)
	}

	upReq := &upstream.Request{
		Model:             model,
		Instructions:      instructions,
		InputItems:        inputItems,
		Tools:             tools,
		ToolChoice:        transform.AnthropicToolChoiceToResponses(req.ToolChoice),
		ParallelToolCalls: false,
		Store:             types.BoolPtr(false),
		ReasoningParam:    reasoningParam,
		SessionID:         r.Header.Get("X-Session-Id"),
	}

	resp, err := s.Pipeline.Upstream.Do(r.Context(), upReq)
	if err != nil {
		codec.WriteAnthropicError(w, http.StatusUnauthorized, "authentication_error", err.Error())
		return
	}
	limits.RecordFromResponse(resp.Headers)

	if resp.StatusCode >= 400 {
		warnMsg := ""
		if s.Config.Verbose {
			warnMsg = "upstream rejected store parameter on anthropic route; retrying without store"
		}
		_ = warnMsg
		resp2, errBody, retried, retryErr := s.Pipeline.Upstream.RetryIfStoreUnsupported(r.Context(), resp, upReq)
		if retried {
			if retryErr != nil {
				codec.WriteAnthropicError(w, http.StatusBadGateway, "api_error", "Upstream retry failed after removing store: "+retryErr.Error())
				return
			}
			if resp2.StatusCode >= 400 {
				errBody2, _ := io.ReadAll(resp2.Body.Body)
				resp2.Body.Body.Close()
				msg := codec.ExtractUpstreamErrorMessage(errBody2)
				if msg == "" {
					msg = codec.FormatUpstreamError(resp2.StatusCode, errBody2)
				}
				codec.WriteAnthropicError(w, resp2.StatusCode, "api_error", msg)
				return
			}
			resp = resp2
		} else {
			msg := codec.ExtractUpstreamErrorMessage(errBody)
			if msg == "" {
				msg = codec.FormatUpstreamErrorWithHeaders(resp.StatusCode, errBody, resp.Headers)
			}
			codec.WriteAnthropicError(w, resp.StatusCode, "api_error", msg)
			return
		}
	}

	outputModel := strings.TrimSpace(req.Model)
	if outputModel == "" {
		outputModel = model
	}

	if req.Stream {
		s.anthropicEnc.WriteStreamHeaders(w, resp.StatusCode)
		translator := s.anthropicEnc.StreamTranslator(w, outputModel, codec.StreamOpts{})
		reader := stream.NewReader(resp.Body.Body)
		translator.Translate(reader)
		resp.Body.Body.Close()
		return
	}

	// Non-streaming anthropic - collect through SSE
	collected := collectAnthropicResponse(resp.Body.Body)
	s.anthropicEnc.WriteCollected(w, resp.StatusCode, collected, outputModel)
}

// handleOllamaChat handles POST /api/chat.
func (s *Server) handleOllamaChat(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, s.ollamaEnc)
	if !ok {
		return
	}
	var payload map[string]any
	if err := decodeJSON(body, &payload); err != nil {
		s.ollamaEnc.WriteError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	modelName, _ := payload["model"].(string)
	rawMsgs, _ := payload["messages"].([]any)
	var topImages []string
	if imgs, ok := payload["images"].([]any); ok {
		for _, img := range imgs {
			if s, ok := img.(string); ok {
				topImages = append(topImages, s)
			}
		}
	}
	messages := transform.ConvertOllamaMessages(rawMsgs, topImages)
	convertSystemToUser(messages)

	streamReq := true
	if v, ok := payload["stream"].(bool); ok {
		streamReq = v
	}

	toolsRaw, _ := payload["tools"].([]any)
	normalizedTools := transform.NormalizeOllamaTools(toolsRaw)
	toolsResponses := transform.ToolsChatToResponses(normalizedTools)
	toolChoice := "auto"
	if tc, ok := payload["tool_choice"].(string); ok {
		toolChoice = tc
	}
	parallelToolCalls := boolVal(payload, "parallel_tool_calls")

	if modelName == "" || len(messages) == 0 {
		s.ollamaEnc.WriteError(w, http.StatusBadRequest, "Invalid request format")
		return
	}

	inputItems := transform.ChatMessagesToResponsesInput(messages)
	normalizedModel := models.NormalizeModelName(modelName, s.Config.DebugModel)

	if ok, hint := s.Registry.IsKnownModel(normalizedModel); !ok && s.Config.DebugModel == "" {
		msg := fmt.Sprintf("model %q is not available via this endpoint", normalizedModel)
		if hint != "" {
			msg += "; available models: " + hint
		}
		s.ollamaEnc.WriteError(w, http.StatusBadRequest, msg)
		return
	}

	reasoningParam := reasoning.BuildReasoningParam(
		s.Config.ReasoningEffort,
		s.Config.ReasoningSummary,
		reasoning.ExtractFromModelName(modelName),
		normalizedModel,
	)

	if s.Config.Verbose {
		reasoningEffort := ""
		reasoningSummary := ""
		if reasoningParam != nil {
			reasoningEffort = reasoningParam.Effort
			reasoningSummary = reasoningParam.Summary
		}
		slog.Info("ollama.chat.request",
			"requested_model", modelName,
			"upstream_model", normalizedModel,
			"stream", streamReq,
			"messages", len(rawMsgs),
			"input_items", len(inputItems),
			"images", len(topImages),
			"tools", len(toolsResponses),
			"tool_choice", summarizeToolChoice(toolChoice),
			"parallel_tool_calls", parallelToolCalls,
			"reasoning_effort", reasoningEffort,
			"reasoning_summary", reasoningSummary,
			"session_override", strings.TrimSpace(r.Header.Get("X-Session-Id")) != "",
		)
	}

	upReq := &upstream.Request{
		Model:             normalizedModel,
		Instructions:      s.Config.InstructionsForModel(normalizedModel),
		InputItems:        inputItems,
		Tools:             toolsResponses,
		ToolChoice:        toolChoice,
		ParallelToolCalls: parallelToolCalls,
		ReasoningParam:    reasoningParam,
		SessionID:         r.Header.Get("X-Session-Id"),
	}

	baseTools := transform.ToolsChatToResponses(normalizedTools)
	resp, upErr := s.Pipeline.Upstream.DoWithRetry(r.Context(), upReq, false, baseTools)
	if upErr != nil {
		s.ollamaEnc.WriteError(w, upErr.StatusCode, upErr.Error())
		return
	}

	createdAt := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	if streamReq {
		s.ollamaEnc.WriteStreamHeaders(w, resp.StatusCode)
		translator := s.ollamaEnc.StreamTranslator(w, modelName, codec.StreamOpts{
			ReasoningCompat: s.Config.ReasoningCompat,
			CreatedAt:       createdAt,
		})
		reader := stream.NewReader(resp.Body.Body)
		translator.Translate(reader)
		resp.Body.Body.Close()
		return
	}

	// Non-streaming ollama
	collected := stream.CollectTextFromSSE(resp.Body.Body, stream.CollectOptions{
		CollectReasoning: true,
		CollectToolCalls: true,
	})
	s.ollamaEnc.WriteCollected(w, http.StatusOK, &codec.CollectedResponse{
		ResponseID:       collected.ResponseID,
		FullText:         collected.FullText,
		ReasoningSummary: collected.ReasoningSummary,
		ReasoningFull:    collected.ReasoningFull,
		ToolCalls:        collected.ToolCalls,
		RawResponse: map[string]any{
			"_reasoning_compat": s.Config.ReasoningCompat,
			"_created_at":       createdAt,
		},
	}, modelName)
}

// --- helpers ---

func decodeJSON(body []byte, dst any) error {
	return json.Unmarshal(body, dst)
}

func boolVal(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func summarizeToolChoice(choice any) string {
	switch v := choice.(type) {
	case nil:
		return "auto"
	case string:
		val := strings.TrimSpace(v)
		if val == "" {
			return "auto"
		}
		return val
	case map[string]any:
		kind, _ := v["type"].(string)
		if fn, ok := v["function"].(map[string]any); ok {
			if name, _ := fn["name"].(string); name != "" {
				if kind != "" {
					return kind + ":" + name
				}
				return "function:" + name
			}
		}
		if kind != "" {
			return kind
		}
		return "object"
	default:
		return "auto"
	}
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

// collectAnthropicResponse collects a non-streaming anthropic response from SSE.
func collectAnthropicResponse(body io.ReadCloser) *codec.CollectedResponse {
	collected := stream.CollectTextFromSSE(body, stream.CollectOptions{
		InitialResponseID: "msg_chatmock",
		CollectUsage:      true,
		CollectToolCalls:  true,
		StopOnFailed:      true,
	})
	return &codec.CollectedResponse{
		ResponseID:   collected.ResponseID,
		FullText:     collected.FullText,
		ToolCalls:    collected.ToolCalls,
		Usage:        collected.Usage,
		ErrorMessage: collected.ErrorMessage,
	}
}

// Wrappers for transform functions used in models.go
func transformAnthropicMessagesToResponsesInput(messages []types.AnthropicMessage) ([]types.ResponsesInputItem, error) {
	return transform.AnthropicMessagesToResponsesInput(messages)
}

func transformAnthropicToolsToResponses(tools []types.AnthropicTool) []types.ResponsesTool {
	return transform.AnthropicToolsToResponses(tools)
}

func estimateResponsesInputTokens(instructions string, input []types.ResponsesInputItem, tools []types.ResponsesTool) int {
	return transform.EstimateResponsesInputTokens(instructions, input, tools)
}
