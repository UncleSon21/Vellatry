package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/siteread"
)

func (s *Server) requestCrawl(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var running bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crawls WHERE status = 'running' AND started_at > now() - interval '1 hour')`).Scan(&running); err != nil {
			return err
		}
		if running {
			return conflict("A crawl is already running.")
		}
		_, err := s.Bus.Emit(ctx, tx, sessionFrom(ctx).OrgID, events.Event{Kind: domainevents.SiteCrawlRequested, Actor: sessionFrom(ctx).UserID})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

func (s *Server) siteSummary(w http.ResponseWriter, r *http.Request) {
	var out siteread.Summary
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = siteread.LoadSummary(ctx, tx)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func limitParam(r *http.Request, def, max int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= max {
		return v
	}
	return def
}

func (s *Server) siteFindings(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "open"
	}
	if status != "open" && status != "resolved" && status != "dismissed" {
		s.fail(w, r, badRequest("Status must be open, resolved or dismissed."))
		return
	}
	var out []siteread.Finding
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = siteread.LoadFindings(ctx, tx, status, limitParam(r, 500, 2000))
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

func (s *Server) patchFinding(w http.ResponseWriter, r *http.Request) {
	s.patchStatus(w, r, "audit_findings", []string{"open", "dismissed"}, domainevents.FindingStatusChanged)
}

func (s *Server) sitePages(w http.ResponseWriter, r *http.Request) {
	var out []siteread.PageRow
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = siteread.LoadPages(ctx, tx, limitParam(r, 300, 5000))
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

func (s *Server) listFixes(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	switch status {
	case "", "proposed", "sent", "live", "measured", "dismissed":
	default:
		s.fail(w, r, badRequest("Unknown status."))
		return
	}
	var out []siteread.Fix
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = siteread.LoadFixes(ctx, tx, status, limitParam(r, 300, 2000))
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

// patchFix lets the team mark a fix as sent (handed to whoever makes the change),
// dismissed, or proposed again. "live" and "measured" are set by the system only.
func (s *Server) patchFix(w http.ResponseWriter, r *http.Request) {
	s.patchStatus(w, r, "fixes", []string{"proposed", "sent", "dismissed"}, domainevents.FixStatusChanged)
}

func (s *Server) patchStatus(w http.ResponseWriter, r *http.Request, table string, allowed []string, eventKind string) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, notFound("Not found."))
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	ok := false
	for _, a := range allowed {
		ok = ok || a == in.Status
	}
	if !ok {
		s.fail(w, r, badRequest("That status can't be set here."))
		return
	}
	err = s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var from string
		err := tx.QueryRow(ctx, `SELECT status FROM `+table+` WHERE id = $1 FOR UPDATE`, id).Scan(&from)
		if isNoRows(err) {
			return notFound("Not found.")
		}
		if err != nil || from == in.Status {
			return err
		}
		set := `status = $2`
		if table == "fixes" && in.Status == "sent" {
			set += `, sent_at = now()`
		}
		if _, err := tx.Exec(ctx, `UPDATE `+table+` SET `+set+` WHERE id = $1`, id, in.Status); err != nil {
			return err
		}
		_, err = s.Bus.Emit(ctx, tx, sessionFrom(ctx).OrgID, events.Event{Kind: eventKind, SubjectID: strconv.FormatInt(id, 10),
			Actor: sessionFrom(ctx).UserID, Payload: map[string]string{"from": from, "to": in.Status}})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
