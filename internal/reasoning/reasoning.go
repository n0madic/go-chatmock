package reasoning

import (
	"strings"

	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/types"
)

// BuildReasoningParam constructs the reasoning parameter for the Responses API.
func BuildReasoningParam(baseEffort, baseSummary string, overrides *types.ReasoningParam, model string) *types.ReasoningParam {
	effort := strings.ToLower(strings.TrimSpace(baseEffort))
	summary := strings.ToLower(strings.TrimSpace(baseSummary))

	validEfforts := models.AllowedEfforts(model)
	validSummaries := map[string]bool{"auto": true, "concise": true, "detailed": true, "none": true}

	if overrides != nil {
		if e := strings.ToLower(strings.TrimSpace(overrides.Effort)); e != "" && validEfforts[e] {
			effort = e
		}
		if s := strings.ToLower(strings.TrimSpace(overrides.Summary)); s != "" && validSummaries[s] {
			summary = s
		}
	}

	if !validEfforts[effort] {
		effort = "medium"
	}
	if !validSummaries[summary] {
		summary = "auto"
	}

	r := &types.ReasoningParam{Effort: effort}
	if summary != "none" {
		r.Summary = summary
	}
	return r
}

// ExtractFromModelName infers reasoning overrides from a model name string.
func ExtractFromModelName(model string) *types.ReasoningParam {
	if model == "" {
		return nil
	}
	s := strings.ToLower(strings.TrimSpace(model))
	if s == "" {
		return nil
	}

	efforts := []string{"minimal", "low", "medium", "high", "xhigh"}

	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		maybe := strings.TrimSpace(s[idx+1:])
		for _, e := range efforts {
			if maybe == e {
				return &types.ReasoningParam{Effort: e}
			}
		}
	}

	for _, sep := range []string{"-", "_"} {
		for _, e := range efforts {
			if strings.HasSuffix(s, sep+e) {
				return &types.ReasoningParam{Effort: e}
			}
		}
	}

	return nil
}
