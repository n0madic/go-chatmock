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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}

	if s.Config.Verbose {
		slog.Info("IN POST /v1/chat/completions", "body", string(body))
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
	if messages == nil {
		// Try prompt or input fallback
		if req.Prompt != "" {
			messages = []types.ChatMessage{{Role: "user", Content: req.Prompt}}
		} else {
			// Try raw payload for "input" field
			var raw map[string]any
			json.Unmarshal(body, &raw)
			if input, ok := raw["input"].(string); ok {
				messages = []types.ChatMessage{{Role: "user", Content: input}}
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

	modelReasoning := reasoning.ExtractFromModelName(requestedModel)
	reasoningOverrides := req.Reasoning
	if reasoningOverrides == nil {
		reasoningOverrides = modelReasoning
	}
	reasoningParam := reasoning.BuildReasoningParam(
		s.Config.ReasoningEffort,
		s.Config.ReasoningSummary,
		reasoningOverrides,
		model,
	)

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

	resp, err := s.upstreamClient.Do(r.Context(), upReq)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	limits.RecordFromResponse(resp.Headers)

	if resp.StatusCode >= 400 {
		if hadResponsesTools {
			// Retry without extra tools
			baseTools := transform.ToolsChatToResponses(req.Tools)
			upReq.Tools = baseTools
			resp2, err2 := s.upstreamClient.Do(r.Context(), upReq)
			if err2 == nil && resp2.StatusCode < 400 {
				limits.RecordFromResponse(resp2.Headers)
				resp.Body.Body.Close()
				resp = resp2
			} else {
				resp.Body.Body.Close()
				if resp2 != nil {
					resp2.Body.Body.Close()
				}
				writeError(w, resp.StatusCode, "Upstream error")
				return
			}
		} else {
			errBody, _ := io.ReadAll(resp.Body.Body)
			resp.Body.Body.Close()
			var errResp types.ErrorResponse
			if json.Unmarshal(errBody, &errResp) == nil && errResp.Error.Message != "" {
				writeError(w, resp.StatusCode, errResp.Error.Message)
				return
			}
			writeError(w, resp.StatusCode, "Upstream error")
			return
		}
	}

	created := time.Now().Unix()
	outputModel := requestedModel
	if outputModel == "" {
		outputModel = model
	}

	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(resp.StatusCode)
		sse.TranslateChat(w, resp.Body.Body, outputModel, created, sse.TranslateChatOptions{
			ReasoningCompat: s.Config.ReasoningCompat,
			Verbose:         s.Config.VerboseObfuscation,
			IncludeUsage:    includeUsage,
		})
		return
	}

	// Non-streaming: collect full response
	s.collectChatCompletion(w, resp, outputModel, created)
}

func (s *Server) collectChatCompletion(w http.ResponseWriter, resp *upstream.Response, model string, created int64) {
	defer resp.Body.Body.Close()

	reader := sse.NewReader(resp.Body.Body)
	var fullText string
	var reasoningSummaryText string
	var reasoningFullText string
	responseID := "chatcmpl"
	var toolCalls []types.ToolCall
	var errorMessage string
	var usageObj *types.Usage

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		if r, ok := evt.Data["response"].(map[string]any); ok {
			if id, ok := r["id"].(string); ok && id != "" {
				responseID = id
			}
		}

		usageData := extractUsageFromEvent(evt.Data)
		if usageData != nil {
			usageObj = usageData
		}

		switch evt.Type {
		case "response.output_text.delta":
			delta, _ := evt.Data["delta"].(string)
			fullText += delta
		case "response.reasoning_summary_text.delta":
			delta, _ := evt.Data["delta"].(string)
			reasoningSummaryText += delta
		case "response.reasoning_text.delta":
			delta, _ := evt.Data["delta"].(string)
			reasoningFullText += delta
		case "response.output_item.done":
			item, _ := evt.Data["item"].(map[string]any)
			if itemType, _ := item["type"].(string); itemType == "function_call" {
				callID := stringOrEmpty(item, "call_id")
				if callID == "" {
					callID = stringOrEmpty(item, "id")
				}
				name := stringOrEmpty(item, "name")
				args := stringOrEmpty(item, "arguments")
				if callID != "" && name != "" {
					toolCalls = append(toolCalls, types.ToolCall{
						ID: callID, Type: "function",
						Function: types.FunctionCall{Name: name, Arguments: args},
					})
				}
			}
		case "response.failed":
			if r, ok := evt.Data["response"].(map[string]any); ok {
				if e, ok := r["error"].(map[string]any); ok {
					errorMessage, _ = e["message"].(string)
				}
			}
			if errorMessage == "" {
				errorMessage = "response.failed"
			}
		case "response.completed":
			goto done
		}
	}

done:
	if errorMessage != "" {
		writeError(w, http.StatusBadGateway, errorMessage)
		return
	}

	message := types.ChatResponseMsg{Role: "assistant", Content: fullText}
	if len(toolCalls) > 0 {
		message.ToolCalls = toolCalls
	}
	reasoning.ApplyReasoningToMessage(&message, reasoningSummaryText, reasoningFullText, s.Config.ReasoningCompat)

	completion := types.ChatCompletionResponse{
		ID:      responseID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []types.ChatChoice{
			{Index: 0, Message: message, FinishReason: types.StringPtr("stop")},
		},
		Usage: usageObj,
	}

	writeJSON(w, resp.StatusCode, completion)
}

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body")
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

	modelReasoning := reasoning.ExtractFromModelName(requestedModel)
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
	if reasoningOverrides == nil {
		reasoningOverrides = modelReasoning
	}
	reasoningParam := reasoning.BuildReasoningParam(
		s.Config.ReasoningEffort, s.Config.ReasoningSummary, reasoningOverrides, model,
	)

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
		var errResp types.ErrorResponse
		if json.Unmarshal(errBody, &errResp) == nil && errResp.Error.Message != "" {
			writeError(w, resp.StatusCode, errResp.Error.Message)
			return
		}
		writeError(w, resp.StatusCode, "Upstream error")
		return
	}

	created := time.Now().Unix()
	outputModel := requestedModel
	if outputModel == "" {
		outputModel = model
	}

	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(resp.StatusCode)
		sse.TranslateText(w, resp.Body.Body, outputModel, created, sse.TranslateTextOptions{
			Verbose:      s.Config.VerboseObfuscation,
			IncludeUsage: includeUsage,
		})
		return
	}

	// Non-streaming
	defer resp.Body.Body.Close()
	reader := sse.NewReader(resp.Body.Body)
	var fullText string
	responseID := "cmpl"
	var usageObj *types.Usage

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}
		if r, ok := evt.Data["response"].(map[string]any); ok {
			if id, ok := r["id"].(string); ok && id != "" {
				responseID = id
			}
		}
		if u := extractUsageFromEvent(evt.Data); u != nil {
			usageObj = u
		}
		switch evt.Type {
		case "response.output_text.delta":
			delta, _ := evt.Data["delta"].(string)
			fullText += delta
		case "response.completed":
			goto textDone
		}
	}

textDone:
	completion := types.TextCompletionResponse{
		ID:      responseID,
		Object:  "text_completion",
		Created: created,
		Model:   outputModel,
		Choices: []types.TextChoice{
			{Index: 0, Text: fullText, FinishReason: types.StringPtr("stop"), Logprobs: nil},
		},
		Usage: usageObj,
	}
	writeJSON(w, resp.StatusCode, completion)
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
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

func extractUsageFromEvent(data map[string]any) *types.Usage {
	resp, _ := data["response"].(map[string]any)
	if resp == nil {
		return nil
	}
	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		return nil
	}
	pt := intFromAny(usage["input_tokens"])
	ct := intFromAny(usage["output_tokens"])
	tt := intFromAny(usage["total_tokens"])
	if tt == 0 {
		tt = pt + ct
	}
	return &types.Usage{
		PromptTokens:     pt,
		CompletionTokens: ct,
		TotalTokens:      tt,
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

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func boolVal(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func stringOrEmpty(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
