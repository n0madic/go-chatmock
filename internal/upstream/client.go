package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/n0madic/go-chatmock/internal/auth"
	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/session"
	"github.com/n0madic/go-chatmock/internal/types"
)

// upstreamHTTPTimeout is the maximum time allowed for the upstream SSE request.
// SSE streams can be long-lived, so we use a generous timeout.
const upstreamHTTPTimeout = 5 * time.Minute

// defaultHTTPClient is the shared HTTP client for upstream requests with a timeout.
var defaultHTTPClient = &http.Client{Timeout: upstreamHTTPTimeout}

// Request holds the parameters for an upstream Responses API request.
type Request struct {
	Model             string
	Instructions      string
	InputItems        []types.ResponsesInputItem
	Tools             []types.ResponsesTool
	ToolChoice        any
	ParallelToolCalls bool
	Include           []string
	Store             *bool
	ReasoningParam    *types.ReasoningParam
	SessionID         string // Client-supplied session ID override
}

// Response wraps the upstream HTTP response.
type Response struct {
	StatusCode int
	Body       *http.Response
	Headers    http.Header
}

// Client makes requests to the ChatGPT backend.
type Client struct {
	TokenManager *auth.TokenManager
	Sessions     *session.SessionStore
	HTTPClient   *http.Client
	Verbose      bool
	Debug        bool
	dumpMu       sync.Mutex
}

// NewClient creates a new upstream client.
func NewClient(tm *auth.TokenManager, verbose, debug bool) *Client {
	return &Client{
		TokenManager: tm,
		Sessions:     session.NewSessionStore(),
		HTTPClient:   defaultHTTPClient,
		Verbose:      verbose,
		Debug:        debug,
	}
}

// Do sends a Responses API request to ChatGPT backend and returns the streaming response.
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	accessToken, accountID, err := c.TokenManager.GetEffectiveAuth()
	if err != nil || accessToken == "" || accountID == "" {
		return nil, auth.ErrNoCredentials
	}

	sessionID := c.Sessions.EnsureSessionID(req.Instructions, req.InputItems, req.SessionID)

	// Normalize tool_choice for upstream
	toolChoice := req.ToolChoice
	switch tc := toolChoice.(type) {
	case string:
		if tc != "auto" && tc != "none" {
			toolChoice = "auto"
		}
	case map[string]any:
		// keep as-is
	default:
		toolChoice = "auto"
	}

	// Build SDK payload
	includes := mergeIncludes(req.Include, req.ReasoningParam != nil)

	payload := responses.ResponseNewParams{
		Model:             req.Model,
		Input:             responsesInputItemsToSDKInput(req.InputItems),
		Tools:             responsesToolsToSDKTools(req.Tools),
		ToolChoice:        toolChoiceToSDK(toolChoice),
		ParallelToolCalls: openai.Bool(req.ParallelToolCalls),
		Include:           includesToSDK(includes),
		PromptCacheKey:    openai.String(sessionID),
	}
	if req.Instructions != "" {
		payload.Instructions = openai.String(req.Instructions)
	}
	if req.Store != nil {
		payload.Store = openai.Bool(*req.Store)
	}
	if req.ReasoningParam != nil {
		payload.Reasoning = reasoningToSDK(req.ReasoningParam)
	}

	body, err := marshalWithStream(&payload)
	if err != nil {
		return nil, err
	}

	if c.Verbose {
		reasoningEffort := ""
		reasoningSummary := ""
		if req.ReasoningParam != nil {
			reasoningEffort = req.ReasoningParam.Effort
			reasoningSummary = req.ReasoningParam.Summary
		}
		slog.Info("upstream.request",
			"model", req.Model,
			"input_items", len(req.InputItems),
			"tools", len(req.Tools),
			"tool_choice", summarizeToolChoice(toolChoice),
			"parallel_tool_calls", req.ParallelToolCalls,
			"include_count", len(includes),
			"store", boolPtrState(req.Store),
			"reasoning_effort", reasoningEffort,
			"reasoning_summary", reasoningSummary,
			"instructions_chars", len(req.Instructions),
			"session_id", sessionID,
		)
	}

	return c.sendPayload(ctx, body, sessionID, accessToken, accountID)
}

// DoRaw sends a pre-built JSON payload (with stream=true injected) to the
// ChatGPT backend. This is used for the Responses API passthrough path where
// the client request is forwarded with minimal transformation, preserving all
// SDK fields (metadata, prompt_cache_retention, custom tool formats, etc.).
func (c *Client) DoRaw(ctx context.Context, body []byte, sessionID string) (*Response, error) {
	accessToken, accountID, err := c.TokenManager.GetEffectiveAuth()
	if err != nil || accessToken == "" || accountID == "" {
		return nil, auth.ErrNoCredentials
	}

	if c.Verbose {
		slog.Info("upstream.request.raw",
			"body_len", len(body),
			"session_id", sessionID,
		)
	}

	return c.sendPayload(ctx, body, sessionID, accessToken, accountID)
}

// sendPayload is the shared HTTP send logic for both Do and DoRaw.
func (c *Client) sendPayload(ctx context.Context, body []byte, sessionID, accessToken, accountID string) (*Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", config.ResponsesURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	config.ApplyCodexDefaultHeaders(httpReq.Header)
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("ChatGPT-Account-ID", accountID)
	// session_id is sent both in the payload as prompt_cache_key and here as a
	// header. The header form is what the ChatGPT backend uses for routing and
	// caching; the payload field may be required by older API versions.
	httpReq.Header.Set("session_id", sessionID)

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream ChatGPT request failed: %w", err)
	}
	c.dumpUpstreamResponse(resp)
	if c.Verbose {
		requestID := upstreamRequestID(resp.Header)
		attrs := []any{"status", resp.StatusCode}
		if requestID != "" {
			attrs = append(attrs, "request_id", requestID)
		}
		slog.Info("upstream.response", attrs...)
	}

	return &Response{
		StatusCode: resp.StatusCode,
		Body:       resp,
		Headers:    resp.Header,
	}, nil
}

// marshalWithStream marshals an SDK payload with stream=true injected.
// The SDK ResponseNewParams does not have a stream field, so we use
// SetExtraFields to add it before marshaling.
func marshalWithStream(payload *responses.ResponseNewParams) ([]byte, error) {
	payload.SetExtraFields(map[string]any{"stream": true})
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}
	return body, nil
}

func summarizeToolChoice(choice any) string {
	switch v := choice.(type) {
	case nil:
		return "auto"
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return "auto"
		}
		return v
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func upstreamRequestID(headers http.Header) string {
	if headers == nil {
		return ""
	}
	return firstNonEmpty(
		headers.Get("x-request-id"),
		headers.Get("x-openai-request-id"),
		headers.Get("x-oai-request-id"),
		headers.Get("openai-request-id"),
		headers.Get("request-id"),
		headers.Get("cf-ray"),
	)
}

// mergeIncludes combines client-requested includes with the reasoning content
// include that is required for the summary stream events to arrive. The
// reasoning include is only added when a reasoning parameter is active to avoid
// bloating responses with encrypted reasoning tokens when the model is not in
// reasoning mode (the encrypted payload cannot be decoded and is useless to us).
func mergeIncludes(clientInclude []string, includeReasoning bool) []string {
	var merged []string
	seen := make(map[string]struct{})

	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		merged = append(merged, v)
	}

	for _, v := range clientInclude {
		add(v)
	}
	if includeReasoning {
		add("reasoning.encrypted_content")
	}

	return merged
}
