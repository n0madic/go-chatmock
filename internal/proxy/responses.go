package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n0madic/go-chatmock/internal/limits"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/responses-state"
	"github.com/n0madic/go-chatmock/internal/sse"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	var req types.ResponsesRequest
	if _, ok := parseJSONRequest(w, r, &req, writeError, "Failed to read request body", "Invalid JSON body"); !ok {
		return
	}

	inputItems, err := req.ParseInput()
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid input field")
		return
	}
	inputItems, systemInstructions := moveResponsesSystemMessagesToInstructions(inputItems)
	inputItems, err = s.restoreFunctionCallContext(inputItems, req.PreviousResponseID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	requestedModel := req.Model
	model := models.NormalizeModelName(requestedModel, s.Config.DebugModel)

	if !s.validateModel(w, model) {
		return
	}

	// Reasoning
	reasoningParam := buildReasoningWithModelFallback(s.Config, requestedModel, model, req.Reasoning)

	// Responses API should use client-provided instructions only. We also move
	// system-role input messages into instructions for upstream compatibility.
	instructions := strings.TrimSpace(req.Instructions)
	if systemInstructions != "" {
		if instructions != "" {
			instructions = instructions + "\n\n" + systemInstructions
		} else {
			instructions = systemInstructions
		}
	}
	if req.PreviousResponseID != "" && instructions == "" {
		if prevInstructions, ok := s.responsesState.GetInstructions(req.PreviousResponseID); ok {
			instructions = prevInstructions
		}
	}

	// Tools: pass through directly; apply default web search if none provided
	tools := req.Tools
	defaultWebSearchApplied := false
	if tools == nil && s.Config.DefaultWebSearch {
		toolChoiceStr, _ := req.ToolChoice.(string)
		if toolChoiceStr != "none" {
			tools = []types.ResponsesTool{{Type: "web_search"}}
			defaultWebSearchApplied = true
		}
	}

	toolChoice := req.ToolChoice
	if toolChoice == nil {
		toolChoice = "auto"
	}

	parallelToolCalls := false
	if req.ParallelToolCalls != nil {
		parallelToolCalls = *req.ParallelToolCalls
	}

	storeForUpstream, storeForced := normalizeStoreForUpstream(req.Store)
	if storeForced && s.Config.Verbose {
		slog.Warn("client requested store=true; forcing store=false for upstream compatibility")
	}
	reasoningEffort, reasoningSummary := reasoningLogFields(reasoningParam)
	if s.Config.Verbose {
		slog.Info("responses.request",
			"requested_model", requestedModel,
			"upstream_model", model,
			"stream", req.Stream,
			"input_items", len(inputItems),
			"tools", len(tools),
			"tool_choice", summarizeToolChoice(toolChoice),
			"parallel_tool_calls", parallelToolCalls,
			"include_count", len(req.Include),
			"instructions_chars", len(instructions),
			"previous_response_id", strings.TrimSpace(req.PreviousResponseID) != "",
			"store_requested", boolPtrState(req.Store),
			"store_upstream", boolPtrState(storeForUpstream),
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
		ToolChoice:        toolChoice,
		ParallelToolCalls: parallelToolCalls,
		Include:           req.Include,
		Store:             storeForUpstream,
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
		resp2, errBody, retried, retryErr := s.retryIfStoreUnsupported(
			r.Context(),
			resp,
			upReq,
			"upstream rejected store parameter; retrying without store",
		)
		if retried {
			if retryErr != nil {
				writeError(w, http.StatusBadGateway, "Upstream retry failed after removing store: "+retryErr.Error())
				return
			}
			if resp2.StatusCode >= 400 {
				errBody2, _ := io.ReadAll(resp2.Body.Body)
				resp2.Body.Body.Close()
				writeError(w, resp2.StatusCode, formatUpstreamError(resp2.StatusCode, errBody2))
				return
			}
			resp = resp2
		} else {
			if resp.StatusCode == http.StatusBadRequest && isToolCallOutputNotFoundError(errBody) {
				writeError(w, resp.StatusCode, formatUpstreamError(resp.StatusCode, errBody)+". Hint: send previous_response_id from the response that created this tool call, or include matching function_call items in input.")
				return
			}
			writeError(w, resp.StatusCode, formatUpstreamError(resp.StatusCode, errBody))
			return
		}
	}

	outputModel := requestedModel
	if outputModel == "" {
		outputModel = model
	}

	if req.Stream {
		writeSSEHeaders(w, resp.StatusCode)
		s.streamResponsesWithState(w, resp, inputItems, instructions)
		return
	}

	s.collectResponsesResponse(w, resp, outputModel, inputItems, instructions)
}

func (s *Server) collectResponsesResponse(w http.ResponseWriter, resp *upstream.Response, model string, requestInput []types.ResponsesInputItem, instructions string) {
	defer resp.Body.Body.Close()

	reader := sse.NewReader(resp.Body.Body)
	var outputItems []types.ResponsesOutputItem
	var responseID string
	var createdAt int64
	var usageObj *types.ResponsesUsage
	var errorMsg string
	var toolCalls []responsesstate.FunctionCall
	status := "completed"

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		if r, ok := evt.Data["response"].(map[string]any); ok {
			if id, ok := r["id"].(string); ok && id != "" {
				responseID = id
			}
			if ca, ok := r["created_at"].(float64); ok {
				createdAt = int64(ca)
			}
			if u, ok := r["usage"].(map[string]any); ok {
				usageObj = &types.ResponsesUsage{
					InputTokens:  types.IntFromAny(u["input_tokens"]),
					OutputTokens: types.IntFromAny(u["output_tokens"]),
					TotalTokens:  types.IntFromAny(u["total_tokens"]),
				}
				if usageObj.TotalTokens == 0 {
					usageObj.TotalTokens = usageObj.InputTokens + usageObj.OutputTokens
				}
			}
		}

		switch evt.Type {
		case "response.output_item.done":
			item, _ := evt.Data["item"].(map[string]any)
			if item != nil {
				if fc, ok := extractFunctionCallFromOutputItem(item); ok {
					toolCalls = append(toolCalls, fc)
				}
				outputItems = append(outputItems, unmarshalOutputItem(item))
			}
		case "response.failed":
			status = "failed"
			if r, ok := evt.Data["response"].(map[string]any); ok {
				if e, ok := r["error"].(map[string]any); ok {
					errorMsg, _ = e["message"].(string)
				}
			}
			if errorMsg == "" {
				errorMsg = "response.failed"
			}
			goto done
		case "response.completed":
			goto done
		}
	}

done:
	s.responsesState.PutSnapshot(responseID, appendContextHistory(requestInput, outputItemsToInputItems(outputItems)), toolCalls)
	s.responsesState.PutInstructions(responseID, instructions)

	if errorMsg != "" {
		writeError(w, http.StatusBadGateway, errorMsg)
		return
	}

	if createdAt == 0 {
		createdAt = time.Now().Unix()
	}

	result := types.ResponsesResponse{
		ID:        responseID,
		Object:    "response",
		CreatedAt: createdAt,
		Model:     model,
		Output:    outputItems,
		Status:    status,
		Usage:     usageObj,
	}
	writeJSON(w, resp.StatusCode, result)
}

func (s *Server) streamResponsesWithState(w http.ResponseWriter, resp *upstream.Response, requestInput []types.ResponsesInputItem, instructions string) {
	defer resp.Body.Body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	reader := sse.NewReader(resp.Body.Body)
	var responseID string
	var toolCalls []responsesstate.FunctionCall
	var outputItems []types.ResponsesOutputItem
	sentDone := false

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		fmt.Fprintf(w, "data: %s\n\n", evt.Raw)
		flusher.Flush()

		if id := responseIDFromEvent(evt.Data); id != "" {
			responseID = id
		}

		if evt.Type == "response.output_item.done" {
			item, _ := evt.Data["item"].(map[string]any)
			if fc, ok := extractFunctionCallFromOutputItem(item); ok {
				toolCalls = append(toolCalls, fc)
			}
			if item != nil {
				outputItems = append(outputItems, unmarshalOutputItem(item))
			}
		}

		if evt.Type == "response.completed" || evt.Type == "response.failed" {
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			sentDone = true
			break
		}
	}

	if !sentDone {
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}

	s.responsesState.PutSnapshot(responseID, appendContextHistory(requestInput, outputItemsToInputItems(outputItems)), toolCalls)
	s.responsesState.PutInstructions(responseID, instructions)
}

// unmarshalOutputItem converts a raw map to ResponsesOutputItem via JSON round-trip.
func unmarshalOutputItem(item map[string]any) types.ResponsesOutputItem {
	b, _ := json.Marshal(item)
	var out types.ResponsesOutputItem
	json.Unmarshal(b, &out) //nolint:errcheck
	return out
}

func outputItemsToInputItems(items []types.ResponsesOutputItem) []types.ResponsesInputItem {
	out := make([]types.ResponsesInputItem, 0, len(items))
	for _, item := range items {
		if converted, ok := inputItemFromOutputItem(item); ok {
			out = append(out, converted)
		}
	}
	return out
}

func inputItemFromOutputItem(item types.ResponsesOutputItem) (types.ResponsesInputItem, bool) {
	switch item.Type {
	case "message":
		if len(item.Content) == 0 {
			return types.ResponsesInputItem{}, false
		}
		role := item.Role
		if role == "" {
			role = "assistant"
		}
		content := make([]types.ResponsesContent, len(item.Content))
		copy(content, item.Content)
		return types.ResponsesInputItem{
			Type:    "message",
			Role:    role,
			Content: content,
		}, true
	case "function_call":
		callID := item.CallID
		if callID == "" {
			callID = item.ID
		}
		if callID == "" || item.Name == "" {
			return types.ResponsesInputItem{}, false
		}
		return types.ResponsesInputItem{
			Type:      "function_call",
			CallID:    callID,
			Name:      item.Name,
			Arguments: item.Arguments,
		}, true
	default:
		return types.ResponsesInputItem{}, false
	}
}

func appendContextHistory(base []types.ResponsesInputItem, delta []types.ResponsesInputItem) []types.ResponsesInputItem {
	if len(base) == 0 && len(delta) == 0 {
		return nil
	}
	combined := cloneResponsesInputItems(base)
	if len(delta) > 0 {
		combined = append(combined, cloneResponsesInputItems(delta)...)
	}
	return combined
}

func cloneResponsesInputItems(items []types.ResponsesInputItem) []types.ResponsesInputItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]types.ResponsesInputItem, len(items))
	copy(out, items)
	for i := range out {
		if len(items[i].Content) == 0 {
			continue
		}
		content := make([]types.ResponsesContent, len(items[i].Content))
		copy(content, items[i].Content)
		out[i].Content = content
	}
	return out
}

// moveResponsesSystemMessagesToInstructions rewrites system messages into
// instructions (text-only cases) and keeps non-text system messages as user.
func moveResponsesSystemMessagesToInstructions(items []types.ResponsesInputItem) ([]types.ResponsesInputItem, string) {
	if len(items) == 0 {
		return nil, ""
	}

	out := make([]types.ResponsesInputItem, 0, len(items))
	var instructionParts []string

	for _, item := range items {
		if item.Role != "system" || (item.Type != "" && item.Type != "message") {
			out = append(out, item)
			continue
		}

		if text, ok := extractSystemInstructionText(item.Content); ok {
			instructionParts = append(instructionParts, text)
			continue
		}

		item.Role = "user"
		out = append(out, item)
	}

	return out, strings.Join(instructionParts, "\n\n")
}

func extractSystemInstructionText(content []types.ResponsesContent) (string, bool) {
	if len(content) == 0 {
		return "", false
	}

	parts := make([]string, 0, len(content))
	for _, c := range content {
		if c.ImageURL != "" {
			return "", false
		}
		typ := strings.TrimSpace(c.Type)
		if typ != "" && typ != "input_text" && typ != "text" {
			return "", false
		}
		text := strings.TrimSpace(c.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}

	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}
