package server

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"sync"

	"github.com/n0madic/go-chatmock/internal/codec"
	"github.com/n0madic/go-chatmock/internal/config"
)

var debugDumpMu sync.Mutex

const serverAccessTokenError = "Invalid or missing server access token"

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqHeaders := r.Header.Get("Access-Control-Request-Headers")
		if reqHeaders == "" {
			reqHeaders = "Authorization, Content-Type, Accept"
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func authMiddleware(cfg *config.ServerConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedToken := ""
		if cfg != nil {
			expectedToken = strings.TrimSpace(cfg.AccessToken)
		}
		if expectedToken == "" || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		switch r.URL.Path {
		case "/", "/health":
			next.ServeHTTP(w, r)
			return
		}
		if !requiresAccessToken(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		header := strings.TrimSpace(r.Header.Get("Authorization"))
		token, ok := parseBearerAuthToken(header)
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
			writeAccessTokenAuthError(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func writeAccessTokenAuthError(w http.ResponseWriter, r *http.Request) {
	if isAnthropicRequest(r) {
		codec.WriteAnthropicError(w, http.StatusUnauthorized, "authentication_error", serverAccessTokenError)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		codec.WriteOllamaError(w, http.StatusUnauthorized, serverAccessTokenError)
		return
	}
	codec.WriteOpenAIError(w, http.StatusUnauthorized, serverAccessTokenError)
}

func isAnthropicRequest(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("anthropic-version")) != "" ||
		strings.TrimSpace(r.Header.Get("anthropic-beta")) != ""
}

func parseBearerAuthToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || parts[0] != "Bearer" || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return parts[1], true
}

func requiresAccessToken(path string) bool {
	return strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/api/")
}

func verboseMiddleware(cfg *config.ServerConfig, next http.Handler) http.Handler {
	if cfg == nil || !cfg.Verbose {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func debugMiddleware(cfg *config.ServerConfig, next http.Handler) http.Handler {
	if cfg == nil || !cfg.Debug {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dump, err := httputil.DumpRequest(r, true)
		if err != nil {
			slog.Error("request.dump.failed", "method", r.Method, "path", r.URL.Path, "error", err)
		} else {
			slog.Info("request.dump", "method", r.Method, "path", r.URL.Path)
			writeDebugDumpBlock("INBOUND REQUEST", dump)
		}
		next.ServeHTTP(w, r)
	})
}

func writeDebugDumpBlock(title string, data []byte) {
	debugDumpMu.Lock()
	defer debugDumpMu.Unlock()

	header := "===== " + strings.TrimSpace(title) + " BEGIN =====\n"
	footer := "===== " + strings.TrimSpace(title) + " END =====\n"

	os.Stderr.WriteString(header)
	if len(data) > 0 {
		os.Stderr.Write(data)
		if data[len(data)-1] != '\n' {
			os.Stderr.WriteString("\n")
		}
	}
	os.Stderr.WriteString(footer)
}

func hasAnthropicAuthHeader(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("x-api-key")) != "" {
		return true
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return true
	}
	if strings.TrimSpace(r.Header.Get("Proxy-Authorization")) != "" {
		return true
	}
	return false
}
