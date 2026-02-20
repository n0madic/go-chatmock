package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n0madic/go-chatmock/internal/auth"
	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/session"
	"github.com/n0madic/go-chatmock/internal/types"
)

// upstreamHTTPTimeout is the maximum time allowed for the upstream SSE request.
// SSE streams can be long-lived, so we use a generous timeout.
const upstreamHTTPTimeout = 5 * time.Minute

// httpClient is the shared HTTP client for upstream requests with a timeout.
var httpClient = &http.Client{Timeout: upstreamHTTPTimeout}

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
	Verbose      bool
}

// NewClient creates a new upstream client.
func NewClient(tm *auth.TokenManager, verbose bool) *Client {
	return &Client{TokenManager: tm, Verbose: verbose}
}

// Do sends a Responses API request to ChatGPT backend and returns the streaming response.
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	accessToken, accountID, err := c.TokenManager.GetEffectiveAuth()
	if err != nil || accessToken == "" || accountID == "" {
		return nil, auth.ErrNoCredentials
	}

	sessionID := session.EnsureSessionID(req.Instructions, req.InputItems, req.SessionID)

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

	payload := types.UpstreamPayload{
		Model:             req.Model,
		Instructions:      req.Instructions,
		Input:             req.InputItems,
		Tools:             req.Tools,
		ToolChoice:        toolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
		Store:             req.Store,
		Stream:            true,
		PromptCacheKey:    sessionID,
	}
	payload.Include = mergeIncludes(req.Include, req.ReasoningParam != nil)
	if req.ReasoningParam != nil {
		payload.Reasoning = req.ReasoningParam
	}

	if c.Verbose {
		reasoningEffort := ""
		reasoningSummary := ""
		if payload.Reasoning != nil {
			reasoningEffort = payload.Reasoning.Effort
			reasoningSummary = payload.Reasoning.Summary
		}
		slog.Info("upstream.request",
			"model", payload.Model,
			"input_items", len(payload.Input),
			"tools", len(payload.Tools),
			"tool_choice", summarizeToolChoice(payload.ToolChoice),
			"parallel_tool_calls", payload.ParallelToolCalls,
			"include_count", len(payload.Include),
			"store", boolPtrState(payload.Store),
			"reasoning_effort", reasoningEffort,
			"reasoning_summary", reasoningSummary,
			"instructions_chars", len(payload.Instructions),
			"session_id", sessionID,
		)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", config.ResponsesURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("chatgpt-account-id", accountID)
	httpReq.Header.Set("OpenAI-Beta", "responses=experimental")
	httpReq.Header.Set("session_id", sessionID)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream ChatGPT request failed: %w", err)
	}
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
