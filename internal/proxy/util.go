package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/limits"
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
	if hadResponsesTools {
		upReq.Tools = baseTools
		resp2, err2 := s.upstreamClient.Do(ctx, upReq)
		if err2 == nil && resp2.StatusCode < 400 {
			limits.RecordFromResponse(resp2.Headers)
			resp.Body.Body.Close()
			return resp2, true
		}
		resp.Body.Body.Close()
		if err2 != nil {
			writeErrFn(w, http.StatusBadGateway, "Upstream retry failed after removing responses_tools: "+err2.Error())
			return nil, false
		}
		if resp2 == nil {
			writeErrFn(w, http.StatusBadGateway, "Upstream retry failed after removing responses_tools: empty response")
			return nil, false
		}
		errBody, _ := io.ReadAll(resp2.Body.Body)
		resp2.Body.Body.Close()
		writeErrFn(w, resp2.StatusCode, formatUpstreamError(resp2.StatusCode, errBody))
		return nil, false
	}
	errBody, _ := io.ReadAll(resp.Body.Body)
	resp.Body.Body.Close()
	writeErrFn(w, resp.StatusCode, formatUpstreamError(resp.StatusCode, errBody))
	return nil, false
}
