package pipeline

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/codec"
	"github.com/n0madic/go-chatmock/internal/limits"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/normalize"
	"github.com/n0madic/go-chatmock/internal/reasoning"
	"github.com/n0madic/go-chatmock/internal/state"
	"github.com/n0madic/go-chatmock/internal/stream"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

// BodyHasInputField returns true if the JSON body contains a top-level "input" key.
func BodyHasInputField(body []byte) bool {
	var probe struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return len(probe.Input) > 0
}

// ExecutePassthrough sends a Responses API request upstream with minimal patching.
// The original request body is preserved — only model, store, instructions,
// reasoning, and prompt_cache_key fields are patched.
func (p *Pipeline) ExecutePassthrough(
	ctx *RequestContext,
	w http.ResponseWriter,
	body []byte,
	enc codec.Encoder,
) {
	writeErr := func(status int, msg string) {
		enc.WriteError(w, status, msg)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		writeErr(http.StatusBadRequest, "Invalid JSON body")
		return
	}

	// Extract and normalize model
	requestedModel := strings.TrimSpace(stream.StringFromAny(raw["model"]))
	model := models.NormalizeModelName(requestedModel, p.Config.DebugModel)
	if ok, hint := p.Registry.IsKnownModel(model); !ok && p.Config.DebugModel == "" {
		msg := fmt.Sprintf("model %q is not available via this endpoint", model)
		if hint != "" {
			msg += "; available models: " + hint
		}
		writeErr(http.StatusBadRequest, msg)
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

	// Extract system messages from input into instructions BEFORE context
	// restoration — restorePreviousContext changes raw["input"] to a typed
	// slice which the []any-based extractor cannot inspect.
	inputSystemInstructions := extractAndRemoveSystemMessages(raw)

	// Strip fields unsupported by the upstream ChatGPT Codex backend.
	for _, key := range []string{"metadata", "stream_options", "user", "prompt_cache_retention", "max_output_tokens"} {
		delete(raw, key)
	}

	// Handle previous_response_id polyfill
	conversationID := normalize.ExtractConversationID(raw)
	previousResponseID := strings.TrimSpace(stream.StringFromAny(raw["previous_response_id"]))
	autoPreviousResponseID := false
	if previousResponseID == "" && conversationID != "" {
		if mappedID, ok := p.Store.GetConversationLatest(conversationID); ok {
			previousResponseID = mappedID
			autoPreviousResponseID = true
		}
	}

	// Restore previous context if needed
	if previousResponseID != "" {
		inputItems, err := restorePreviousContext(p.Store, raw, previousResponseID)
		if err != nil {
			if autoPreviousResponseID {
				previousResponseID = ""
			} else {
				writeErr(http.StatusBadRequest, err.Error())
				return
			}
		} else if inputItems != nil {
			raw["input"] = inputItems
		}
	}
	delete(raw, "previous_response_id")

	// Instructions composition
	clientInstructions := strings.TrimSpace(stream.StringFromAny(raw["instructions"]))
	instructions := normalize.ComposeInstructions(p.Config, p.Store, "responses", model, clientInstructions, inputSystemInstructions, previousResponseID)
	if instructions != "" {
		raw["instructions"] = instructions
	}

	// Store: always send false upstream
	raw["store"] = false

	// Ensure stream=true
	streamReq := false
	if v, ok := raw["stream"].(bool); ok {
		streamReq = v
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
	if reasoningOverrides == nil {
		reasoningOverrides = reasoning.ExtractFromModelName(requestedModel)
	}
	reasoningParam := reasoning.BuildReasoningParam(
		p.Config.ReasoningEffort,
		p.Config.ReasoningSummary,
		reasoningOverrides,
		model,
	)
	if reasoningParam != nil {
		raw["reasoning"] = map[string]any{
			"effort":  reasoningParam.Effort,
			"summary": reasoningParam.Summary,
		}
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

	// Ensure a session ID for upstream prompt caching. The normalized path
	// (Do) calls EnsureSessionID automatically; for passthrough we must do it
	// here because DoRaw receives the session ID as an opaque string.
	sessionID := ctx.SessionID
	if sessionID == "" {
		inputItems := extractInputItemsFromRaw(raw)
		sessionID = p.Upstream.Sessions.EnsureSessionID(instructions, inputItems, "")
	}

	// Inject prompt_cache_key into the body to match the header-based session.
	raw["prompt_cache_key"] = sessionID

	if p.Config.Verbose {
		reasoningEffort := ""
		reasoningSummary := ""
		if reasoningParam != nil {
			reasoningEffort = reasoningParam.Effort
			reasoningSummary = reasoningParam.Summary
		}
		slog.Info("responses.passthrough",
			"requested_model", requestedModel,
			"upstream_model", model,
			"stream", streamReq,
			"instructions_chars", len(instructions),
			"previous_response_id", previousResponseID != "",
			"previous_response_id_auto", autoPreviousResponseID,
			"conversation_id", conversationID != "",
			"reasoning_effort", reasoningEffort,
			"reasoning_summary", reasoningSummary,
			"session_id", sessionID,
			"session_override", ctx.SessionID != "",
		)
	}

	// Marshal the patched body
	patchedBody, err := json.Marshal(raw)
	if err != nil {
		writeErr(http.StatusInternalServerError, "Failed to marshal patched request")
		return
	}

	// Send upstream via DoRaw
	resp, err := p.Upstream.DoRaw(ctx.Context, patchedBody, sessionID)
	if err != nil {
		writeErr(http.StatusUnauthorized, err.Error())
		return
	}
	limits.RecordFromResponse(resp.Headers)

	if resp.StatusCode >= 400 {
		defer resp.Body.Body.Close()
		errBody, _ := io.ReadAll(resp.Body.Body)
		writeErr(resp.StatusCode, codec.FormatUpstreamError(resp.StatusCode, errBody))
		return
	}

	// Extract input items for state storage
	inputItems := extractInputItemsFromRaw(raw)

	outputModel := requestedModel
	if outputModel == "" {
		outputModel = model
	}

	if streamReq {
		enc.WriteStreamHeaders(w, resp.StatusCode)
		p.streamResponsesPassthrough(w, resp, inputItems, instructions, conversationID)
		return
	}
	p.collectResponsesPassthrough(w, resp, enc, outputModel, inputItems, instructions, conversationID)
}

// streamResponsesPassthrough forwards upstream SSE events as-is while capturing state.
func (p *Pipeline) streamResponsesPassthrough(
	w http.ResponseWriter,
	resp *upstream.Response,
	inputItems []types.ResponsesInputItem,
	instructions string,
	conversationID string,
) {
	defer resp.Body.Body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	reader := stream.NewReader(resp.Body.Body)
	var responseID string
	var toolCalls []state.FunctionCall
	var outputItems []types.ResponsesOutputItem
	sentDone := false

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		if evt.Type != "" {
			fmt.Fprintf(w, "event: %s\n", evt.Type)
		}
		fmt.Fprintf(w, "data: %s\n\n", evt.Raw)
		flusher.Flush()

		if id := stream.ResponseIDFromEvent(evt.Data); id != "" {
			responseID = id
		}

		if evt.Type == "response.output_item.done" {
			item, _ := evt.Data["item"].(map[string]any)
			if item != nil {
				fc, ok := extractFunctionCallFromMap(item)
				if ok {
					toolCalls = append(toolCalls, fc)
				}
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

	delta := outputItemsToInputItems(outputItems)
	combined := appendContextHistory(inputItems, delta)
	p.Store.PutSnapshot(responseID, combined, toolCalls)
	p.Store.PutInstructions(responseID, instructions)
	p.Store.PutConversationLatest(conversationID, responseID)
}

// collectResponsesPassthrough collects a non-streaming responses response with state.
func (p *Pipeline) collectResponsesPassthrough(
	w http.ResponseWriter,
	resp *upstream.Response,
	enc codec.Encoder,
	outputModel string,
	inputItems []types.ResponsesInputItem,
	instructions string,
	conversationID string,
) {
	defer resp.Body.Body.Close()

	collected := collectFullResponse(resp.Body.Body)

	// Store state
	delta := outputItemsToInputItems(collected.OutputItems)
	calls := extractFunctionCalls(delta)
	combined := appendContextHistory(inputItems, delta)
	p.Store.PutSnapshot(collected.ResponseID, combined, calls)
	p.Store.PutInstructions(collected.ResponseID, instructions)
	p.Store.PutConversationLatest(conversationID, collected.ResponseID)

	if collected.ErrorMessage != "" {
		enc.WriteError(w, http.StatusBadGateway, collected.ErrorMessage)
		return
	}

	enc.WriteCollected(w, resp.StatusCode, collected, outputModel)
}

// restorePreviousContext prepends stored context from a previous response.
func restorePreviousContext(store *state.Store, raw map[string]any, previousResponseID string) (any, error) {
	ctx, ok := store.GetContext(previousResponseID)
	if !ok {
		return nil, nil
	}

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

	combined := make([]types.ResponsesInputItem, 0, len(ctx)+len(currentItems))
	combined = append(combined, ctx...)
	combined = append(combined, currentItems...)
	return combined, nil
}

// extractInputItemsFromRaw extracts ResponsesInputItem from the raw map.
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

// extractFunctionCallFromMap extracts a state.FunctionCall from a raw output item map.
func extractFunctionCallFromMap(item map[string]any) (state.FunctionCall, bool) {
	if item == nil {
		return state.FunctionCall{}, false
	}
	itemType, _ := item["type"].(string)
	if itemType != "function_call" {
		return state.FunctionCall{}, false
	}
	callID := strings.TrimSpace(stringOrEmpty(item, "call_id"))
	if callID == "" {
		callID = strings.TrimSpace(stringOrEmpty(item, "id"))
	}
	name := strings.TrimSpace(stringOrEmpty(item, "name"))
	args := stringOrEmpty(item, "arguments")
	if callID == "" || name == "" {
		return state.FunctionCall{}, false
	}
	return state.FunctionCall{
		CallID:    callID,
		Name:      name,
		Arguments: args,
	}, true
}

func stringOrEmpty(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// extractAndRemoveSystemMessages removes system-role messages from the raw
// input array and returns their concatenated text. The upstream ChatGPT Codex
// backend rejects system messages in input — they must go into instructions.
func extractAndRemoveSystemMessages(raw map[string]any) string {
	items, ok := raw["input"].([]any)
	if !ok || len(items) == 0 {
		return ""
	}
	var kept []any
	var parts []string
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}
		role, _ := m["role"].(string)
		if role != "system" {
			kept = append(kept, item)
			continue
		}
		if text := extractTextFromRawContent(m["content"]); text != "" {
			parts = append(parts, text)
		}
		// drop system item from input
	}
	if len(parts) > 0 {
		raw["input"] = kept
	}
	return strings.Join(parts, "\n\n")
}

// extractTextFromRawContent extracts plain text from a content field that may
// be a string, an array of content parts, or nil.
func extractTextFromRawContent(content any) string {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return strings.TrimSpace(s)
	}
	arr, ok := content.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, part := range arr {
		pm, ok := part.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := pm["type"].(string)
		if typ != "" && typ != "text" && typ != "input_text" {
			continue
		}
		text, _ := pm["text"].(string)
		text = strings.TrimSpace(text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}
