package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/visibility/read"
)

// period reads ?from=&to= (UTC dates, inclusive), defaulting to the last 30 days.
func period(r *http.Request) (time.Time, time.Time, error) {
	to := time.Now().UTC().Truncate(24 * time.Hour)
	from := to.AddDate(0, 0, -29)
	var err error
	if v := r.URL.Query().Get("to"); v != "" {
		if to, err = time.Parse(time.DateOnly, v); err != nil {
			return from, to, badRequest("to must be a date like 2026-09-19.")
		}
	}
	if v := r.URL.Query().Get("from"); v != "" {
		if from, err = time.Parse(time.DateOnly, v); err != nil {
			return from, to, badRequest("from must be a date like 2026-09-01.")
		}
	}
	if from.After(to) || to.Sub(from) > 400*24*time.Hour {
		return from, to, badRequest("Choose a period of up to 400 days.")
	}
	return from, to, nil
}

func (s *Server) performance(w http.ResponseWriter, r *http.Request) {
	from, to, err := period(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var out read.Performance
	err = s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = read.LoadPerformance(ctx, tx, from, to)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) grid(w http.ResponseWriter, r *http.Request) {
	var out []read.GridRow
	var coverage struct {
		Topics   int `json:"topics"`
		Explored int `json:"explored"`
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var engines []string
		if err := tx.QueryRow(ctx, `SELECT engines FROM org_settings LIMIT 1`).Scan(&engines); err != nil {
			return err
		}
		var err error
		out, err = read.LoadGrid(ctx, tx, engines)
		for _, row := range out {
			coverage.Topics++
			for _, c := range row.Cells {
				if c.State != "unexplored" {
					coverage.Explored++
					break
				}
			}
		}
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": nonNilT(out), "coverage": coverage})
}

func (s *Server) blindspots(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "open"
	}
	if status != "open" && status != "resolved" && status != "dismissed" {
		s.fail(w, r, badRequest("Status must be open, resolved or dismissed."))
		return
	}
	var out []read.Blindspot
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = read.LoadBlindspots(ctx, tx, status)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

// patchBlindspot lets a user dismiss a blindspot (it stays dismissed through later
// answers: the customer's decision wins) or reopen one they dismissed.
func (s *Server) patchBlindspot(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, notFound("Blindspot not found."))
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Status != "dismissed" && in.Status != "open" {
		s.fail(w, r, badRequest("Status must be dismissed or open."))
		return
	}
	err = s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var from, promptID string
		err := tx.QueryRow(ctx, `SELECT status, prompt_id::text FROM blindspots WHERE id = $1 FOR UPDATE`, id).Scan(&from, &promptID)
		if isNoRows(err) {
			return notFound("Blindspot not found.")
		}
		if err != nil || from == in.Status {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE blindspots SET status = $2 WHERE id = $1`, id, in.Status); err != nil {
			return err
		}
		_, err = s.Bus.Emit(ctx, tx, sessionFrom(ctx).OrgID, events.Event{Kind: domainevents.BlindspotStatusChanged, SubjectID: promptID,
			Actor: sessionFrom(ctx).UserID, Payload: map[string]any{"blindspot_id": id, "from": from, "to": in.Status}})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) promptAnswers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, r, notFound("Prompt not found."))
		return
	}
	limit := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	var out []read.Answer
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM prompts WHERE id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return notFound("Prompt not found.")
		}
		var err error
		out, err = read.LoadAnswers(ctx, tx, id, limit)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

func (s *Server) sources(w http.ResponseWriter, r *http.Request) {
	from, to, err := period(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var out []read.SourceRow
	err = s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		b, comps, err := brand.Load(ctx, tx)
		if err != nil {
			return err
		}
		var compDomains []string
		for _, c := range comps {
			compDomains = append(compDomains, c.Domains...)
		}
		out, err = read.LoadSources(ctx, tx, from, to, b.Domain, compDomains, 200)
		return err
	})
	if err != nil {
		s.fail(w, r, mapBrandErr(err))
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}
