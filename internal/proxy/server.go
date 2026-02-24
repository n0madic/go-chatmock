package proxy

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/n0madic/go-chatmock/internal/auth"
	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/models"
	responsesstate "github.com/n0madic/go-chatmock/internal/responses-state"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

// upstreamDoer abstracts the ChatGPT upstream client so the proxy handlers can
// be tested with a mock without a real network connection.
type upstreamDoer interface {
	Do(context.Context, *upstream.Request) (*upstream.Response, error)
}

// Server is the main proxy HTTP server.
type Server struct {
	Config         *config.ServerConfig
	httpServer     *http.Server
	upstreamClient upstreamDoer
	Registry       *models.Registry
	responsesState *responsesstate.Store
	debugDumpMu    sync.Mutex
	cancelBg       context.CancelFunc
}

const serverAccessTokenError = "Invalid or missing server access token"

// New creates a new proxy server with all routes registered.
func New(cfg *config.ServerConfig) *Server {
	tm := auth.NewTokenManager(config.ClientID(), config.TokenURL())
	uc := upstream.NewClient(tm, cfg.Verbose, cfg.Debug)
	reg := models.NewRegistry(tm)

	s := &Server{
		Config:         cfg,
		upstreamClient: uc,
		Registry:       reg,
		responsesState: responsesstate.NewStore(responsesstate.DefaultTTL, responsesstate.DefaultCapacity),
	}

	// Pre-fetch available models in background so the registry is ready for
	// the first incoming request.
	bgCtx, cancel := context.WithCancel(context.Background())
	s.cancelBg = cancel
	go func() {
		done := make(chan struct{})
		go func() {
			reg.GetModels()
			close(done)
		}()
		select {
		case <-done:
		case <-bgCtx.Done():
			slog.Debug("background model prefetch cancelled")
		}
	}()

	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("GET /", s.handleHealth)
	mux.HandleFunc("GET /health", s.handleHealth)

	// OpenAI-compatible routes
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/completions", s.handleCompletions)
	mux.HandleFunc("GET /v1/models", s.handleListModels)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)

	// Anthropic-compatible routes (Claude Code gateway)
	mux.HandleFunc("POST /v1/messages", s.handleAnthropicMessages)
	mux.HandleFunc("POST /v1/messages/count_tokens", s.handleAnthropicCountTokens)

	// Ollama-compatible routes
	mux.HandleFunc("POST /api/chat", s.handleOllamaChat)
	mux.HandleFunc("GET /api/tags", s.handleOllamaTags)
	mux.HandleFunc("POST /api/show", s.handleOllamaShow)
	mux.HandleFunc("GET /api/version", s.handleOllamaVersion)

	// OPTIONS for CORS preflight
	mux.HandleFunc("OPTIONS /", s.handleOptions)

	handler := s.corsMiddleware(s.authMiddleware(s.verboseMiddleware(s.debugMiddleware(mux))))

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: handler,
		// ReadTimeout covers only reading the request body; 30s is plenty for any JSON payload.
		ReadTimeout: 30 * time.Second,
		// WriteTimeout must be longer than the upstream SSE timeout (5 min) plus
		// translation overhead. 600s gives a comfortable margin for long-running
		// reasoning streams without hard-cutting clients mid-response.
		WriteTimeout: 600 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return s
}

// ListenAndServe starts the proxy server.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.cancelBg != nil {
		s.cancelBg()
	}
	if s.responsesState != nil {
		s.responsesState.Close()
	}
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// corsMiddleware allows requests from any origin. This proxy is designed for
// local use only; wildcard CORS is intentional so browser-based IDE extensions
// (Cursor, VS Code web, etc.) can reach it without a per-origin allowlist.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
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

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedToken := ""
		if s.Config != nil {
			expectedToken = strings.TrimSpace(s.Config.AccessToken)
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
		// ConstantTimeCompare prevents timing attacks that could leak the expected
		// token length or prefix through response latency differences.
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
			s.writeAccessTokenAuthError(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) writeAccessTokenAuthError(w http.ResponseWriter, r *http.Request) {
	if isAnthropicRequest(r) {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", serverAccessTokenError)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeOllamaError(w, http.StatusUnauthorized, serverAccessTokenError)
		return
	}
	writeError(w, http.StatusUnauthorized, serverAccessTokenError)
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

func (s *Server) verboseMiddleware(next http.Handler) http.Handler {
	if !s.Config.Verbose {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) debugMiddleware(next http.Handler) http.Handler {
	if s.Config == nil || !s.Config.Debug {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dump, err := httputil.DumpRequest(r, true)
		if err != nil {
			slog.Error("request.dump.failed", "method", r.Method, "path", r.URL.Path, "error", err)
		} else {
			slog.Info("request.dump", "method", r.Method, "path", r.URL.Path)
			s.writeDebugDumpBlock("INBOUND REQUEST", dump)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) writeDebugDumpBlock(title string, data []byte) {
	if s == nil {
		return
	}
	s.debugDumpMu.Lock()
	defer s.debugDumpMu.Unlock()

	header := "===== " + strings.TrimSpace(title) + " BEGIN =====\n"
	footer := "===== " + strings.TrimSpace(title) + " END =====\n"

	if _, err := os.Stderr.WriteString(header); err != nil {
		slog.Error("debug.dump.write.failed", "title", title, "error", err)
		return
	}
	if len(data) > 0 {
		if _, err := os.Stderr.Write(data); err != nil {
			slog.Error("debug.dump.write.failed", "title", title, "error", err)
			return
		}
		if data[len(data)-1] != '\n' {
			if _, err := os.Stderr.WriteString("\n"); err != nil {
				slog.Error("debug.dump.write.failed", "title", title, "error", err)
				return
			}
		}
	}
	if _, err := os.Stderr.WriteString(footer); err != nil {
		slog.Error("debug.dump.write.failed", "title", title, "error", err)
	}
}

// validateModel checks whether model is in the registry, writing a 400 error and
// returning false if it is not. Skips validation when --debug-model is active.
func (s *Server) validateModel(w http.ResponseWriter, model string) bool {
	if s.Config.DebugModel != "" {
		return true
	}
	ok, hint := s.Registry.IsKnownModel(model)
	if ok {
		return true
	}
	msg := fmt.Sprintf("model %q is not available via this endpoint", model)
	if hint != "" {
		msg += "; available models: " + hint
	}
	writeError(w, http.StatusBadRequest, msg)
	return false
}
