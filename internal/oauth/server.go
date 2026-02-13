package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"github.com/n0madic/go-chatmock/internal/auth"
	"github.com/n0madic/go-chatmock/internal/config"
)

const (
	RequiredPort = 1455
	URLBase      = "http://localhost:1455"
)

// Server manages the OAuth callback HTTP server.
type Server struct {
	httpServer  *http.Server
	listener    net.Listener
	ExitCode    int
	OAuthConfig *oauth2.Config
	Verifier    string
	State       string
	Verbose     bool
}

// NewServer creates a new OAuth callback server.
func NewServer(bindHost string, verbose bool) (*Server, error) {
	clientID := config.ClientID()
	issuer := config.OAuthIssuer()

	stateBytes := make([]byte, 32)
	_, _ = rand.Read(stateBytes)

	s := &Server{
		ExitCode:    1,
		OAuthConfig: auth.NewOAuth2Config(clientID, issuer),
		Verifier:    oauth2.GenerateVerifier(),
		State:       hex.EncodeToString(stateBytes),
		Verbose:     verbose,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("GET /success", s.handleSuccess)

	addr := fmt.Sprintf("%s:%d", bindHost, RequiredPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.listener = ln

	s.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s, nil
}

// AuthURL returns the full authorization URL to open in the browser.
func (s *Server) AuthURL() string {
	return s.OAuthConfig.AuthCodeURL(s.State,
		oauth2.S256ChallengeOption(s.Verifier),
		oauth2.SetAuthURLParam("id_token_add_organizations", "true"),
		oauth2.SetAuthURLParam("codex_cli_simplified_flow", "true"),
	)
}

// ListenAndServe starts the OAuth callback server.
func (s *Server) ListenAndServe() error {
	slog.Info("starting local login server", "url", URLBase)
	return s.httpServer.Serve(s.listener)
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.httpServer.Shutdown(ctx); err != nil {
		slog.Error("server shutdown error", "error", err)
	}
}

// ExchangeCode exchanges an authorization code for tokens and persists them.
func (s *Server) ExchangeCode(ctx context.Context, code string) (*auth.AuthFile, error) {
	token, err := s.OAuthConfig.Exchange(ctx, code, oauth2.VerifierOption(s.Verifier))
	if err != nil {
		return nil, err
	}

	idToken, _ := token.Extra("id_token").(string)
	accessToken := token.AccessToken
	refreshToken := token.RefreshToken

	idClaims, _ := auth.ParseJWTClaims(idToken)
	accessClaims, _ := auth.ParseJWTClaims(accessToken)

	var accountID string
	if authObj, ok := idClaims["https://api.openai.com/auth"].(map[string]any); ok {
		accountID, _ = authObj["chatgpt_account_id"].(string)
	}

	apiKey, _ := s.maybeObtainAPIKey(idClaims, accessClaims, idToken)

	af := &auth.AuthFile{
		APIKey: apiKey,
		Tokens: auth.TokenData{
			IDToken:      idToken,
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			AccountID:    accountID,
		},
		LastRefresh: auth.NowISO8601(),
	}

	return af, nil
}

func (s *Server) maybeObtainAPIKey(idClaims, accessClaims map[string]any, idToken string) (string, error) {
	orgID, _ := idClaims["organization_id"].(string)
	projectID, _ := idClaims["project_id"].(string)

	if orgID == "" || projectID == "" {
		return "", nil
	}

	today := time.Now().UTC().Format("2006-01-02")
	name := fmt.Sprintf("ChatGPT Local [auto-generated] (%s)", today)

	tokenURL := s.OAuthConfig.Endpoint.TokenURL
	apiKey, err := auth.TokenExchange(tokenURL, s.OAuthConfig.ClientID, idToken, name)
	if err != nil {
		slog.Warn("API key exchange failed", "error", err)
		return "", err
	}
	return apiKey, nil
}
