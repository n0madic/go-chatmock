package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/limits"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

// bodyHasInputField returns true if the JSON body contains a top-level "input" key.
// This is a quick check (no full parse) to decide between passthrough and normalization.
func bodyHasInputField(body []byte) bool {
	var probe struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return len(probe.Input) > 0
}

// handleResponsesPassthrough sends a Responses API request upstream with minimal
// patching. The original request body is preserved as much as possible — only
// the model, store, instructions, and prompt_cache_key fields are patched. All
// other SDK fields (metadata, prompt_cache_retention, custom tool formats, etc.)
// pass through unchanged.
func (s *Server) handleResponsesPassthrough(w http.ResponseWriter, r *http.Request, body []byte) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	// Extract and normalize model
	requestedModel := strings.TrimSpace(stringFromAny(raw["model"]))
	model := models.NormalizeModelName(requestedModel, s.Config.DebugModel)
	if !s.validateModel(w, model) {
		return
	}

	// Patch model for upstream
	raw["model"] = model

	// Normalize string input to array (upstream requires array format).
	if s, ok := raw["input"].(string); ok {
		raw["input"] = []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": s},
				},
			},
		}
	}

	// Handle previous_response_id polyfill
	conversationID := extractConversationID(raw)
	previousResponseID := strings.TrimSpace(stringFromAny(raw["previous_response_id"]))
	autoPreviousResponseID := false
	if previousResponseID == "" && conversationID != "" {
		if mappedID, ok := s.responsesState.GetConversationLatest(conversationID); ok {
			previousResponseID = mappedID
			autoPreviousResponseID = true
		}
	}

	// Restore previous context if needed
	if previousResponseID != "" {
		inputItems, err := s.restorePreviousContext(raw, previousResponseID)
		if err != nil {
			if autoPreviousResponseID {
				previousResponseID = ""
			} else {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		} else if inputItems != nil {
			raw["input"] = inputItems
		}
	}
	// Don't send previous_response_id upstream; the polyfill inlines the context.
	delete(raw, "previous_response_id")

	// Instructions composition
	clientInstructions := strings.TrimSpace(stringFromAny(raw["instructions"]))
	instructions := composeInstructionsForRoute(s, universalRouteResponses, model, clientInstructions, "", previousResponseID)
	if instructions != "" {
		raw["instructions"] = instructions
	}

	// Store: always send false upstream (upstream rejects store=true)
	raw["store"] = false

	// Ensure stream=true
	stream := false
	if v, ok := raw["stream"].(bool); ok {
		stream = v
	}
	raw["stream"] = true

	// Reasoning: apply model fallback if not provided
	var reasoningOverrides *types.ReasoningParam
	if ro, ok := raw["reasoning"].(map[string]any); ok {
		reasoningOverrides = &types.ReasoningParam{}
		if e, ok := ro["effort"].(string); ok {
			reasoningOverrides.Effort = e
		}
		if sm, ok := ro["summary"].(string); ok {
			reasoningOverrides.Summary = sm
		}
	}
	reasoningParam := buildReasoningWithModelFallback(s.Config, requestedModel, model, reasoningOverrides)
	if reasoningParam != nil {
		raw["reasoning"] = map[string]any{
			"effort":  reasoningParam.Effort,
			"summary": reasoningParam.Summary,
		}
		// Ensure reasoning.encrypted_content is included
		includes, _ := raw["include"].([]any)
		hasReasoningInclude := false
		for _, inc := range includes {
			if s, ok := inc.(string); ok && s == "reasoning.encrypted_content" {
				hasReasoningInclude = true
				break
			}
		}
		if !hasReasoningInclude {
			includes = append(includes, "reasoning.encrypted_content")
			raw["include"] = includes
		}
	}

	// Session ID for prompt caching
	sessionID := strings.TrimSpace(r.Header.Get("X-Session-Id"))
	if _, ok := raw["prompt_cache_key"]; !ok && sessionID == "" {
		// No session override and no explicit prompt_cache_key; let upstream client handle it
	}

	if s.Config.Verbose {
		reasoningEffort, reasoningSummary := reasoningLogFields(reasoningParam)
		slog.Info("responses.passthrough",
			"requested_model", requestedModel,
			"upstream_model", model,
			"stream", stream,
			"instructions_chars", len(instructions),
			"previous_response_id", previousResponseID != "",
			"previous_response_id_auto", autoPreviousResponseID,
			"conversation_id", conversationID != "",
			"reasoning_effort", reasoningEffort,
			"reasoning_summary", reasoningSummary,
			"session_override", sessionID != "",
		)
	}

	// Marshal the patched body
	patchedBody, err := json.Marshal(raw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to marshal patched request")
		return
	}

	// Send upstream via DoRaw
	resp, err := s.upstreamClient.DoRaw(r.Context(), patchedBody, sessionID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	limits.RecordFromResponse(resp.Headers)

	if resp.StatusCode >= 400 {
		s.handlePassthroughError(w, resp)
		return
	}

	// Extract input items for state storage (best-effort)
	inputItems := extractInputItemsFromRaw(raw)

	outputModel := requestedModel
	if outputModel == "" {
		outputModel = model
	}

	if stream {
		writeSSEHeaders(w, resp.StatusCode)
		s.streamResponsesWithState(w, resp, inputItems, instructions, conversationID)
		return
	}
	s.collectResponsesResponse(w, resp, outputModel, inputItems, instructions, conversationID)
}

// restorePreviousContext prepends stored context from a previous response ID
// to the current input items in the raw request.
func (s *Server) restorePreviousContext(raw map[string]any, previousResponseID string) (any, error) {
	ctx, ok := s.responsesState.GetContext(previousResponseID)
	if !ok {
		return nil, nil
	}

	// Parse current input items
	var currentItems []types.ResponsesInputItem
	if inputRaw, ok := raw["input"]; ok {
		inputBytes, err := json.Marshal(inputRaw)
		if err == nil {
			var s string
			if json.Unmarshal(inputBytes, &s) == nil {
				currentItems = []types.ResponsesInputItem{
					{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: s}}},
				}
			} else {
				_ = json.Unmarshal(inputBytes, &currentItems)
			}
		}
	}

	// Prepend stored context
	combined := make([]types.ResponsesInputItem, 0, len(ctx)+len(currentItems))
	combined = append(combined, ctx...)
	combined = append(combined, currentItems...)
	return combined, nil
}

// extractInputItemsFromRaw extracts ResponsesInputItem from the raw map for state storage.
func extractInputItemsFromRaw(raw map[string]any) []types.ResponsesInputItem {
	inputRaw, ok := raw["input"]
	if !ok {
		return nil
	}
	inputBytes, err := json.Marshal(inputRaw)
	if err != nil {
		return nil
	}
	req := types.ResponsesRequest{Input: inputBytes}
	items, _ := req.ParseInput()
	return items
}

// handlePassthroughError reads and forwards the upstream error response.
func (s *Server) handlePassthroughError(w http.ResponseWriter, resp *upstream.Response) {
	defer resp.Body.Body.Close()

	errBody, _ := io.ReadAll(resp.Body.Body)
	writeError(w, resp.StatusCode, formatUpstreamError(resp.StatusCode, errBody))
}
