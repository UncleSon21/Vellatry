package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// timelineEvent is one row of the event timeline.
type timelineEvent struct {
	ID         int64           `json:"id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Kind       string          `json:"kind"`
	SubjectID  *string         `json:"subject_id"`
	Actor      *string         `json:"actor"`
	Payload    json.RawMessage `json:"payload"`
}

// listEvents is the change timeline (newest first), and the catch-up read for a
// client that reconnects to the stream.
func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	var after int64
	if v := r.URL.Query().Get("after_id"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	}
	var out []timelineEvent
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, occurred_at, kind, subject_id, actor, payload FROM events
			WHERE id > $1 ORDER BY id DESC LIMIT $2`, after, limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (timelineEvent, error) {
			var e timelineEvent
			return e, r.Scan(&e.ID, &e.OccurredAt, &e.Kind, &e.SubjectID, &e.Actor, &e.Payload)
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

// streamEvents pushes the org's events over Server-Sent Events as they happen. Each
// message carries ids only; the client reads details through the API, which applies
// the same tenant scoping as every other read.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if sess.OrgID == "" {
		s.fail(w, r, forbidden("Finish setting up your organisation first."))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, r, fmt.Errorf("streaming unsupported"))
		return
	}
	// The stream outlives the server's write timeout by design.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	ch, unsubscribe := s.Hub.Subscribe(sess.OrgID)
	defer unsubscribe()
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case n, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", n.ID, n.Kind, n.raw)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// Notification is one event announced by Postgres.
type Notification struct {
	OrgID string `json:"org_id"`
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	raw   string
}

// Hub fans Postgres event notifications out to SSE subscribers, one LISTEN connection
// for the whole process, filtered by org so no tenant sees another's events.
type Hub struct {
	Pool   *pgxpool.Pool
	Logger *slog.Logger

	mu   sync.Mutex
	subs map[string]map[chan Notification]struct{}
}

// Subscribe returns a channel of org's notifications and a function to stop.
func (h *Hub) Subscribe(org string) (<-chan Notification, func()) {
	ch := make(chan Notification, 64)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[string]map[chan Notification]struct{}{}
	}
	if h.subs[org] == nil {
		h.subs[org] = map[chan Notification]struct{}{}
	}
	h.subs[org][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[org], ch)
		if len(h.subs[org]) == 0 {
			delete(h.subs, org)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) publish(n Notification) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[n.OrgID] {
		select {
		case ch <- n:
		default: // a slow client misses a nudge; it catches up from /v1/events
		}
	}
}

// Run listens until ctx ends, reconnecting with backoff.
func (h *Hub) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := h.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		h.Logger.WarnContext(ctx, "event listener stopped, reconnecting", "error", err, "in", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (h *Hub) listen(ctx context.Context) error {
	conn, err := h.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN vellatry_events"); err != nil {
		return err
	}
	for {
		pn, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var n Notification
		if err := json.Unmarshal([]byte(pn.Payload), &n); err != nil || n.OrgID == "" {
			continue
		}
		n.raw = pn.Payload
		h.publish(n)
	}
}
