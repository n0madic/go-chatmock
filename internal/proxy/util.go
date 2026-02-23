package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/limits"
	"github.com/n0madic/go-chatmock/internal/reasoning"
	"github.com/n0madic/go-chatmock/internal/sse"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

// maxBodyBytes limits the size of incoming request bodies to prevent memory exhaustion.
const maxBodyBytes = 10 * 1024 * 1024 // 10 MB

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	slog.Error("request failed", "status", status, "error", message)
	writeJSON(w, status, types.ErrorResponse{Error: types.ErrorDetail{Message: message}})
}

func writeOllamaError(w http.ResponseWriter, status int, message string) {
	slog.Error("request failed", "status", status, "error", message)
	writeJSON(w, status, map[string]string{"error": message})
}

type requestErrorWriter func(http.ResponseWriter, int, string)

func readLimitedRequestBody(
	w http.ResponseWriter,
	r *http.Request,
	writeErr requestErrorWriter,
	readErrMsg string,
) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, readErrMsg)
		return nil, false
	}
	return body, true
}

func decodeJSONBody(
	w http.ResponseWriter,
	body []byte,
	dst any,
	writeErr requestErrorWriter,
	invalidJSONMsg string,
) bool {
	if err := json.Unmarshal(body, dst); err != nil {
		writeErr(w, http.StatusBadRequest, invalidJSONMsg)
		return false
	}
	return true
}

func parseJSONRequest(
	w http.ResponseWriter,
	r *http.Request,
	dst any,
	writeErr requestErrorWriter,
	readErrMsg string,
	invalidJSONMsg string,
) ([]byte, bool) {
	body, ok := readLimitedRequestBody(w, r, writeErr, readErrMsg)
	if !ok {
		return nil, false
	}
	if !decodeJSONBody(w, body, dst, writeErr, invalidJSONMsg) {
		return nil, false
	}
	return body, true
}

func summarizeToolChoice(choice any) string {
	switch v := choice.(type) {
	case nil:
		return "auto"
	case string:
		val := strings.TrimSpace(v)
		if val == "" {
			return "auto"
		}
		return val
	case map[string]any:
		kind, _ := v["type"].(string)
		if fn, ok := v["function"].(map[string]any); ok {
			if name, _ := fn["name"].(string); name != "" {
				if kind != "" {
					return kind + ":" + name
				}
				return "function:" + name
			}
		}
		if kind != "" {
			return kind
		}
		return "object"
	default:
		return fmt.Sprintf("%T", choice)
	}
}

func boolPtrState(v *bool) string {
	if v == nil {
		return "unset"
	}
	if *v {
		return "true"
	}
	return "false"
}

func buildReasoningWithModelFallback(
	cfg *config.ServerConfig,
	requestedModel string,
	normalizedModel string,
	reasoningOverrides *types.ReasoningParam,
) *types.ReasoningParam {
	if reasoningOverrides == nil {
		reasoningOverrides = reasoning.ExtractFromModelName(requestedModel)
	}
	return reasoning.BuildReasoningParam(
		cfg.ReasoningEffort,
		cfg.ReasoningSummary,
		reasoningOverrides,
		normalizedModel,
	)
}

func reasoningLogFields(reasoningParam *types.ReasoningParam) (effort string, summary string) {
	if reasoningParam == nil {
		return "", ""
	}
	return reasoningParam.Effort, reasoningParam.Summary
}

func writeSSEHeaders(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(status)
}

type collectTextResponseOptions struct {
	InitialResponseID string
	CollectUsage      bool
	CollectReasoning  bool
	CollectToolCalls  bool
	StopOnFailed      bool
}

type collectedTextResponse struct {
	ResponseID       string
	FullText         string
	ReasoningSummary string
	ReasoningFull    string
	ToolCalls        []types.ToolCall
	Usage            *types.Usage
	ErrorMessage     string
}

func collectTextResponseFromSSE(body io.ReadCloser, opts collectTextResponseOptions) collectedTextResponse {
	defer body.Close()

	out := collectedTextResponse{
		ResponseID: opts.InitialResponseID,
	}
	reader := sse.NewReader(body)

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		if id := responseIDFromEvent(evt.Data); id != "" {
			out.ResponseID = id
		}
		if opts.CollectUsage {
			if usage := types.ExtractUsageFromEvent(evt.Data); usage != nil {
				out.Usage = usage
			}
		}

		switch evt.Type {
		case "response.output_text.delta":
			delta, _ := evt.Data["delta"].(string)
			out.FullText += delta
		case "response.reasoning_summary_text.delta":
			if opts.CollectReasoning {
				delta, _ := evt.Data["delta"].(string)
				out.ReasoningSummary += delta
			}
		case "response.reasoning_text.delta":
			if opts.CollectReasoning {
				delta, _ := evt.Data["delta"].(string)
				out.ReasoningFull += delta
			}
		case "response.output_item.done":
			if opts.CollectToolCalls {
				item, _ := evt.Data["item"].(map[string]any)
				if tc, ok := functionToolCallFromOutputItem(item); ok {
					out.ToolCalls = append(out.ToolCalls, tc)
				}
			}
		case "response.failed":
			out.ErrorMessage = responseErrorMessageFromEvent(evt.Data)
			if out.ErrorMessage == "" {
				out.ErrorMessage = "response.failed"
			}
			if opts.StopOnFailed {
				return out
			}
		case "response.completed":
			return out
		}
	}

	return out
}

func functionToolCallFromOutputItem(item map[string]any) (types.ToolCall, bool) {
	if item == nil {
		return types.ToolCall{}, false
	}
	if itemType, _ := item["type"].(string); itemType != "function_call" {
		return types.ToolCall{}, false
	}

	callID := strings.TrimSpace(stringOrEmpty(item, "call_id"))
	if callID == "" {
		callID = strings.TrimSpace(stringOrEmpty(item, "id"))
	}
	name := strings.TrimSpace(stringOrEmpty(item, "name"))
	args := stringOrEmpty(item, "arguments")
	if callID == "" || name == "" {
		return types.ToolCall{}, false
	}

	return types.ToolCall{
		ID:       callID,
		Type:     "function",
		Function: types.FunctionCall{Name: name, Arguments: args},
	}, true
}

func stringOrEmpty(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func responseErrorMessageFromEvent(data map[string]any) string {
	resp, _ := data["response"].(map[string]any)
	if resp == nil {
		return ""
	}
	errObj, _ := resp["error"].(map[string]any)
	if errObj == nil {
		return ""
	}
	msg, _ := errObj["message"].(string)
	return strings.TrimSpace(msg)
}

func responseIDFromEvent(data map[string]any) string {
	resp, _ := data["response"].(map[string]any)
	if resp == nil {
		return ""
	}
	return stringFromAny(resp["id"])
}

func convertSystemToUser(messages []types.ChatMessage) {
	for i, m := range messages {
		if m.Role == "system" {
			messages[i] = types.ChatMessage{Role: "user", Content: m.Content}
			if i > 0 {
				msg := messages[i]
				copy(messages[1:i+1], messages[:i])
				messages[0] = msg
			}
			return
		}
	}
}

func boolVal(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func parseResponsesInputFromRaw(rawInput any) ([]types.ResponsesInputItem, string, bool) {
	if rawInput == nil {
		return nil, "", false
	}
	inputBytes, err := json.Marshal(rawInput)
	if err != nil {
		return nil, "", false
	}
	req := types.ResponsesRequest{Input: inputBytes}
	items, err := req.ParseInput()
	if err != nil {
		return nil, "", false
	}
	items, systemInstructions := moveResponsesSystemMessagesToInstructions(items)
	return items, systemInstructions, true
}

func parseResponsesStyleToolsFromRaw(rawTools any) []types.ResponsesTool {
	toolsSlice, ok := rawTools.([]any)
	if !ok || len(toolsSlice) == 0 {
		return nil
	}
	// Responses-style tools have top-level name/parameters fields.
	hasTopLevelName := false
	for _, raw := range toolsSlice {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := m["name"].(string); name != "" {
			hasTopLevelName = true
			break
		}
	}
	if !hasTopLevelName {
		return nil
	}
	toolBytes, err := json.Marshal(toolsSlice)
	if err != nil {
		return nil
	}
	var parsed []types.ResponsesTool
	if err := json.Unmarshal(toolBytes, &parsed); err != nil {
		return nil
	}
	var out []types.ResponsesTool
	for _, t := range parsed {
		switch t.Type {
		case "function":
			if strings.TrimSpace(t.Name) == "" {
				continue
			}
			if t.Parameters == nil {
				t.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			if t.Strict == nil {
				t.Strict = types.BoolPtr(false)
			}
		case "web_search", "web_search_preview":
			// pass through
		default:
			continue
		}
		out = append(out, t)
	}
	return out
}

// retryIfStoreUnsupported retries upstream requests after removing the "store"
// parameter when upstream rejects it as unsupported.
func (s *Server) retryIfStoreUnsupported(
	ctx context.Context,
	resp *upstream.Response,
	upReq *upstream.Request,
	warnMsg string,
) (nextResp *upstream.Response, errBody []byte, retried bool, err error) {
	errBody, _ = io.ReadAll(resp.Body.Body)
	resp.Body.Body.Close()

	if upReq.Store == nil || !isUnsupportedParameterError(errBody, "store") {
		return nil, errBody, false, nil
	}

	if warnMsg != "" {
		slog.Warn(warnMsg)
	}
	upReq.Store = nil

	nextResp, err = s.upstreamClient.Do(ctx, upReq)
	if err != nil {
		return nil, nil, true, err
	}
	limits.RecordFromResponse(nextResp.Headers)
	return nextResp, nil, true, nil
}

func (s *Server) doUpstreamWithResponsesToolsRetry(
	ctx context.Context,
	w http.ResponseWriter,
	upReq *upstream.Request,
	hadResponsesTools bool,
	baseTools []types.ResponsesTool,
	writeErrFn func(http.ResponseWriter, int, string),
) (*upstream.Response, bool) {
	resp, err := s.upstreamClient.Do(ctx, upReq)
	if err != nil {
		writeErrFn(w, http.StatusUnauthorized, err.Error())
		return nil, false
	}
	limits.RecordFromResponse(resp.Headers)

	if resp.StatusCode >= 400 {
		var ok bool
		resp, ok = s.doWithRetry(ctx, w, resp, upReq, hadResponsesTools, baseTools, writeErrFn)
		if !ok {
			return nil, false
		}
	}

	return resp, true
}

// doWithRetry handles an upstream 4xx response. If hadResponsesTools is true it
// retries the request after stripping the extra tools from upReq. On success it
// returns the new *upstream.Response and true; on failure it writes the error to
// w using writeErrFn and returns nil, false.
func (s *Server) doWithRetry(
	ctx context.Context,
	w http.ResponseWriter,
	resp *upstream.Response,
	upReq *upstream.Request,
	hadResponsesTools bool,
	baseTools []types.ResponsesTool,
	writeErrFn func(http.ResponseWriter, int, string),
) (*upstream.Response, bool) {
	errBody, _ := io.ReadAll(resp.Body.Body)
	resp.Body.Body.Close()

	latestStatus := resp.StatusCode
	if hadResponsesTools {
		upReq.Tools = baseTools
		resp2, err2 := s.upstreamClient.Do(ctx, upReq)
		if err2 != nil {
			writeErrFn(w, http.StatusBadGateway, "Upstream retry failed after removing responses_tools: "+err2.Error())
			return nil, false
		}
		if resp2 == nil {
			writeErrFn(w, http.StatusBadGateway, "Upstream retry failed after removing responses_tools: empty response")
			return nil, false
		}
		limits.RecordFromResponse(resp2.Headers)
		if resp2.StatusCode < 400 {
			return resp2, true
		}
		latestStatus = resp2.StatusCode
		errBody, _ = io.ReadAll(resp2.Body.Body)
		resp2.Body.Body.Close()
	}

	if upReq.Store != nil && isUnsupportedParameterError(errBody, "store") {
		slog.Warn("upstream rejected store parameter; retrying without store")
		upReq.Store = nil

		resp3, err3 := s.upstreamClient.Do(ctx, upReq)
		if err3 != nil {
			writeErrFn(w, http.StatusBadGateway, "Upstream retry failed after removing store: "+err3.Error())
			return nil, false
		}
		if resp3 == nil {
			writeErrFn(w, http.StatusBadGateway, "Upstream retry failed after removing store: empty response")
			return nil, false
		}
		limits.RecordFromResponse(resp3.Headers)
		if resp3.StatusCode < 400 {
			return resp3, true
		}
		latestStatus = resp3.StatusCode
		errBody, _ = io.ReadAll(resp3.Body.Body)
		resp3.Body.Body.Close()
	}

	writeErrFn(w, latestStatus, formatUpstreamError(latestStatus, errBody))
	return nil, false
}
