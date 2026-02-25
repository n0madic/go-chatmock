package server

import (
	"net/http"

	"github.com/n0madic/go-chatmock/internal/codec"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	codec.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
