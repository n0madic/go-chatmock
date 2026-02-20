package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/n0madic/go-chatmock/internal/types"
)

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
