package proxy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/n0madic/go-chatmock/internal/responses-state"
	"github.com/n0madic/go-chatmock/internal/types"
)

func (s *Server) restoreFunctionCallContext(inputItems []types.ResponsesInputItem, previousResponseID string) ([]types.ResponsesInputItem, error) {
	effectiveInput := cloneResponsesInputItems(inputItems)

	if previousResponseID != "" {
		previousContext, hasContext := s.responsesState.GetContext(previousResponseID)
		if hasContext && len(previousContext) > 0 {
			if !hasResponsesInputPrefix(effectiveInput, previousContext) {
				effectiveInput = append(cloneResponsesInputItems(previousContext), effectiveInput...)
			}
		} else {
			// Backward compatibility for entries created before context storage existed.
			if !s.responsesState.Exists(previousResponseID) {
				return nil, fmt.Errorf("Unknown or expired previous_response_id %q.", previousResponseID)
			}
		}
	}

	missingCallIDs := missingFunctionCallOutputIDs(effectiveInput)
	if len(missingCallIDs) == 0 {
		return effectiveInput, nil
	}
	if previousResponseID == "" {
		return nil, fmt.Errorf(
			"Invalid tool state: function_call_output references call_id(s) with no matching function_call in input: %s. "+
				"Send previous_response_id from the response that created these tool calls, or include matching function_call items in input.",
			strings.Join(missingCallIDs, ", "),
		)
	}

	storedCalls, ok := s.responsesState.Get(previousResponseID)
	if !ok {
		return nil, fmt.Errorf(
			"Unknown or expired previous_response_id %q. Unable to resolve call_id(s): %s.",
			previousResponseID,
			strings.Join(missingCallIDs, ", "),
		)
	}

	callByID := make(map[string]responsesstate.FunctionCall)
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
			"previous_response_id %q does not contain required call_id(s): %s. Include matching function_call items in input.",
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

func hasResponsesInputPrefix(items []types.ResponsesInputItem, prefix []types.ResponsesInputItem) bool {
	if len(prefix) == 0 {
		return true
	}
	if len(items) < len(prefix) {
		return false
	}
	for i := range prefix {
		if !reflect.DeepEqual(items[i], prefix[i]) {
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

func isUnsupportedParameterError(rawBody []byte, param string) bool {
	msg := strings.ToLower(extractUpstreamErrorMessage(rawBody))
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "unsupported parameter") && strings.Contains(msg, strings.ToLower(strings.TrimSpace(param)))
}

func isToolCallOutputNotFoundError(rawBody []byte) bool {
	msg := strings.ToLower(extractUpstreamErrorMessage(rawBody))
	return strings.Contains(msg, "no tool call found for function call output with call_id")
}

// normalizeStoreForUpstream keeps store handling compatible with upstream:
// we always send store=false upstream, because true is rejected.
// Returns (valueToSend, forcedFromTrue).
func normalizeStoreForUpstream(requested *bool) (*bool, bool) {
	if requested != nil && *requested {
		return types.BoolPtr(false), true
	}
	return types.BoolPtr(false), false
}

func extractFunctionCallFromOutputItem(item map[string]any) (responsesstate.FunctionCall, bool) {
	if item == nil {
		return responsesstate.FunctionCall{}, false
	}

	itemType := stringFromAny(item["type"])
	if itemType != "function_call" {
		return responsesstate.FunctionCall{}, false
	}

	callID := stringFromAny(item["call_id"])
	if callID == "" {
		callID = stringFromAny(item["id"])
	}
	name := stringFromAny(item["name"])
	args := stringFromAny(item["arguments"])
	if callID == "" || name == "" {
		return responsesstate.FunctionCall{}, false
	}

	return responsesstate.FunctionCall{
		CallID:    callID,
		Name:      name,
		Arguments: args,
	}, true
}

func stringFromAny(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case nil:
		return ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
}
