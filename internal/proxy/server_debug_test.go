package proxy

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/n0madic/go-chatmock/internal/config"
)

func TestDebugMiddlewareDumpsRequestAndPreservesBody(t *testing.T) {
	originalLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	originalStderr := os.Stderr
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stderr = stderrW
	t.Cleanup(func() { os.Stderr = originalStderr })

	s := &Server{Config: &config.ServerConfig{Debug: true}}
	const payload = "debug-body"

	handler := s.debugMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != payload {
			t.Fatalf("body: got %q, want %q", string(body), payload)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?trace=1", strings.NewReader(payload))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusNoContent)
	}
	if err := stderrW.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	rawDump, err := io.ReadAll(stderrR)
	if err != nil {
		t.Fatalf("read stderr dump: %v", err)
	}
	if err := stderrR.Close(); err != nil {
		t.Fatalf("close stderr reader: %v", err)
	}

	logOutput := logs.String()
	if !strings.Contains(logOutput, "request.dump") {
		t.Fatalf("expected request.dump log entry, got %q", logOutput)
	}
	rawDumpStr := string(rawDump)
	if !strings.Contains(rawDumpStr, "POST /v1/chat/completions?trace=1 HTTP/1.1") {
		t.Fatalf("expected request line in raw dump, got %q", rawDumpStr)
	}
	if !strings.Contains(rawDumpStr, payload) {
		t.Fatalf("expected payload in raw dump, got %q", rawDumpStr)
	}
	if !strings.Contains(rawDumpStr, "===== INBOUND REQUEST BEGIN =====") {
		t.Fatalf("expected dump begin delimiter, got %q", rawDumpStr)
	}
	if !strings.Contains(rawDumpStr, "===== INBOUND REQUEST END =====") {
		t.Fatalf("expected dump end delimiter, got %q", rawDumpStr)
	}
}
