package codec

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/types"
)

// WriteJSON writes a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteOpenAIError writes an OpenAI-format error response.
func WriteOpenAIError(w http.ResponseWriter, status int, message string) {
	slog.Error("request failed", "status", status, "error", message)
	WriteJSON(w, status, types.ErrorResponse{Error: types.ErrorDetail{Message: message}})
}

// WriteAnthropicError writes an Anthropic-format error response.
func WriteAnthropicError(w http.ResponseWriter, status int, errorType, message string) {
	if strings.TrimSpace(errorType) == "" {
		errorType = "api_error"
	}
	if strings.TrimSpace(message) == "" {
		message = http.StatusText(status)
	}
	WriteJSON(w, status, types.AnthropicErrorResponse{
		Type: "error",
		Error: types.AnthropicErrorBody{
			Type:    errorType,
			Message: message,
		},
	})
}

// WriteOllamaError writes an Ollama-format error response.
func WriteOllamaError(w http.ResponseWriter, status int, message string) {
	slog.Error("request failed", "status", status, "error", message)
	WriteJSON(w, status, map[string]string{"error": message})
}

// FormatUpstreamError formats an error from the upstream response.
func FormatUpstreamError(statusCode int, rawBody []byte) string {
	status := fmt.Sprintf("%d", statusCode)
	if text := http.StatusText(statusCode); text != "" {
		status = fmt.Sprintf("%d %s", statusCode, text)
	}
	if msg := ExtractUpstreamErrorMessage(rawBody); msg != "" {
		return fmt.Sprintf("Upstream returned HTTP %s: %s", status, msg)
	}
	if preview := compactBodyPreview(rawBody, 280); preview != "" {
		return fmt.Sprintf("Upstream returned HTTP %s with unparsed body: %s", status, preview)
	}
	return fmt.Sprintf("Upstream returned HTTP %s with empty error body", status)
}

// FormatUpstreamErrorWithHeaders includes request ID headers in the error.
func FormatUpstreamErrorWithHeaders(statusCode int, rawBody []byte, headers http.Header) string {
	msg := FormatUpstreamError(statusCode, rawBody)
	if headers == nil {
		return msg
	}
	reqID := extractUpstreamRequestID(headers)
	if reqID == "" {
		return msg
	}
	return fmt.Sprintf("%s (request_id: %s)", msg, reqID)
}

// ExtractUpstreamErrorMessage extracts the error message from an upstream error body.
func ExtractUpstreamErrorMessage(rawBody []byte) string {
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
	for _, key := range []string{"message", "detail", "error_description", "title", "reason"} {
		if v, ok := payload[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if nested, ok := payload["error"].(map[string]any); ok {
		if msg := extractErrorMessageFromMap(nested); msg != "" {
			return msg
		}
	}
	if v, ok := payload["error"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if list, ok := payload["errors"].([]any); ok {
		for _, item := range list {
			if entry, ok := item.(map[string]any); ok {
				if msg := extractErrorMessageFromMap(entry); msg != "" {
					return msg
				}
			}
			if v, ok := item.(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
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

func extractUpstreamRequestID(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for _, key := range []string{"x-request-id", "x-openai-request-id", "x-oai-request-id", "openai-request-id", "request-id", "cf-ray"} {
		if v := strings.TrimSpace(headers.Get(key)); v != "" {
			return v
		}
	}
	return ""
}
