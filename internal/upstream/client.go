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
		logJSON("OUTBOUND >> ChatGPT Responses API payload", payload)
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

	return &Response{
		StatusCode: resp.StatusCode,
		Body:       resp,
		Headers:    resp.Header,
	}, nil
}

func logJSON(prefix string, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		slog.Info(prefix, "data", fmt.Sprintf("%v", v))
		return
	}
	slog.Info(prefix + "\n" + string(data))
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
