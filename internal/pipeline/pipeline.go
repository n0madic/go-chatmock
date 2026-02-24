package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/codec"
	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/normalize"
	"github.com/n0madic/go-chatmock/internal/state"
	"github.com/n0madic/go-chatmock/internal/stream"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

// Pipeline orchestrates request processing through the
// decode → normalize → upstream → translate → encode flow.
type Pipeline struct {
	Config   *config.ServerConfig
	Store    *state.Store
	Upstream *upstream.Client
	Registry *models.Registry
}

// Execute processes a request body through the full normalization pipeline.
// route is "chat" or "responses" (from the URL path).
// enc is the format-specific encoder for writing responses.
// chatEnc is the Chat encoder used when format resolution picks chat.
// responsesEnc is the Responses encoder used when format resolution picks responses.
func (p *Pipeline) Execute(
	ctx *RequestContext,
	w http.ResponseWriter,
	body []byte,
	route string,
	chatEnc codec.Encoder,
	responsesEnc codec.Encoder,
) {
	writeErr := func(status int, msg string) {
		chatEnc.WriteError(w, status, msg)
	}

	req, nerr := normalize.Enrich(body, route, p.Config, p.Store)
	if nerr != nil {
		writeErr(nerr.StatusCode, nerr.Message)
		return
	}

	if ok, hint := p.Registry.IsKnownModel(req.Model); !ok && p.Config.DebugModel == "" {
		msg := "model " + req.Model + " is not available via this endpoint"
		if hint != "" {
			msg += "; available models: " + hint
		}
		writeErr(http.StatusBadRequest, msg)
		return
	}

	p.logNormalizedRequest(route, req, ctx.SessionID)

	upReq := &upstream.Request{
		Model:             req.Model,
		Instructions:      req.Instructions,
		InputItems:        req.InputItems,
		Tools:             req.Tools,
		ToolChoice:        req.ToolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
		Include:           req.Include,
		Store:             req.StoreForUpstream,
		ReasoningParam:    req.ReasoningParam,
		SessionID:         ctx.SessionID,
	}

	resp, upErr := p.Upstream.DoWithRetry(ctx.Context, upReq, req.HadResponsesTools, req.BaseTools)
	if upErr != nil {
		writeErr(upErr.StatusCode, upErr.Error())
		return
	}

	outputModel := req.RequestedModel
	if outputModel == "" {
		outputModel = req.Model
	}

	// Select encoder based on response format
	enc := chatEnc
	if req.ResponseFormat == "responses" {
		enc = responsesEnc
	}

	if req.Stream {
		p.handleStream(w, resp, enc, outputModel, req, ctx)
		return
	}
	p.handleCollected(w, resp, enc, outputModel, req)
}

// handleStream processes a streaming response.
func (p *Pipeline) handleStream(
	w http.ResponseWriter,
	resp *upstream.Response,
	enc codec.Encoder,
	outputModel string,
	req *types.CanonicalRequest,
	ctx *RequestContext,
) {
	enc.WriteStreamHeaders(w, resp.StatusCode)

	// Capture SSE bytes via TeeReader for state extraction after streaming
	var rawSSE bytes.Buffer
	teeBody := newTeeReadCloser(resp.Body.Body, &rawSSE)
	sseReader := stream.NewReader(teeBody)

	translator := enc.StreamTranslator(w, outputModel, codec.StreamOpts{
		ReasoningCompat: p.Config.ReasoningCompat,
		IncludeUsage:    req.IncludeUsage,
		CreatedAt:       ctx.CreatedAt,
	})
	translator.Translate(sseReader)
	teeBody.Close()

	// Extract state from captured SSE bytes
	p.storeStateFromSSE(rawSSE.Bytes(), req.InputItems, req.Instructions, req.ConversationID)
}

// handleCollected processes a non-streaming response.
func (p *Pipeline) handleCollected(
	w http.ResponseWriter,
	resp *upstream.Response,
	enc codec.Encoder,
	outputModel string,
	req *types.CanonicalRequest,
) {
	defer resp.Body.Body.Close()

	collected := collectFullResponse(resp.Body.Body)
	collected.RawResponse = map[string]any{
		"_reasoning_compat": p.Config.ReasoningCompat,
	}

	// Store state from collected data
	p.storeStateFromCollected(collected, req.InputItems, req.Instructions, req.ConversationID)

	if collected.ErrorMessage != "" {
		enc.WriteError(w, http.StatusBadGateway, collected.ErrorMessage)
		return
	}

	enc.WriteCollected(w, resp.StatusCode, collected, outputModel)
}

// collectFullResponse reads an upstream SSE stream and assembles a CollectedResponse
// with all data needed for both format encoding and state storage.
func collectFullResponse(body io.Reader) *codec.CollectedResponse {
	reader := stream.NewReader(io.NopCloser(body))
	out := &codec.CollectedResponse{}

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		if id := stream.ResponseIDFromEvent(evt.Data); id != "" {
			out.ResponseID = id
		}
		if usage := stream.ExtractUsageFromEvent(evt.Data); usage != nil {
			out.Usage = usage
		}

		switch evt.Type {
		case "response.output_text.delta":
			delta, _ := evt.Data["delta"].(string)
			out.FullText += delta
		case "response.reasoning_summary_text.delta":
			delta, _ := evt.Data["delta"].(string)
			out.ReasoningSummary += delta
		case "response.reasoning_text.delta":
			delta, _ := evt.Data["delta"].(string)
			out.ReasoningFull += delta
		case "response.output_item.done":
			item, _ := evt.Data["item"].(map[string]any)
			if item != nil {
				out.OutputItems = append(out.OutputItems, unmarshalOutputItem(item))
				if tc, ok := stream.FunctionToolCallFromOutputItem(item); ok {
					out.ToolCalls = append(out.ToolCalls, tc)
				}
			}
		case "response.failed":
			out.ErrorMessage = stream.ResponseErrorMessageFromEvent(evt.Data)
			if out.ErrorMessage == "" {
				out.ErrorMessage = "response.failed"
			}
			return out
		case "response.completed":
			if r, ok := evt.Data["response"].(map[string]any); ok {
				if out.RawResponse == nil {
					out.RawResponse = map[string]any{}
				}
				out.RawResponse["_upstream_response"] = r
			}
			return out
		}
	}

	return out
}

// storeStateFromSSE parses raw SSE bytes and stores conversation state.
func (p *Pipeline) storeStateFromSSE(raw []byte, requestInput []types.ResponsesInputItem, instructions string, conversationID string) {
	if len(raw) == 0 {
		return
	}

	reader := stream.NewReader(io.NopCloser(bytes.NewReader(raw)))
	var responseID string
	var outputItems []types.ResponsesOutputItem

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		if id := stream.ResponseIDFromEvent(evt.Data); id != "" {
			responseID = id
		}
		if evt.Type != "response.output_item.done" {
			continue
		}
		item, _ := evt.Data["item"].(map[string]any)
		if item != nil {
			outputItems = append(outputItems, unmarshalOutputItem(item))
		}
	}

	if responseID == "" {
		return
	}

	delta := outputItemsToInputItems(outputItems)
	calls := extractFunctionCalls(delta)

	combined := appendContextHistory(requestInput, delta)
	p.Store.PutSnapshot(responseID, combined, calls)
	p.Store.PutInstructions(responseID, instructions)
	p.Store.PutConversationLatest(conversationID, responseID)
}

// storeStateFromCollected stores conversation state from a collected response.
func (p *Pipeline) storeStateFromCollected(collected *codec.CollectedResponse, requestInput []types.ResponsesInputItem, instructions string, conversationID string) {
	if collected.ResponseID == "" {
		return
	}

	delta := outputItemsToInputItems(collected.OutputItems)

	// If no output items but we have text/tool calls (chat format), synthesize
	if len(delta) == 0 {
		if txt := strings.TrimSpace(collected.FullText); txt != "" {
			delta = append(delta, types.ResponsesInputItem{
				Type:    "message",
				Role:    "assistant",
				Content: []types.ResponsesContent{{Type: "output_text", Text: txt}},
			})
		}
		for _, tc := range collected.ToolCalls {
			callID := strings.TrimSpace(tc.ID)
			name := strings.TrimSpace(tc.Function.Name)
			if callID == "" || name == "" {
				continue
			}
			delta = append(delta, types.ResponsesInputItem{
				Type:      "function_call",
				CallID:    callID,
				Name:      name,
				Arguments: tc.Function.Arguments,
			})
		}
	}

	calls := extractFunctionCalls(delta)
	combined := appendContextHistory(requestInput, delta)
	p.Store.PutSnapshot(collected.ResponseID, combined, calls)
	p.Store.PutInstructions(collected.ResponseID, instructions)
	p.Store.PutConversationLatest(conversationID, collected.ResponseID)
}

// logNormalizedRequest logs the normalized request details.
func (p *Pipeline) logNormalizedRequest(route string, req *types.CanonicalRequest, sessionID string) {
	if !p.Config.Verbose {
		return
	}

	reasoningEffort := ""
	reasoningSummary := ""
	if req.ReasoningParam != nil {
		reasoningEffort = req.ReasoningParam.Effort
		reasoningSummary = req.ReasoningParam.Summary
	}

	if req.StoreForced {
		slog.Warn("client requested store=true; forcing store=false for upstream compatibility")
	}

	if route == "chat" {
		slog.Info("openai.chat.request",
			"requested_model", req.RequestedModel,
			"upstream_model", req.Model,
			"stream", req.Stream,
			"include_usage", req.IncludeUsage,
			"messages", req.MessagesCount,
			"input_items", len(req.InputItems),
			"input_source", req.InputSource,
			"tools", len(req.Tools),
			"tool_choice", summarizeToolChoice(req.ToolChoice),
			"responses_tools", boolToInt(req.HadResponsesTools),
			"default_web_search", req.DefaultWebSearchApplied,
			"parallel_tool_calls", req.ParallelToolCalls,
			"include_count", len(req.Include),
			"store_requested", boolPtrState(req.StoreRequested),
			"store_upstream", boolPtrState(req.StoreForUpstream),
			"reasoning_effort", reasoningEffort,
			"reasoning_summary", reasoningSummary,
			"prompt_fallback", req.UsedPromptFallback,
			"input_fallback", req.UsedInputFallback,
			"previous_response_id", req.PreviousResponseID != "",
			"previous_response_id_auto", req.AutoPreviousResponseID,
			"conversation_id", req.ConversationID != "",
			"session_override", sessionID != "",
		)
	} else {
		slog.Info("responses.request",
			"requested_model", req.RequestedModel,
			"upstream_model", req.Model,
			"stream", req.Stream,
			"input_items", len(req.InputItems),
			"input_source", req.InputSource,
			"tools", len(req.Tools),
			"tool_choice", summarizeToolChoice(req.ToolChoice),
			"parallel_tool_calls", req.ParallelToolCalls,
			"include_count", len(req.Include),
			"instructions_chars", len(req.Instructions),
			"previous_response_id", req.PreviousResponseID != "",
			"previous_response_id_auto", req.AutoPreviousResponseID,
			"conversation_id", req.ConversationID != "",
			"store_requested", boolPtrState(req.StoreRequested),
			"store_upstream", boolPtrState(req.StoreForUpstream),
			"default_web_search", req.DefaultWebSearchApplied,
			"reasoning_effort", reasoningEffort,
			"reasoning_summary", reasoningSummary,
			"session_override", sessionID != "",
		)
	}
}

// --- helpers ---

// RequestContext carries per-request metadata that isn't part of the canonical request.
type RequestContext struct {
	Context   context.Context
	SessionID string
	CreatedAt string // RFC3339 timestamp for Ollama
}

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
	combined := cloneInputItems(base)
	if len(delta) > 0 {
		combined = append(combined, cloneInputItems(delta)...)
	}
	return combined
}

func cloneInputItems(items []types.ResponsesInputItem) []types.ResponsesInputItem {
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

func extractFunctionCalls(items []types.ResponsesInputItem) []state.FunctionCall {
	if len(items) == 0 {
		return nil
	}
	var calls []state.FunctionCall
	for _, it := range items {
		if it.Type != "function_call" || strings.TrimSpace(it.CallID) == "" || strings.TrimSpace(it.Name) == "" {
			continue
		}
		calls = append(calls, state.FunctionCall{
			CallID:    strings.TrimSpace(it.CallID),
			Name:      strings.TrimSpace(it.Name),
			Arguments: it.Arguments,
		})
	}
	return calls
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
		return "auto"
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

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

type teeReadCloser struct {
	reader io.Reader
	closer io.Closer
}

func newTeeReadCloser(src io.ReadCloser, dst io.Writer) io.ReadCloser {
	return &teeReadCloser{
		reader: io.TeeReader(src, dst),
		closer: src,
	}
}

func (t *teeReadCloser) Read(p []byte) (int, error) {
	return t.reader.Read(p)
}

func (t *teeReadCloser) Close() error {
	if t.closer == nil {
		return nil
	}
	return t.closer.Close()
}
