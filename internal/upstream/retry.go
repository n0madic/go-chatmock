package upstream

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/n0madic/go-chatmock/internal/codec"
	"github.com/n0madic/go-chatmock/internal/limits"
	"github.com/n0madic/go-chatmock/internal/state"
	"github.com/n0madic/go-chatmock/internal/types"
)

// UpstreamError represents a failed upstream request with error details.
type UpstreamError struct {
	StatusCode int
	Body       []byte
	Headers    http.Header
}

func (e *UpstreamError) Error() string {
	return codec.FormatUpstreamErrorWithHeaders(e.StatusCode, e.Body, e.Headers)
}

// DoWithRetry sends a request and retries on failure:
// 1. If hadResponsesTools is true and upstream rejects, retries with baseTools.
// 2. If store is set and upstream rejects it as unsupported, retries without store.
// Returns the successful response, or an UpstreamError with the final error details.
func (c *Client) DoWithRetry(
	ctx context.Context,
	req *Request,
	hadResponsesTools bool,
	baseTools []types.ResponsesTool,
) (*Response, *UpstreamError) {
	resp, err := c.Do(ctx, req)
	if err != nil {
		return nil, &UpstreamError{StatusCode: http.StatusUnauthorized, Body: []byte(err.Error())}
	}
	limits.RecordFromResponse(resp.Headers)

	if resp.StatusCode < 400 {
		return resp, nil
	}

	return c.retryOnFailure(ctx, resp, req, hadResponsesTools, baseTools)
}

// retryOnFailure handles upstream 4xx responses with retry strategies:
// 1. Strip responses_tools and retry if hadResponsesTools
// 2. Remove store parameter and retry if upstream rejects it
func (c *Client) retryOnFailure(
	ctx context.Context,
	resp *Response,
	req *Request,
	hadResponsesTools bool,
	baseTools []types.ResponsesTool,
) (*Response, *UpstreamError) {
	errBody, _ := io.ReadAll(resp.Body.Body)
	resp.Body.Body.Close()

	latestStatus := resp.StatusCode
	latestHeaders := resp.Headers

	// Strategy 1: retry without responses_tools
	if hadResponsesTools {
		req.Tools = baseTools
		resp2, err2 := c.Do(ctx, req)
		if err2 != nil {
			return nil, &UpstreamError{
				StatusCode: http.StatusBadGateway,
				Body:       []byte("Upstream retry failed after removing responses_tools: " + err2.Error()),
			}
		}
		limits.RecordFromResponse(resp2.Headers)
		if resp2.StatusCode < 400 {
			return resp2, nil
		}
		latestStatus = resp2.StatusCode
		latestHeaders = resp2.Headers
		errBody, _ = io.ReadAll(resp2.Body.Body)
		resp2.Body.Body.Close()
	}

	// Strategy 2: retry without store
	if req.Store != nil && state.IsUnsupportedParameterError(errBody, "store") {
		if c.Verbose {
			slog.Warn("upstream rejected store parameter; retrying without store")
		}
		req.Store = nil

		resp3, err3 := c.Do(ctx, req)
		if err3 != nil {
			return nil, &UpstreamError{
				StatusCode: http.StatusBadGateway,
				Body:       []byte("Upstream retry failed after removing store: " + err3.Error()),
			}
		}
		limits.RecordFromResponse(resp3.Headers)
		if resp3.StatusCode < 400 {
			return resp3, nil
		}
		latestStatus = resp3.StatusCode
		latestHeaders = resp3.Headers
		errBody, _ = io.ReadAll(resp3.Body.Body)
		resp3.Body.Body.Close()
	}

	return nil, &UpstreamError{
		StatusCode: latestStatus,
		Body:       errBody,
		Headers:    latestHeaders,
	}
}

// RetryIfStoreUnsupported checks if the upstream error is about an unsupported
// "store" parameter and retries without it. Used by the Anthropic path which
// has its own error handling flow.
func (c *Client) RetryIfStoreUnsupported(
	ctx context.Context,
	resp *Response,
	req *Request,
) (nextResp *Response, errBody []byte, retried bool, err error) {
	errBody, _ = io.ReadAll(resp.Body.Body)
	resp.Body.Body.Close()

	if req.Store == nil || !state.IsUnsupportedParameterError(errBody, "store") {
		return nil, errBody, false, nil
	}

	if c.Verbose {
		slog.Warn("upstream rejected store parameter; retrying without store")
	}
	req.Store = nil

	nextResp, err = c.Do(ctx, req)
	if err != nil {
		return nil, nil, true, err
	}
	limits.RecordFromResponse(nextResp.Headers)
	return nextResp, nil, true, nil
}
