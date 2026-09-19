package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/searchread"
)

func (s *Server) searchOverview(w http.ResponseWriter, r *http.Request) {
	from, to, err := period(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var out searchread.Overview
	err = s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = searchread.LoadOverview(ctx, tx, from, to)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// searchTop serves /v1/search/queries and /v1/search/pages for ?month=YYYY-MM.
func (s *Server) searchTop(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		month := time.Now().UTC()
		month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
		if v := r.URL.Query().Get("month"); v != "" {
			m, err := time.Parse("2006-01", v)
			if err != nil {
				s.fail(w, r, badRequest("month must look like 2026-09."))
				return
			}
			month = m
		}
		limit := 100
		if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 1000 {
			limit = v
		}
		var out []searchread.TopRow
		err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			out, err = searchread.LoadTop(ctx, tx, kind, month, limit)
			return err
		})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"month": month.Format("2006-01"), "rows": nonNilT(out)})
	}
}

// channels serves /v1/analytics/channels and /v1/analytics/ai-referrals.
func (s *Server) channels(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		from, to, err := period(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		var totals []searchread.ChannelTotal
		var series []searchread.Point
		err = s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			totals, series, err = searchread.LoadChannels(ctx, tx, kind, from, to)
			return err
		})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly),
			"totals": nonNilT(totals), "series": nonNilT(series)})
	}
}
