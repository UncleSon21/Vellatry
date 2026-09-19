// Package searchread answers the dashboard's Search and Analytics questions from the
// Postgres rollups only. It is safe for the api role: no external clients.
package searchread

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// Totals are Search Console numbers for a period.
type Totals struct {
	Clicks      int64    `json:"clicks"`
	Impressions int64    `json:"impressions"`
	CTR         *float64 `json:"ctr"`      // %
	Position    *float64 `json:"position"` // impression-weighted
	Days        int      `json:"days"`     // days with data
}

// DayPoint is one day of Search Console totals.
type DayPoint struct {
	Day         string   `json:"day"`
	Clicks      int64    `json:"clicks"`
	Impressions int64    `json:"impressions"`
	Position    *float64 `json:"position"`
}

// Overview is the Search page header: the period, the previous period of equal length,
// and a daily series.
type Overview struct {
	From     string     `json:"from"`
	To       string     `json:"to"`
	Current  Totals     `json:"current"`
	Previous Totals     `json:"previous"`
	Series   []DayPoint `json:"series"`
}

// LoadOverview reads search_daily for [from, to] and the equal-length period before it.
func LoadOverview(ctx context.Context, tx pgx.Tx, from, to time.Time) (Overview, error) {
	o := Overview{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Series: []DayPoint{}}
	span := int(to.Sub(from).Hours()/24) + 1
	prevFrom, prevTo := from.AddDate(0, 0, -span), from.AddDate(0, 0, -1)
	var err error
	if o.Current, err = totals(ctx, tx, from, to); err != nil {
		return o, err
	}
	if o.Previous, err = totals(ctx, tx, prevFrom, prevTo); err != nil {
		return o, err
	}
	rows, err := tx.Query(ctx, `
		SELECT day::text, clicks, impressions, position_sum FROM search_daily
		WHERE day BETWEEN $1::date AND $2::date ORDER BY day`, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return o, err
	}
	o.Series, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (DayPoint, error) {
		var p DayPoint
		var posSum float64
		err := r.Scan(&p.Day, &p.Clicks, &p.Impressions, &posSum)
		p.Position = weighted(posSum, p.Impressions)
		return p, err
	})
	return o, err
}

func totals(ctx context.Context, tx pgx.Tx, from, to time.Time) (Totals, error) {
	var t Totals
	var posSum float64
	err := tx.QueryRow(ctx, `
		SELECT coalesce(sum(clicks), 0)::bigint, coalesce(sum(impressions), 0)::bigint, coalesce(sum(position_sum), 0), count(*)::int
		FROM search_daily WHERE day BETWEEN $1::date AND $2::date`, from.Format(time.DateOnly), to.Format(time.DateOnly)).
		Scan(&t.Clicks, &t.Impressions, &posSum, &t.Days)
	if t.Impressions > 0 {
		ctr := math.Round(10000*float64(t.Clicks)/float64(t.Impressions)) / 100
		t.CTR = &ctr
	}
	t.Position = weighted(posSum, t.Impressions)
	return t, err
}

func weighted(posSum float64, impressions int64) *float64 {
	if impressions == 0 {
		return nil
	}
	v := math.Round(100*posSum/float64(impressions)) / 100
	return &v
}

// TopRow is one query or page in a month.
type TopRow struct {
	Key         string   `json:"key"`
	Clicks      int64    `json:"clicks"`
	Impressions int64    `json:"impressions"`
	CTR         *float64 `json:"ctr"`
	Position    *float64 `json:"position"`
	// Change against the previous month, when the key was in that month's top list.
	ClicksPrev *int64   `json:"clicks_prev"`
	PosPrev    *float64 `json:"position_prev"`
}

// LoadTop returns a month's top queries ("query") or pages ("page") with the previous
// month alongside.
func LoadTop(ctx context.Context, tx pgx.Tx, kind string, month time.Time, limit int) ([]TopRow, error) {
	table, col := "search_query_monthly", "query"
	if kind == "page" {
		table, col = "search_page_monthly", "page"
	}
	m := month.Format(time.DateOnly)
	prev := month.AddDate(0, -1, 0).Format(time.DateOnly)
	rows, err := tx.Query(ctx, `
		SELECT c.`+col+`, c.clicks, c.impressions, c.position_sum, p.clicks, p.impressions, p.position_sum
		FROM `+table+` c
		LEFT JOIN `+table+` p ON p.org_id = c.org_id AND p.month = $2::date AND p.`+col+` = c.`+col+`
		WHERE c.month = $1::date ORDER BY c.clicks DESC, c.impressions DESC LIMIT $3`, m, prev, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TopRow, error) {
		var t TopRow
		var posSum float64
		var pClicks, pImps *int64
		var pPos *float64
		if err := r.Scan(&t.Key, &t.Clicks, &t.Impressions, &posSum, &pClicks, &pImps, &pPos); err != nil {
			return t, err
		}
		if t.Impressions > 0 {
			ctr := math.Round(10000*float64(t.Clicks)/float64(t.Impressions)) / 100
			t.CTR = &ctr
		}
		t.Position = weighted(posSum, t.Impressions)
		t.ClicksPrev = pClicks
		if pImps != nil && pPos != nil {
			t.PosPrev = weighted(*pPos, *pImps)
		}
		return t, nil
	})
}

// ChannelTotal is sessions for one channel or AI assistant over a period.
type ChannelTotal struct {
	Name      string  `json:"name"`
	Sessions  int64   `json:"sessions"`
	Users     int64   `json:"users"`
	KeyEvents float64 `json:"key_events"`
}

// Point is one day of one channel or assistant.
type Point struct {
	Day      string `json:"day"`
	Name     string `json:"name"`
	Sessions int64  `json:"sessions"`
}

// LoadChannels returns per-channel ("channel") or per-assistant ("assistant") totals and
// the daily series for [from, to].
func LoadChannels(ctx context.Context, tx pgx.Tx, kind string, from, to time.Time) ([]ChannelTotal, []Point, error) {
	table, col := "analytics_daily", "channel"
	if kind == "assistant" {
		table, col = "ai_referral_daily", "assistant"
	}
	f, t := from.Format(time.DateOnly), to.Format(time.DateOnly)
	rows, err := tx.Query(ctx, `
		SELECT `+col+`, sum(sessions)::bigint, sum(users)::bigint, sum(key_events)::float8 FROM `+table+`
		WHERE day BETWEEN $1::date AND $2::date GROUP BY `+col+` ORDER BY sum(sessions) DESC`, f, t)
	if err != nil {
		return nil, nil, err
	}
	totals, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ChannelTotal, error) {
		var c ChannelTotal
		return c, r.Scan(&c.Name, &c.Sessions, &c.Users, &c.KeyEvents)
	})
	if err != nil {
		return nil, nil, err
	}
	rows, err = tx.Query(ctx, `SELECT day::text, `+col+`, sessions FROM `+table+` WHERE day BETWEEN $1::date AND $2::date ORDER BY day`, f, t)
	if err != nil {
		return nil, nil, err
	}
	series, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Point, error) {
		var p Point
		return p, r.Scan(&p.Day, &p.Name, &p.Sessions)
	})
	return totals, series, err
}
