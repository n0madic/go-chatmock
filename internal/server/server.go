package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n0madic/go-chatmock/internal/auth"
	"github.com/n0madic/go-chatmock/internal/codec"
	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/pipeline"
	"github.com/n0madic/go-chatmock/internal/state"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

// maxBodyBytes limits the size of incoming request bodies.
const maxBodyBytes = 10 * 1024 * 1024 // 10 MB

// Server is the main HTTP server.
type Server struct {
	Config     *config.ServerConfig
	httpServer *http.Server
	Pipeline   *pipeline.Pipeline
	Registry   *models.Registry
	Store      *state.Store
	cancelBg   context.CancelFunc

	chatEnc      codec.Encoder
	responsesEnc codec.Encoder
	textEnc      codec.Encoder
	anthropicEnc codec.Encoder
	ollamaEnc    codec.Encoder
}

// New creates a new server with all routes registered.
func New(cfg *config.ServerConfig) *Server {
	tm := auth.NewTokenManager(config.ClientID(), config.TokenURL())
	uc := upstream.NewClient(tm, cfg.Verbose, cfg.Debug)
	reg := models.NewRegistry(tm)
	store := state.NewStore(state.DefaultTTL, state.DefaultCapacity)

	s := &Server{
		Config:   cfg,
		Registry: reg,
		Store:    store,
		Pipeline: &pipeline.Pipeline{
			Config:   cfg,
			Store:    store,
			Upstream: uc,
			Registry: reg,
		},
		chatEnc:      &codec.ChatEncoder{},
		responsesEnc: &codec.ResponsesEncoder{},
		textEnc:      &codec.TextEncoder{},
		anthropicEnc: &codec.AnthropicEncoder{},
		ollamaEnc:    &codec.OllamaEncoder{},
	}

	// Pre-fetch available models in background
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

	// Anthropic-compatible routes
	mux.HandleFunc("POST /v1/messages", s.handleAnthropicMessages)
	mux.HandleFunc("POST /v1/messages/count_tokens", s.handleAnthropicCountTokens)

	// Ollama-compatible routes
	mux.HandleFunc("POST /api/chat", s.handleOllamaChat)
	mux.HandleFunc("GET /api/tags", s.handleOllamaTags)
	mux.HandleFunc("POST /api/show", s.handleOllamaShow)
	mux.HandleFunc("GET /api/version", s.handleOllamaVersion)

	// OPTIONS for CORS preflight
	mux.HandleFunc("OPTIONS /", s.handleOptions)

	handler := corsMiddleware(authMiddleware(cfg, verboseMiddleware(cfg, debugMiddleware(cfg, mux))))

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

// ListenAndServe starts the server.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.cancelBg != nil {
		s.cancelBg()
	}
	if s.Store != nil {
		s.Store.Close()
	}
	return s.httpServer.Shutdown(ctx)
}

// --- Route handlers ---

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, s.chatEnc)
	if !ok {
		return
	}

	// Passthrough: only on /v1/responses route, not chat
	ctx := &pipeline.RequestContext{
		Context:   r.Context(),
		SessionID: strings.TrimSpace(r.Header.Get("X-Session-Id")),
	}
	s.Pipeline.Execute(ctx, w, body, "chat", s.chatEnc, s.responsesEnc)
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, s.chatEnc)
	if !ok {
		return
	}

	ctx := &pipeline.RequestContext{
		Context:   r.Context(),
		SessionID: strings.TrimSpace(r.Header.Get("X-Session-Id")),
	}

	// Passthrough: when the body has a top-level `input` field
	if pipeline.BodyHasInputField(body) {
		s.Pipeline.ExecutePassthrough(ctx, w, body, s.responsesEnc)
		return
	}

	s.Pipeline.Execute(ctx, w, body, "responses", s.chatEnc, s.responsesEnc)
}

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	// Text completions uses its own handler path (not unified pipeline)
	// as it has simpler normalization.
	s.handleTextCompletions(w, r)
}

func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// --- Helpers ---

func readBody(w http.ResponseWriter, r *http.Request, enc codec.Encoder) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		enc.WriteError(w, http.StatusBadRequest, "Failed to read request body")
		return nil, false
	}
	return body, true
}
