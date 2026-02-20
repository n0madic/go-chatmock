package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/n0madic/go-chatmock/internal/auth"
	"github.com/n0madic/go-chatmock/internal/config"
)

// newTestServer builds a Server without binding to port 1455, suitable for
// unit tests that exercise handler logic but do not need a real TCP listener.
func newTestServer() *Server {
	return &Server{
		ExitCode:    1,
		OAuthConfig: auth.NewOAuth2Config(config.ClientID(), config.OAuthIssuer()),
		Verifier:    oauth2.GenerateVerifier(),
		State:       "test-state-abc123",
		httpServer:  &http.Server{}, // minimal; supports Shutdown()
	}
}

// TestAuthURLContainsState verifies that the generated auth URL includes the state.
func TestAuthURLContainsState(t *testing.T) {
	s := newTestServer()
	authURL := s.AuthURL()
	if !strings.Contains(authURL, s.State) {
		t.Errorf("auth URL %q does not contain state %q", authURL, s.State)
	}
}

// TestAuthURLContainsChallengeMethod verifies PKCE S256 challenge is present.
func TestAuthURLContainsChallengeMethod(t *testing.T) {
	s := newTestServer()
	authURL := s.AuthURL()
	if !strings.Contains(authURL, "code_challenge") {
		t.Errorf("auth URL %q does not contain code_challenge", authURL)
	}
}

// TestHandleCallbackMissingCode checks that a callback request without a code
// returns 400 Bad Request.
func TestHandleCallbackMissingCode(t *testing.T) {
	s := newTestServer()
	// Provide a minimal httpServer so Shutdown() does not panic.
	s.httpServer = &http.Server{}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	rec := httptest.NewRecorder()

	s.handleCallback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

// TestExchangeCodeFailsWithBadCode verifies that ExchangeCode returns an error
// when the upstream token server rejects the code.
func TestExchangeCodeFailsWithBadCode(t *testing.T) {
	// Point OAuthConfig at a test server that always returns 400.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer ts.Close()

	s := newTestServer()
	s.OAuthConfig.Endpoint.TokenURL = ts.URL

	_, err := s.ExchangeCode(context.Background(), "bad_code")
	if err == nil {
		t.Fatal("expected error for bad code, got nil")
	}
}

// TestShutdownIsIdempotent checks that calling Shutdown multiple times does not panic.
func TestShutdownIsIdempotent(t *testing.T) {
	s := newTestServer()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Shutdown panicked: %v", r)
		}
	}()

	s.Shutdown()
	s.Shutdown()
}

// TestNewServerBindsPort verifies that NewServer successfully binds to the
// required port. Skipped when the port is already in use.
func TestNewServerBindsPort(t *testing.T) {
	s, err := NewServer("127.0.0.1", false)
	if err != nil {
		if strings.Contains(err.Error(), "address already in use") ||
			strings.Contains(err.Error(), "bind: address already in use") {
			t.Skip("port 1455 already in use; skipping")
		}
		t.Fatalf("NewServer: %v", err)
	}
	defer s.Shutdown()

	if s.State == "" {
		t.Error("State must not be empty")
	}
	if s.Verifier == "" {
		t.Error("Verifier must not be empty")
	}
}

// TestHandleSuccessReturns200 verifies that /success returns 200 OK.
func TestHandleSuccessReturns200(t *testing.T) {
	s := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/success", nil)
	rec := httptest.NewRecorder()

	s.handleSuccess(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("expected text/html Content-Type, got %q", rec.Header().Get("Content-Type"))
	}
}
