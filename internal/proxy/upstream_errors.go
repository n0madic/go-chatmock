package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/types"
)

func formatUpstreamError(statusCode int, rawBody []byte) string {
	status := fmt.Sprintf("%d", statusCode)
	if text := http.StatusText(statusCode); text != "" {
		status = fmt.Sprintf("%d %s", statusCode, text)
	}

	if msg := extractUpstreamErrorMessage(rawBody); msg != "" {
		return fmt.Sprintf("Upstream returned HTTP %s: %s", status, msg)
	}

	if preview := compactBodyPreview(rawBody, 280); preview != "" {
		return fmt.Sprintf("Upstream returned HTTP %s with unparsed body: %s", status, preview)
	}

	return fmt.Sprintf("Upstream returned HTTP %s with empty error body", status)
}

func formatUpstreamErrorWithHeaders(statusCode int, rawBody []byte, headers http.Header) string {
	msg := formatUpstreamError(statusCode, rawBody)
	if headers == nil {
		return msg
	}
	reqID := extractUpstreamRequestID(headers)
	if reqID == "" {
		return msg
	}
	return fmt.Sprintf("%s (request_id: %s)", msg, reqID)
}

func extractUpstreamRequestID(headers http.Header) string {
	if headers == nil {
		return ""
	}
	return firstNonEmptyTrimmed(
		headers.Get("x-request-id"),
		headers.Get("x-openai-request-id"),
		headers.Get("x-oai-request-id"),
		headers.Get("openai-request-id"),
		headers.Get("request-id"),
		headers.Get("cf-ray"),
	)
}

func extractUpstreamErrorMessage(rawBody []byte) string {
	trimmed := strings.TrimSpace(string(rawBody))
	if trimmed == "" {
		return ""
	}

	var errResp types.ErrorResponse
	if err := json.Unmarshal([]byte(trimmed), &errResp); err == nil && strings.TrimSpace(errResp.Error.Message) != "" {
		return strings.TrimSpace(errResp.Error.Message)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return ""
	}

	return extractErrorMessageFromMap(payload)
}

func extractErrorMessageFromMap(payload map[string]any) string {
	if payload == nil {
		return ""
	}

	if v := trimmedString(payload["message"]); v != "" {
		return v
	}
	if v := trimmedString(payload["detail"]); v != "" {
		return v
	}
	if v := trimmedString(payload["error_description"]); v != "" {
		return v
	}
	if v := trimmedString(payload["title"]); v != "" {
		return v
	}
	if v := trimmedString(payload["reason"]); v != "" {
		return v
	}

	if nested, ok := payload["error"].(map[string]any); ok {
		if msg := extractErrorMessageFromMap(nested); msg != "" {
			return msg
		}
	}
	if v := trimmedString(payload["error"]); v != "" {
		return v
	}

	if list, ok := payload["errors"].([]any); ok {
		for _, item := range list {
			if entry, ok := item.(map[string]any); ok {
				if msg := extractErrorMessageFromMap(entry); msg != "" {
					return msg
				}
			}
			if v := trimmedString(item); v != "" {
				return v
			}
		}
	}

	return ""
}

func trimmedString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func compactBodyPreview(rawBody []byte, maxLen int) string {
	trimmed := strings.TrimSpace(string(rawBody))
	if trimmed == "" {
		return ""
	}

	clean := strings.Join(strings.Fields(trimmed), " ")
	if len(clean) <= maxLen {
		return clean
	}
	return clean[:maxLen] + "..."
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}
