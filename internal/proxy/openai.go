package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n0madic/go-chatmock/internal/limits"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/reasoning"
	responsesstate "github.com/n0madic/go-chatmock/internal/responses-state"
	"github.com/n0madic/go-chatmock/internal/sse"
	"github.com/n0madic/go-chatmock/internal/transform"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	s.handleUnifiedCompletions(w, r, universalRouteChat)
}

func (s *Server) collectChatCompletion(
	w http.ResponseWriter,
	resp *upstream.Response,
	model string,
	created int64,
	requestInput []types.ResponsesInputItem,
	instructions string,
	conversationID string,
) {
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

	storeChatCollectedState(s, collected, requestInput, instructions, conversationID)

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
		Store:          types.BoolPtr(false),
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

func storeChatCollectedState(
	s *Server,
	collected collectedTextResponse,
	requestInput []types.ResponsesInputItem,
	instructions string,
	conversationID string,
) {
	if s == nil || collected.ResponseID == "" {
		return
	}

	var delta []types.ResponsesInputItem
	if txt := strings.TrimSpace(collected.FullText); txt != "" {
		delta = append(delta, types.ResponsesInputItem{
			Type:    "message",
			Role:    "assistant",
			Content: []types.ResponsesContent{{Type: "output_text", Text: txt}},
		})
	}

	var calls []responsesstate.FunctionCall
	for _, tc := range collected.ToolCalls {
		callID := strings.TrimSpace(tc.ID)
		name := strings.TrimSpace(tc.Function.Name)
		if callID == "" || name == "" {
			continue
		}
		args := tc.Function.Arguments
		delta = append(delta, types.ResponsesInputItem{
			Type:      "function_call",
			CallID:    callID,
			Name:      name,
			Arguments: args,
		})
		calls = append(calls, responsesstate.FunctionCall{
			CallID:    callID,
			Name:      name,
			Arguments: args,
		})
	}

	s.responsesState.PutSnapshot(collected.ResponseID, appendContextHistory(requestInput, delta), calls)
	s.responsesState.PutInstructions(collected.ResponseID, instructions)
	s.responsesState.PutConversationLatest(conversationID, collected.ResponseID)
}
