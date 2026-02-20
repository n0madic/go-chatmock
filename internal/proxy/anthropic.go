package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	anthropicutil "github.com/n0madic/go-chatmock/internal/anthropic"
	"github.com/n0madic/go-chatmock/internal/limits"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/reasoning"
	"github.com/n0madic/go-chatmock/internal/sse"
	"github.com/n0madic/go-chatmock/internal/transform"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

const anthropicModelCreatedAt = "2024-01-01T00:00:00Z"

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if !validateAnthropicHeaders(w, r) {
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	var req types.AnthropicMessagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON body")
		return
	}

	resolvedModel, matchedModel := models.ResolveAnthropicModel(req.Model, models.DefaultAnthropicFallbackModel)
	model := models.NormalizeModelName(resolvedModel, s.Config.DebugModel)
	if s.Config.DebugModel == "" {
		ok, hint := s.Registry.IsKnownModel(model)
		if !ok {
			msg := fmt.Sprintf("model %q is not available via this endpoint", model)
			if hint != "" {
				msg += "; available models: " + hint
			}
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", msg)
			return
		}
	}

	systemText, err := types.ParseSystemText(req.System)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	inputItems, err := transform.AnthropicMessagesToResponsesInput(req.Messages)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	// Anthropic compatibility: prefer client-provided system prompt as the
	// primary instruction source to avoid conflicting tool-use policies.
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

	reasoningParam := reasoning.BuildReasoningParam(
		s.Config.ReasoningEffort,
		s.Config.ReasoningSummary,
		nil,
		model,
	)
	reasoningEffort := ""
	reasoningSummary := ""
	if reasoningParam != nil {
		reasoningEffort = reasoningParam.Effort
		reasoningSummary = reasoningParam.Summary
	}
	if s.Config.Verbose {
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

	resp, err := s.upstreamClient.Do(r.Context(), upReq)
	if err != nil {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", err.Error())
		return
	}
	limits.RecordFromResponse(resp.Headers)

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body.Body)
		resp.Body.Body.Close()
		if upReq.Store != nil && isUnsupportedParameterError(errBody, "store") {
			if s.Config.Verbose {
				slog.Warn("upstream rejected store parameter on anthropic route; retrying without store")
			}
			upReq.Store = nil
			resp2, err2 := s.upstreamClient.Do(r.Context(), upReq)
			if err2 != nil {
				writeAnthropicError(w, http.StatusBadGateway, "api_error", "Upstream retry failed after removing store: "+err2.Error())
				return
			}
			limits.RecordFromResponse(resp2.Headers)
			if resp2.StatusCode >= 400 {
				errBody2, _ := io.ReadAll(resp2.Body.Body)
				resp2.Body.Body.Close()
				msg := extractUpstreamErrorMessage(errBody2)
				if msg == "" {
					msg = formatUpstreamError(resp2.StatusCode, errBody2)
				}
				writeAnthropicError(w, resp2.StatusCode, "api_error", msg)
				return
			}
			resp = resp2
		} else {
			msg := extractUpstreamErrorMessage(errBody)
			if msg == "" {
				msg = formatUpstreamErrorWithHeaders(resp.StatusCode, errBody, resp.Headers)
			}
			writeAnthropicError(w, resp.StatusCode, "api_error", msg)
			return
		}
	}

	outputModel := strings.TrimSpace(req.Model)
	if outputModel == "" {
		outputModel = model
	}

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(resp.StatusCode)
		sse.TranslateAnthropic(w, resp.Body.Body, outputModel)
		return
	}

	s.collectAnthropicMessage(w, resp, outputModel)
}

func (s *Server) collectAnthropicMessage(w http.ResponseWriter, resp *upstream.Response, outputModel string) {
	defer resp.Body.Body.Close()

	reader := sse.NewReader(resp.Body.Body)
	responseID := "msg_chatmock"
	var textBuilder strings.Builder
	var content []types.AnthropicContentOut
	var usageObj types.AnthropicUsage
	var sawToolUse bool
	var errorMessage string
	toolArgs := map[string]any{}
	toolArgDeltas := map[string]string{}

	flushText := func() {
		if textBuilder.Len() == 0 {
			return
		}
		content = append(content, types.AnthropicContentOut{
			Type: "text",
			Text: textBuilder.String(),
		})
		textBuilder.Reset()
	}

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		if id := responseIDFromEvent(evt.Data); id != "" {
			responseID = id
		}
		if usage := types.ExtractUsageFromEvent(evt.Data); usage != nil {
			usageObj = types.AnthropicUsage{
				InputTokens:  usage.PromptTokens,
				OutputTokens: usage.CompletionTokens,
			}
		}

		switch evt.Type {
		case "response.output_item.added":
			item, _ := evt.Data["item"].(map[string]any)
			itemType, _ := item["type"].(string)
			if itemType == "function_call" {
				if rawArgs, ok := anthropicutil.ExtractToolInputFromMap(item); ok {
					for _, key := range anthropicutil.FunctionCallItemKeys(item) {
						toolArgs[key] = rawArgs
					}
				}
			}

		case "response.function_call_arguments.delta":
			itemID := firstMapString(evt.Data, "item_id", "call_id", "id")
			delta, _ := evt.Data["delta"].(string)
			if itemID != "" && delta != "" {
				toolArgDeltas[itemID] += delta
			}

		case "response.function_call_arguments.done":
			itemID := firstMapString(evt.Data, "item_id", "call_id", "id")
			if itemID != "" {
				if rawArgs, ok := anthropicutil.ExtractToolInputFromMap(evt.Data); ok {
					toolArgs[itemID] = rawArgs
				}
			}

		case "response.output_text.delta":
			delta, _ := evt.Data["delta"].(string)
			textBuilder.WriteString(delta)

		case "response.output_item.done":
			item, _ := evt.Data["item"].(map[string]any)
			itemType, _ := item["type"].(string)
			if itemType != "function_call" {
				continue
			}
			flushText()
			callID := stringOrEmpty(item, "call_id")
			if callID == "" {
				callID = stringOrEmpty(item, "id")
			}
			name := stringOrEmpty(item, "name")
			rawArgs, _ := anthropicutil.ExtractToolInputFromMap(item)
			if rawArgs == nil {
				rawArgs = anthropicutil.BufferedToolInput(anthropicutil.FunctionCallItemKeys(item), toolArgs, toolArgDeltas)
			}
			content = append(content, types.AnthropicContentOut{
				Type:  "tool_use",
				ID:    callID,
				Name:  name,
				Input: parseAnthropicToolInputAny(rawArgs),
			})
			sawToolUse = true

		case "response.failed":
			if r, ok := evt.Data["response"].(map[string]any); ok {
				if e, ok := r["error"].(map[string]any); ok {
					errorMessage, _ = e["message"].(string)
				}
			}
			if errorMessage == "" {
				errorMessage = "response.failed"
			}
			goto done

		case "response.completed":
			goto done
		}
	}

done:
	if errorMessage != "" {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", errorMessage)
		return
	}

	flushText()

	stopReason := "end_turn"
	if sawToolUse {
		stopReason = "tool_use"
	}

	result := types.AnthropicMessageResponse{
		ID:           responseID,
		Type:         "message",
		Role:         "assistant",
		Model:        outputModel,
		Content:      content,
		StopReason:   types.StringPtr(stopReason),
		StopSequence: nil,
		Usage:        usageObj,
	}

	writeJSON(w, resp.StatusCode, result)
}

func (s *Server) handleAnthropicCountTokens(w http.ResponseWriter, r *http.Request) {
	if !validateAnthropicHeaders(w, r) {
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	var req types.AnthropicCountTokensRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON body")
		return
	}

	systemText, err := types.ParseSystemText(req.System)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	inputItems, err := transform.AnthropicMessagesToResponsesInput(req.Messages)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	tools := transform.AnthropicToolsToResponses(req.Tools)

	count := transform.EstimateResponsesInputTokens(systemText, inputItems, tools)
	writeJSON(w, http.StatusOK, types.AnthropicCountTokensResponse{InputTokens: count})
}

func (s *Server) handleListModelsAnthropic(w http.ResponseWriter, r *http.Request) {
	if !validateAnthropicHeaders(w, r) {
		return
	}

	mods := s.Registry.GetModels()
	data := make([]types.AnthropicModel, 0, len(mods))
	for _, m := range mods {
		if m.Visibility == "hidden" {
			continue
		}
		data = append(data, types.AnthropicModel{
			ID:          m.Slug,
			Type:        "model",
			DisplayName: firstNonEmpty(m.DisplayName, m.Slug),
			CreatedAt:   anthropicModelCreatedAt,
		})
		if s.Config.ExposeReasoningModels {
			for _, lvl := range m.SupportedReasoningLevels {
				id := m.Slug + "-" + lvl.Effort
				data = append(data, types.AnthropicModel{
					ID:          id,
					Type:        "model",
					DisplayName: id,
					CreatedAt:   anthropicModelCreatedAt,
				})
			}
		}
	}

	resp := types.AnthropicModelListResponse{
		Data:    data,
		HasMore: false,
	}
	if len(data) > 0 {
		resp.FirstID = data[0].ID
		resp.LastID = data[len(data)-1].ID
	}
	writeJSON(w, http.StatusOK, resp)
}

func isAnthropicRequest(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("anthropic-version")) != "" ||
		strings.TrimSpace(r.Header.Get("anthropic-beta")) != ""
}

func validateAnthropicHeaders(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("anthropic-version")) == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Missing required header: anthropic-version")
		return false
	}
	if !hasAnthropicAuthHeader(r) {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Missing auth header: provide x-api-key or Authorization")
		return false
	}
	return true
}

func hasAnthropicAuthHeader(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("x-api-key")) != "" {
		return true
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return true
	}
	if strings.TrimSpace(r.Header.Get("Proxy-Authorization")) != "" {
		return true
	}
	return false
}

func writeAnthropicError(w http.ResponseWriter, status int, errorType, message string) {
	if strings.TrimSpace(errorType) == "" {
		errorType = "api_error"
	}
	if strings.TrimSpace(message) == "" {
		message = http.StatusText(status)
	}
	writeJSON(w, status, types.AnthropicErrorResponse{
		Type: "error",
		Error: types.AnthropicErrorBody{
			Type:    errorType,
			Message: message,
		},
	})
}

func parseAnthropicToolInputAny(raw any) any {
	switch v := raw.(type) {
	case map[string]any:
		return v
	case []any:
		return v
	case string:
		arguments := strings.TrimSpace(v)
		if arguments == "" {
			return map[string]any{}
		}
		var parsed any
		if err := json.Unmarshal([]byte(arguments), &parsed); err == nil {
			return parsed
		}
		return map[string]any{"raw": arguments}
	default:
		if raw == nil {
			return map[string]any{}
		}
		if b, err := json.Marshal(raw); err == nil {
			var parsed any
			if err := json.Unmarshal(b, &parsed); err == nil {
				return parsed
			}
			return map[string]any{"raw": string(b)}
		}
		return map[string]any{}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}
