package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/n0madic/go-chatmock/internal/auth"
	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/session"
	"github.com/n0madic/go-chatmock/internal/types"
)

// Request holds the parameters for an upstream Responses API request.
type Request struct {
	Model             string
	Instructions      string
	InputItems        []types.ResponsesInputItem
	Tools             []types.ResponsesTool
	ToolChoice        any
	ParallelToolCalls bool
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

	var include []string
	if req.ReasoningParam != nil {
		include = append(include, "reasoning.encrypted_content")
	}

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
		Store:             false,
		Stream:            true,
		PromptCacheKey:    sessionID,
	}
	if len(include) > 0 {
		payload.Include = include
	}
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

	resp, err := http.DefaultClient.Do(httpReq)
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
