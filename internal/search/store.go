// Package search keeps the small Search Console and GA4 rollups the dashboard reads.
// Raw facts live in internal/warehouse; only totals and bounded top-N lists land here.
// Writes run inside a transaction scoped to one org and replace whole days or months,
// so re-running a sync never double-counts.
package search

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/google"
	"github.com/UncleSon21/vellatry/internal/warehouse"
)

// Top-N kept per month in Postgres; the long tail is queried from the warehouse.
const (
	TopQueries = 5000
	TopPages   = 2000
	TopLanding = 1000
)

// UpsertDayTotals stores Search Console's exact daily totals.
func UpsertDayTotals(ctx context.Context, tx pgx.Tx, org string, totals []google.DayTotal) error {
	for _, d := range totals {
		if _, err := tx.Exec(ctx, `
			INSERT INTO search_daily (org_id, day, clicks, impressions, position_sum)
			VALUES ($1, $2::date, $3, $4, $5)
			ON CONFLICT (org_id, day) DO UPDATE SET clicks = EXCLUDED.clicks, impressions = EXCLUDED.impressions,
			    position_sum = EXCLUDED.position_sum, synced_at = now()`,
			org, d.Date, d.Clicks, d.Impressions, d.Position*float64(d.Impressions)); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceSearchMonth replaces a month's top queries and pages.
func ReplaceSearchMonth(ctx context.Context, tx pgx.Tx, org string, month time.Time, queries, pages []warehouse.Rollup) error {
	m := month.Format(time.DateOnly)
	for _, t := range []struct {
		table, col string
		rows       []warehouse.Rollup
	}{{"search_query_monthly", "query", queries}, {"search_page_monthly", "page", pages}} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+t.table+` WHERE org_id = $1 AND month = $2::date`, org, m); err != nil {
			return err
		}
		if len(t.rows) == 0 {
			continue
		}
		keys, clicks, imps, pos := columns(t.rows)
		if _, err := tx.Exec(ctx, `
			INSERT INTO `+t.table+` (org_id, month, `+t.col+`, clicks, impressions, position_sum)
			SELECT $1, $2::date, k, c, i, p FROM unnest($3::text[], $4::bigint[], $5::bigint[], $6::float8[]) AS u(k, c, i, p)`,
			org, m, keys, clicks, imps, pos); err != nil {
			return err
		}
	}
	return nil
}

func columns(rows []warehouse.Rollup) ([]string, []int64, []int64, []float64) {
	keys := make([]string, len(rows))
	clicks := make([]int64, len(rows))
	imps := make([]int64, len(rows))
	pos := make([]float64, len(rows))
	for i, r := range rows {
		keys[i], clicks[i], imps[i], pos[i] = r.Key, r.Clicks, r.Impressions, r.PositionSum
	}
	return keys, clicks, imps, pos
}

// ChannelDay and AssistantDay are GA4 rows aggregated for Postgres.
type ChannelDay struct {
	Channel   string
	Sessions  int64
	Users     int64
	KeyEvents float64
}

// AggregateAnalytics folds one day of GA4 rows into totals per channel and per AI
// assistant (sessions whose source is an AI assistant).
func AggregateAnalytics(rows []google.AnalyticsRow) (channels, assistants []ChannelDay) {
	byChannel, byAssistant := map[string]*ChannelDay{}, map[string]*ChannelDay{}
	add := func(m map[string]*ChannelDay, key string, r google.AnalyticsRow) {
		c := m[key]
		if c == nil {
			c = &ChannelDay{Channel: key}
			m[key] = c
		}
		c.Sessions += r.Sessions
		c.Users += r.Users
		c.KeyEvents += r.KeyEvents
	}
	for _, r := range rows {
		ch := r.Channel
		if ch == "" {
			ch = "(not set)"
		}
		add(byChannel, ch, r)
		if a := google.Assistant(r.Source); a != "" {
			add(byAssistant, a, r)
		}
	}
	for _, c := range byChannel {
		channels = append(channels, *c)
	}
	for _, c := range byAssistant {
		assistants = append(assistants, *c)
	}
	return channels, assistants
}

// ReplaceAnalyticsDay replaces one day of channel and AI-assistant totals.
func ReplaceAnalyticsDay(ctx context.Context, tx pgx.Tx, org string, day time.Time, channels, assistants []ChannelDay) error {
	d := day.Format(time.DateOnly)
	for _, t := range []struct {
		table, col string
		rows       []ChannelDay
	}{{"analytics_daily", "channel", channels}, {"ai_referral_daily", "assistant", assistants}} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+t.table+` WHERE org_id = $1 AND day = $2::date`, org, d); err != nil {
			return err
		}
		for _, c := range t.rows {
			if _, err := tx.Exec(ctx, `INSERT INTO `+t.table+` (org_id, day, `+t.col+`, sessions, users, key_events) VALUES ($1, $2::date, $3, $4, $5, $6)`,
				org, d, c.Channel, c.Sessions, c.Users, c.KeyEvents); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReplaceLandingMonth replaces a month's top landing pages.
func ReplaceLandingMonth(ctx context.Context, tx pgx.Tx, org string, month time.Time, rows []warehouse.Rollup) error {
	m := month.Format(time.DateOnly)
	if _, err := tx.Exec(ctx, `DELETE FROM analytics_landing_monthly WHERE org_id = $1 AND month = $2::date`, org, m); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	pages := make([]string, len(rows))
	sessions := make([]int64, len(rows))
	events := make([]float64, len(rows))
	for i, r := range rows {
		pages[i], sessions[i], events[i] = r.Key, r.Sessions, r.KeyEvents
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO analytics_landing_monthly (org_id, month, page, sessions, key_events)
		SELECT $1, $2::date, p, s, e FROM unnest($3::text[], $4::bigint[], $5::float8[]) AS u(p, s, e)`,
		org, m, pages, sessions, events)
	return err
}

// MarkSynced records a successful sync, or the error that stopped it.
func MarkSynced(ctx context.Context, tx pgx.Tx, org, kind string, lastDay time.Time, syncErr error) error {
	var errText *string
	if syncErr != nil {
		s := syncErr.Error()
		errText = &s
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO sync_state (org_id, kind, last_synced_at, last_day, last_error) VALUES ($1, $2, now(), $3::date, $4)
		ON CONFLICT (org_id, kind) DO UPDATE SET last_synced_at = now(),
		    last_day = GREATEST(sync_state.last_day, EXCLUDED.last_day), last_error = EXCLUDED.last_error`,
		org, kind, lastDay.Format(time.DateOnly), errText)
	return err
}

// MarkSyncError records why a sync stopped without advancing what was synced.
func MarkSyncError(ctx context.Context, tx pgx.Tx, org, kind string, syncErr error) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO sync_state (org_id, kind, last_error) VALUES ($1, $2, $3)
		ON CONFLICT (org_id, kind) DO UPDATE SET last_error = EXCLUDED.last_error`,
		org, kind, syncErr.Error())
	return err
}

// Months returns the first day of every month touched by [from, to].
func Months(from, to time.Time) []time.Time {
	var out []time.Time
	for m, _ := warehouse.MonthBounds(from); !m.After(to); m = m.AddDate(0, 1, 0) {
		out = append(out, m)
	}
	return out
}

// Days returns every day in [from, to].
func Days(from, to time.Time) []time.Time {
	var out []time.Time
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return out
}

// Chunks splits [from, to] into ranges of at most size days, oldest first.
func Chunks(from, to time.Time, size int) [][2]time.Time {
	var out [][2]time.Time
	for start := from; !start.After(to); start = start.AddDate(0, 0, size) {
		end := start.AddDate(0, 0, size-1)
		if end.After(to) {
			end = to
		}
		out = append(out, [2]time.Time{start, end})
	}
	return out
}
