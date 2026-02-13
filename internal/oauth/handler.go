package oauth

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/n0madic/go-chatmock/internal/auth"
)

const loginSuccessHTML = `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>Login successful</title>
  </head>
  <body>
    <div style="max-width: 640px; margin: 80px auto; font-family: system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial, sans-serif;">
      <h1>Login successful</h1>
      <p>You can now close this window and return to the terminal and run <code>go-chatmock serve</code> to start the server.</p>
    </div>
  </body>
</html>`

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Missing auth code", http.StatusBadRequest)
		go s.Shutdown()
		return
	}

	af, err := s.ExchangeCode(r.Context(), code)
	if err != nil {
		http.Error(w, "Token exchange failed: "+err.Error(), http.StatusInternalServerError)
		go s.Shutdown()
		return
	}

	if err := auth.WriteAuthFile(af); err != nil {
		http.Error(w, "Unable to persist auth file", http.StatusInternalServerError)
		go s.Shutdown()
		return
	}

	s.ExitCode = 0
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(loginSuccessHTML))

	go func() {
		time.Sleep(2 * time.Second)
		s.Shutdown()
	}()
}

func (s *Server) handleSuccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(loginSuccessHTML))

	go func() {
		time.Sleep(2 * time.Second)
		s.Shutdown()
	}()
	_ = slog.Default()
}
