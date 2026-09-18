// Package api is the api role: it serves the dashboard from stored results and enqueues
// work. It never calls an external service itself; internal/archtest enforces that it
// cannot even import one.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Server holds the api role's dependencies.
type Server struct {
	Pool *pgxpool.Pool
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status, body := http.StatusOK, map[string]any{"ok": true}
	if err := s.Pool.Ping(ctx); err != nil {
		status, body = http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "database unreachable"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
