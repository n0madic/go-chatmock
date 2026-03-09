package stream

import (
	"encoding/json"

	"github.com/n0madic/go-chatmock/internal/types"
)

// Int64FromAny converts a JSON-decoded numeric value to int64.
// Handles float64, int, int64, and json.Number (all common from json.Unmarshal).
func Int64FromAny(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// ExtractUsageFromEvent extracts a *Usage from a response.completed SSE event data map.
// Returns nil if no usage data is present.
func ExtractUsageFromEvent(data map[string]any) *types.Usage {
	resp, _ := data["response"].(map[string]any)
	if resp == nil {
		return nil
	}
	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		return nil
	}
	pt := Int64FromAny(usage["input_tokens"])
	ct := Int64FromAny(usage["output_tokens"])
	tt := Int64FromAny(usage["total_tokens"])
	if tt == 0 {
		tt = pt + ct
	}
	u := &types.Usage{
		PromptTokens:     pt,
		CompletionTokens: ct,
		TotalTokens:      tt,
	}

	if ctd, ok := usage["completion_tokens_details"].(map[string]any); ok && len(ctd) > 0 {
		u.CompletionTokensDetails = &types.CompletionTokensDetails{
			AcceptedPredictionTokens: Int64FromAny(ctd["accepted_prediction_tokens"]),
			AudioTokens:              Int64FromAny(ctd["audio_tokens"]),
			ReasoningTokens:          Int64FromAny(ctd["reasoning_tokens"]),
			RejectedPredictionTokens: Int64FromAny(ctd["rejected_prediction_tokens"]),
		}
	}
	if ptd, ok := usage["prompt_tokens_details"].(map[string]any); ok && len(ptd) > 0 {
		u.PromptTokensDetails = &types.PromptTokensDetails{
			AudioTokens:  Int64FromAny(ptd["audio_tokens"]),
			CachedTokens: Int64FromAny(ptd["cached_tokens"]),
		}
	}

	return u
}
