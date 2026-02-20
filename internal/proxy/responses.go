package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/n0madic/go-chatmock/internal/limits"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/reasoning"
	"github.com/n0madic/go-chatmock/internal/sse"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}

	if s.Config.Verbose {
		slog.Info("IN POST /v1/responses", "body", string(body))
	}

	var req types.ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	inputItems, err := req.ParseInput()
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid input field")
		return
	}
	normalizeResponsesSystemToUser(inputItems)

	requestedModel := req.Model
	model := models.NormalizeModelName(requestedModel, s.Config.DebugModel)

	if !s.validateModel(w, model) {
		return
	}

	// Reasoning
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

	// Combine server instructions with client instructions
	baseInstructions := s.Config.InstructionsForModel(model)
	instructions := baseInstructions
	if req.Instructions != "" {
		if instructions != "" {
			instructions = instructions + "\n\n" + req.Instructions
		} else {
			instructions = req.Instructions
		}
	}

	// Tools: pass through directly; apply default web search if none provided
	tools := req.Tools
	if tools == nil && s.Config.DefaultWebSearch {
		toolChoiceStr, _ := req.ToolChoice.(string)
		if toolChoiceStr != "none" {
			tools = []types.ResponsesTool{{Type: "web_search"}}
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

	upReq := &upstream.Request{
		Model:             model,
		Instructions:      instructions,
		InputItems:        inputItems,
		Tools:             tools,
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
		errBody, _ := io.ReadAll(resp.Body.Body)
		resp.Body.Body.Close()
		writeError(w, resp.StatusCode, formatUpstreamError(resp.StatusCode, errBody))
		return
	}

	outputModel := requestedModel
	if outputModel == "" {
		outputModel = model
	}

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(resp.StatusCode)
		sse.TranslateResponses(w, resp.Body.Body)
		return
	}

	s.collectResponsesResponse(w, resp, outputModel)
}

func (s *Server) collectResponsesResponse(w http.ResponseWriter, resp *upstream.Response, model string) {
	defer resp.Body.Body.Close()

	reader := sse.NewReader(resp.Body.Body)
	var outputItems []types.ResponsesOutputItem
	var responseID string
	var createdAt int64
	var usageObj *types.ResponsesUsage
	var errorMsg string
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
					InputTokens:  intFromAny(u["input_tokens"]),
					OutputTokens: intFromAny(u["output_tokens"]),
					TotalTokens:  intFromAny(u["total_tokens"]),
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

// unmarshalOutputItem converts a raw map to ResponsesOutputItem via JSON round-trip.
func unmarshalOutputItem(item map[string]any) types.ResponsesOutputItem {
	b, _ := json.Marshal(item)
	var out types.ResponsesOutputItem
	json.Unmarshal(b, &out) //nolint:errcheck
	return out
}

// normalizeResponsesSystemToUser rewrites system-role input messages to user-role,
// because the upstream Responses endpoint rejects system messages in input.
func normalizeResponsesSystemToUser(items []types.ResponsesInputItem) {
	for i := range items {
		if items[i].Role == "system" && (items[i].Type == "" || items[i].Type == "message") {
			items[i].Role = "user"
		}
	}
}
