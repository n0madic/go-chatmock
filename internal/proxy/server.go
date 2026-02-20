package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/n0madic/go-chatmock/internal/auth"
	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/responses-state"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

// Server is the main proxy HTTP server.
type Server struct {
	Config         *config.ServerConfig
	httpServer     *http.Server
	upstreamClient *upstream.Client
	Registry       *models.Registry
	responsesState *responsesstate.Store
}

// New creates a new proxy server with all routes registered.
func New(cfg *config.ServerConfig) *Server {
	tm := auth.NewTokenManager(config.ClientID(), config.TokenURL())
	uc := upstream.NewClient(tm, cfg.Verbose)
	reg := models.NewRegistry(tm)

	s := &Server{
		Config:         cfg,
		upstreamClient: uc,
		Registry:       reg,
		responsesState: responsesstate.NewStore(responsesstate.DefaultTTL, responsesstate.DefaultCapacity),
	}

	// Pre-fetch available models in background so the registry is ready for
	// the first incoming request.
	go func() { reg.GetModels() }()

	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("GET /", s.handleHealth)
	mux.HandleFunc("GET /health", s.handleHealth)

	// OpenAI-compatible routes
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/completions", s.handleCompletions)
	mux.HandleFunc("GET /v1/models", s.handleListModels)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)

	// Ollama-compatible routes
	mux.HandleFunc("POST /api/chat", s.handleOllamaChat)
	mux.HandleFunc("GET /api/tags", s.handleOllamaTags)
	mux.HandleFunc("POST /api/show", s.handleOllamaShow)
	mux.HandleFunc("GET /api/version", s.handleOllamaVersion)

	// OPTIONS for CORS preflight
	mux.HandleFunc("OPTIONS /", s.handleOptions)

	handler := s.corsMiddleware(s.verboseMiddleware(mux))

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
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
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		reqHeaders := r.Header.Get("Access-Control-Request-Headers")
		if reqHeaders == "" {
			reqHeaders = "Authorization, Content-Type, Accept"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
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

func (s *Server) verboseMiddleware(next http.Handler) http.Handler {
	if !s.Config.Verbose {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
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
