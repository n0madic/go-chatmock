package proxy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n0madic/go-chatmock/internal/responses-state"
	"github.com/n0madic/go-chatmock/internal/sse"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	s.handleUnifiedCompletions(w, r, universalRouteResponses)
}

func (s *Server) collectResponsesResponse(
	w http.ResponseWriter,
	resp *upstream.Response,
	model string,
	requestInput []types.ResponsesInputItem,
	instructions string,
	conversationID string,
) {
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
				usageObj = mergeResponsesUsage(usageObj, u)
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
			// goto is the only way to break out of a switch inside a for loop.
			goto done
		case "response.completed":
			goto done
		}
	}

done:
	s.responsesState.PutSnapshot(responseID, appendContextHistory(requestInput, outputItemsToInputItems(outputItems)), toolCalls)
	s.responsesState.PutInstructions(responseID, instructions)
	s.responsesState.PutConversationLatest(conversationID, responseID)

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

func mergeResponsesUsage(current *types.ResponsesUsage, usage map[string]any) *types.ResponsesUsage {
	if len(usage) == 0 {
		return current
	}

	_, hasInput := usage["input_tokens"]
	_, hasOutput := usage["output_tokens"]
	totalRaw, hasTotal := usage["total_tokens"]
	if !hasInput && !hasOutput && !hasTotal {
		return current
	}

	if current == nil {
		current = &types.ResponsesUsage{}
	}

	if hasInput {
		current.InputTokens = types.IntFromAny(usage["input_tokens"])
	}
	if hasOutput {
		current.OutputTokens = types.IntFromAny(usage["output_tokens"])
	}
	if hasTotal {
		current.TotalTokens = types.IntFromAny(totalRaw)
	} else if hasInput || hasOutput {
		// Preserve fallback behavior when total_tokens is absent.
		current.TotalTokens = current.InputTokens + current.OutputTokens
	}
	if current.TotalTokens == 0 {
		current.TotalTokens = current.InputTokens + current.OutputTokens
	}

	return current
}

func (s *Server) streamResponsesWithState(
	w http.ResponseWriter,
	resp *upstream.Response,
	requestInput []types.ResponsesInputItem,
	instructions string,
	conversationID string,
) {
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
	s.responsesState.PutConversationLatest(conversationID, responseID)
}

// unmarshalOutputItem converts a raw map to ResponsesOutputItem via JSON round-trip.
func unmarshalOutputItem(item map[string]any) types.ResponsesOutputItem {
	b, err := json.Marshal(item)
	if err != nil {
		slog.Error("unmarshalOutputItem: marshal failed", "error", err)
		return types.ResponsesOutputItem{}
	}
	var out types.ResponsesOutputItem
	if err := json.Unmarshal(b, &out); err != nil {
		slog.Error("unmarshalOutputItem: unmarshal failed", "error", err)
	}
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
		// Cursor streams internal planning commentary as output messages with
		// phase="commentary". These are not visible to the end user and must
		// not be included in the stored conversation history, or they would be
		// re-sent as assistant context on the next turn.
		if strings.EqualFold(strings.TrimSpace(item.Phase), "commentary") {
			return types.ResponsesInputItem{}, false
		}
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

// moveResponsesSystemMessagesToInstructions extracts text-only system messages
// into the top-level instructions field. The Responses API does not accept a
// "system" role in the input array, so system content must be moved to
// instructions. Non-text system messages (e.g. those with image parts) cannot
// be expressed as instructions and are demoted to "user" role as a best-effort
// fallback.
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
