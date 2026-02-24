package state

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/n0madic/go-chatmock/internal/types"
)

// RestoreFunctionCallContext is a client-side polyfill for the Responses API
// previous_response_id feature.
func (s *Store) RestoreFunctionCallContext(inputItems []types.ResponsesInputItem, previousResponseID string, prependPreviousContext bool) ([]types.ResponsesInputItem, error) {
	effectiveInput := cloneInputItems(inputItems)

	if previousResponseID != "" && prependPreviousContext {
		previousContext, hasContext := s.GetContext(previousResponseID)
		if hasContext && len(previousContext) > 0 {
			if !hasResponsesInputPrefix(effectiveInput, previousContext) {
				effectiveInput = append(cloneInputItems(previousContext), effectiveInput...)
			}
		} else {
			if !s.Exists(previousResponseID) {
				return nil, fmt.Errorf("unknown or expired previous_response_id %q", previousResponseID)
			}
		}
	}

	missingCallIDs := missingFunctionCallOutputIDs(effectiveInput)
	if len(missingCallIDs) == 0 {
		return effectiveInput, nil
	}
	if previousResponseID == "" {
		return nil, fmt.Errorf(
			"invalid tool state: function_call_output references call_id(s) with no matching function_call in input: %s; "+
				"send previous_response_id from the response that created these tool calls, or include matching function_call items in input",
			strings.Join(missingCallIDs, ", "),
		)
	}

	storedCalls, ok := s.Get(previousResponseID)
	if !ok {
		return nil, fmt.Errorf(
			"unknown or expired previous_response_id %q; unable to resolve call_id(s): %s",
			previousResponseID,
			strings.Join(missingCallIDs, ", "),
		)
	}

	callByID := make(map[string]FunctionCall)
	for _, c := range storedCalls {
		if c.CallID == "" {
			continue
		}
		callByID[c.CallID] = c
	}

	var unresolved []string
	for _, callID := range missingCallIDs {
		if _, ok := callByID[callID]; !ok {
			unresolved = append(unresolved, callID)
		}
	}
	if len(unresolved) > 0 {
		return nil, fmt.Errorf(
			"previous_response_id %q does not contain required call_id(s): %s; include matching function_call items in input",
			previousResponseID,
			strings.Join(unresolved, ", "),
		)
	}

	missingSet := make(map[string]struct{}, len(missingCallIDs))
	for _, callID := range missingCallIDs {
		missingSet[callID] = struct{}{}
	}

	inserted := make(map[string]struct{})
	augmented := make([]types.ResponsesInputItem, 0, len(effectiveInput)+len(missingCallIDs))
	for _, item := range effectiveInput {
		if item.Type == "function_call_output" && item.CallID != "" {
			if _, shouldInsert := missingSet[item.CallID]; shouldInsert {
				if _, alreadyInserted := inserted[item.CallID]; !alreadyInserted {
					call := callByID[item.CallID]
					augmented = append(augmented, types.ResponsesInputItem{
						Type:      "function_call",
						CallID:    call.CallID,
						Name:      call.Name,
						Arguments: call.Arguments,
					})
					inserted[item.CallID] = struct{}{}
				}
			}
		}
		augmented = append(augmented, item)
	}

	return augmented, nil
}

// RestorePreviousContext prepends stored context from a previous response ID
// to the current input items in the raw request (for passthrough path).
func (s *Store) RestorePreviousContext(raw map[string]any, previousResponseID string) (any, error) {
	ctx, ok := s.GetContext(previousResponseID)
	if !ok {
		return nil, nil
	}

	var currentItems []types.ResponsesInputItem
	if inputRaw, ok := raw["input"]; ok {
		inputBytes, err := json.Marshal(inputRaw)
		if err == nil {
			var str string
			if json.Unmarshal(inputBytes, &str) == nil {
				currentItems = []types.ResponsesInputItem{
					{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: str}}},
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

// IsUnsupportedParameterError checks if an error body indicates unsupported parameter.
func IsUnsupportedParameterError(rawBody []byte, param string) bool {
	msg := strings.ToLower(extractUpstreamErrorMessage(rawBody))
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "unsupported parameter") && strings.Contains(msg, strings.ToLower(strings.TrimSpace(param)))
}

// NormalizeStoreForUpstream keeps store handling compatible with upstream.
func NormalizeStoreForUpstream(requested *bool) (*bool, bool) {
	if requested != nil && *requested {
		return types.BoolPtr(false), true
	}
	return types.BoolPtr(false), false
}

func hasResponsesInputPrefix(items []types.ResponsesInputItem, prefix []types.ResponsesInputItem) bool {
	if len(prefix) == 0 {
		return true
	}
	if len(items) < len(prefix) {
		return false
	}
	for i := range prefix {
		if !responsesInputItemEqual(items[i], prefix[i]) {
			return false
		}
	}
	return true
}

func responsesInputItemEqual(a, b types.ResponsesInputItem) bool {
	return a.Type == b.Type &&
		a.Role == b.Role &&
		a.Name == b.Name &&
		a.Arguments == b.Arguments &&
		a.CallID == b.CallID &&
		a.Output == b.Output &&
		responsesContentSliceEqual(a.Content, b.Content)
}

func responsesContentSliceEqual(a, b []types.ResponsesContent) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func missingFunctionCallOutputIDs(items []types.ResponsesInputItem) []string {
	existingCalls := make(map[string]struct{})
	for _, item := range items {
		if item.Type == "function_call" && item.CallID != "" {
			existingCalls[item.CallID] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	var missing []string
	for _, item := range items {
		if item.Type != "function_call_output" || item.CallID == "" {
			continue
		}
		if _, ok := existingCalls[item.CallID]; ok {
			continue
		}
		if _, ok := seen[item.CallID]; ok {
			continue
		}
		seen[item.CallID] = struct{}{}
		missing = append(missing, item.CallID)
	}
	return missing
}

func extractUpstreamErrorMessage(rawBody []byte) string {
	trimmed := strings.TrimSpace(string(rawBody))
	if trimmed == "" {
		return ""
	}
	var errResp types.ErrorResponse
	if err := json.Unmarshal([]byte(trimmed), &errResp); err == nil && strings.TrimSpace(errResp.Error.Message) != "" {
		return strings.TrimSpace(errResp.Error.Message)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return ""
	}
	return extractErrorMessageFromMap(payload)
}

func extractErrorMessageFromMap(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	for _, key := range []string{"message", "detail", "error_description", "title", "reason"} {
		if v, ok := payload[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if nested, ok := payload["error"].(map[string]any); ok {
		if msg := extractErrorMessageFromMap(nested); msg != "" {
			return msg
		}
	}
	if v, ok := payload["error"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// ExtractFunctionCallFromOutputItem extracts a FunctionCall from an output item map.
func ExtractFunctionCallFromOutputItem(item map[string]any) (FunctionCall, bool) {
	if item == nil {
		return FunctionCall{}, false
	}
	itemType, _ := item["type"].(string)
	if itemType != "function_call" {
		return FunctionCall{}, false
	}
	callID, _ := item["call_id"].(string)
	callID = strings.TrimSpace(callID)
	if callID == "" {
		id, _ := item["id"].(string)
		callID = strings.TrimSpace(id)
	}
	name, _ := item["name"].(string)
	name = strings.TrimSpace(name)
	args, _ := item["arguments"].(string)
	if callID == "" || name == "" {
		return FunctionCall{}, false
	}
	return FunctionCall{CallID: callID, Name: name, Arguments: args}, true
}
