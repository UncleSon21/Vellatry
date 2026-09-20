package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/searchread"
)

// trafficChange answers "why did organic traffic change?": the totals first, then the
// split between people searching for the brand and everyone else, then the queries
// that moved most.
func trafficChange(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error) {
	p := in.Period()
	o, err := searchread.LoadOverview(ctx, tx, p.From, p.To)
	if err != nil {
		return Bundle{}, err
	}
	var b Bundle
	if o.Current.Days == 0 {
		b.Empty = "Search Console holds no data for " + p.Label() + "."
		return b, nil
	}
	b.Add(Fact{ID: "clicks", Label: "Clicks", Value: Num(o.Current.Clicks), Raw: float64(o.Current.Clicks),
		Note: "from Google Search, " + p.Label(), Trend: Trend(float64(o.Current.Clicks - o.Previous.Clicks))})
	b.Add(Fact{ID: "impressions", Label: "Impressions", Value: Num(o.Current.Impressions), Raw: float64(o.Current.Impressions)})
	if o.Previous.Days > 0 {
		b.Add(Fact{ID: "clicks_before", Label: "Clicks before", Value: Num(o.Previous.Clicks), Raw: float64(o.Previous.Clicks),
			Note: "the " + fmt.Sprint(p.Days()) + " days before"})
		b.Add(Fact{ID: "clicks_change", Label: "Change in clicks", Value: Change(o.Current.Clicks, o.Previous.Clicks),
			Raw: float64(o.Current.Clicks - o.Previous.Clicks), Trend: Trend(float64(o.Current.Clicks - o.Previous.Clicks))})
	}
	if o.Current.CTR != nil {
		b.Add(Fact{ID: "ctr", Label: "Click-through rate", Value: Pct(*o.Current.CTR), Raw: *o.Current.CTR, Unit: "%"})
	}
	if o.Current.Position != nil {
		b.Add(Fact{ID: "position", Label: "Average position", Value: fmt.Sprintf("%.1f", *o.Current.Position), Raw: *o.Current.Position})
	}
	series := Series{ID: "clicks", Title: "Clicks", Unit: "clicks"}
	for _, d := range o.Series {
		series.Points = append(series.Points, Point{Day: d.Day, Value: float64(d.Clicks)})
	}
	b.Series = append(b.Series, series)

	// Branded against everything else, and the queries that moved, from the monthly
	// rollups: the month the period ends in, against the month before.
	month := time.Date(p.To.Year(), p.To.Month(), 1, 0, 0, 0, 0, time.UTC)
	prevMonth := month.AddDate(0, -1, 0)
	cur, err := queryMonth(ctx, tx, month)
	if err != nil {
		return b, err
	}
	before, err := queryMonth(ctx, tx, prevMonth)
	if err != nil {
		return b, err
	}
	if len(cur) > 0 {
		curB, curN := splitBranded(cur, in.Brand)
		prevB, prevN := splitBranded(before, in.Brand)
		t := Table{ID: "branded", Title: "Branded and non-branded, " + month.Format("January 2006"),
			Head:    []string{"Searches", "Clicks", "Change on " + prevMonth.Format("January")},
			Caption: "From the month's top queries, so smaller searches are not counted."}
		t.Rows = append(t.Rows, []string{"For " + in.Brand.Name, Num(curB), Change(curB, prevB)})
		t.Rows = append(t.Rows, []string{"Everything else", Num(curN), Change(curN, prevN)})
		b.Tables = append(b.Tables, t)
		b.Add(Fact{ID: "branded_clicks", Label: "Branded clicks", Value: Num(curB), Raw: float64(curB)})
		b.Add(Fact{ID: "nonbranded_clicks", Label: "Non-branded clicks", Value: Num(curN), Raw: float64(curN)})

		movers := movers(cur, before, 8)
		if len(movers.Rows) > 0 {
			b.Tables = append(b.Tables, movers)
		}
	}
	delta := o.Current.Clicks - o.Previous.Clicks
	switch {
	case o.Previous.Days == 0:
		b.Headline = fmt.Sprintf("Google Search brought %s clicks in %s.", Num(o.Current.Clicks), p.Label())
	case delta == 0:
		b.Headline = fmt.Sprintf("Google Search clicks held at %s.", Num(o.Current.Clicks))
	default:
		b.Headline = fmt.Sprintf("Google Search clicks are %s at %s, %s on the period before.",
			map[bool]string{true: "up", false: "down"}[delta > 0], Num(o.Current.Clicks), Change(o.Current.Clicks, o.Previous.Clicks))
	}
	b.Links = append(b.Links, Link{Label: "Search", Href: "/search"})
	return b, nil
}

type queryStat struct {
	query  string
	clicks int64
}

func queryMonth(ctx context.Context, tx pgx.Tx, month time.Time) (map[string]int64, error) {
	rows, err := tx.Query(ctx, `SELECT query, clicks FROM search_query_monthly WHERE month = $1::date`, month.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for rows.Next() {
		var q string
		var c int64
		if err := rows.Scan(&q, &c); err != nil {
			return nil, err
		}
		out[q] = c
	}
	return out, rows.Err()
}

// splitBranded divides clicks between searches that name the brand and the rest.
func splitBranded(month map[string]int64, b brand.Brand) (branded, other int64) {
	tokens := brandTokens(b)
	for q, clicks := range month {
		if isBranded(q, tokens) {
			branded += clicks
			continue
		}
		other += clicks
	}
	return branded, other
}

// brandTokens are the words that make a search a branded one.
func brandTokens(b brand.Brand) []string {
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if len(s) >= 3 {
			out = append(out, s)
		}
	}
	add(b.Name)
	for _, a := range b.Aliases {
		add(a)
	}
	if host := strings.TrimPrefix(strings.ToLower(b.Domain), "www."); host != "" {
		add(strings.SplitN(host, ".", 2)[0])
	}
	return out
}

func isBranded(query string, tokens []string) bool {
	q := strings.ToLower(query)
	for _, t := range tokens {
		if strings.Contains(q, t) {
			return true
		}
	}
	return false
}

func movers(cur, before map[string]int64, limit int) Table {
	var all []queryStat
	for q, c := range cur {
		all = append(all, queryStat{q, c - before[q]})
	}
	sort.Slice(all, func(i, j int) bool {
		if abs(float64(all[i].clicks)) != abs(float64(all[j].clicks)) {
			return abs(float64(all[i].clicks)) > abs(float64(all[j].clicks))
		}
		return all[i].query < all[j].query
	})
	t := Table{ID: "movers", Title: "Searches that moved most", Head: []string{"Search", "Clicks now", "Change"}}
	for _, s := range all {
		if s.clicks == 0 || len(t.Rows) == limit {
			break
		}
		t.Rows = append(t.Rows, []string{s.query, Num(cur[s.query]), fmt.Sprintf("%+d", s.clicks)})
	}
	return t
}

// aiReferrals answers "are AI assistants sending us traffic?".
func aiReferrals(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error) {
	p := in.Period()
	cur, points, err := searchread.LoadChannels(ctx, tx, "assistant", p.From, p.To)
	if err != nil {
		return Bundle{}, err
	}
	var b Bundle
	prev, _, err := searchread.LoadChannels(ctx, tx, "assistant", p.Previous().From, p.Previous().To)
	if err != nil {
		return b, err
	}
	var now, before int64
	beforeBy := map[string]int64{}
	for _, c := range prev {
		before += c.Sessions
		beforeBy[c.Name] = c.Sessions
	}
	t := Table{ID: "assistants", Title: "Sessions by assistant", Head: []string{"Assistant", "Sessions", "Change"}}
	for _, c := range cur {
		now += c.Sessions
		t.Rows = append(t.Rows, []string{c.Name, Num(c.Sessions), Change(c.Sessions, beforeBy[c.Name])})
	}
	if now == 0 && before == 0 {
		var ga4 int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM analytics_daily WHERE day BETWEEN $1::date AND $2::date`,
			p.From.Format(time.DateOnly), p.To.Format(time.DateOnly)).Scan(&ga4); err != nil {
			return b, err
		}
		if ga4 == 0 {
			b.Empty = "Google Analytics 4 holds no data for " + p.Label() + "."
			return b, nil
		}
		b.Add(Fact{ID: "sessions", Label: "Sessions from AI assistants", Value: "0", Raw: 0})
		b.Headline = "No visits from AI assistants were recorded in " + p.Label() + "."
		b.Links = append(b.Links, Link{Label: "AI referrals", Href: "/search/ai-referrals"})
		return b, nil
	}
	b.Add(Fact{ID: "sessions", Label: "Sessions from AI assistants", Value: Num(now), Raw: float64(now), Trend: Trend(float64(now - before))})
	b.Add(Fact{ID: "sessions_before", Label: "The period before", Value: Num(before), Raw: float64(before)})
	b.Add(Fact{ID: "change", Label: "Change", Value: Change(now, before), Raw: float64(now - before), Trend: Trend(float64(now - before))})
	b.Tables = append(b.Tables, t)
	series := Series{ID: "sessions", Title: "Sessions from AI assistants", Unit: "sessions"}
	byDay := map[string]float64{}
	var order []string
	for _, pt := range points {
		if _, seen := byDay[pt.Day]; !seen {
			order = append(order, pt.Day)
		}
		byDay[pt.Day] += float64(pt.Sessions)
	}
	for _, d := range order {
		series.Points = append(series.Points, Point{Day: d, Value: byDay[d]})
	}
	b.Series = append(b.Series, series)
	if len(cur) > 0 {
		b.Add(Fact{ID: "top_assistant", Label: "Most visits from", Value: cur[0].Name, Note: Num(cur[0].Sessions) + " sessions"})
		b.Headline = fmt.Sprintf("AI assistants sent %s sessions in %s, %s; most from %s.", Num(now), p.Label(), Change(now, before), cur[0].Name)
	} else {
		b.Headline = fmt.Sprintf("AI assistants sent no sessions in %s, against %s in the period before.", p.Label(), Num(before))
	}
	b.Links = append(b.Links, Link{Label: "AI referrals", Href: "/search/ai-referrals"})
	return b, nil
}

// fixOutcomes answers "did the fixes we made work?": each fix Vellatry confirmed live,
// with the page's clicks before and after, and the blindspots that closed since.
func fixOutcomes(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error) {
	var b Bundle
	rows, err := tx.Query(ctx, `
		SELECT title, coalesce(page_url, ''), live_at FROM fixes
		WHERE live_at IS NOT NULL AND live_at >= $1 ORDER BY live_at DESC LIMIT 15`, in.Now.AddDate(0, 0, -180))
	if err != nil {
		return b, err
	}
	type fix struct {
		title, page string
		live        time.Time
	}
	fixes, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (fix, error) {
		var f fix
		return f, r.Scan(&f.title, &f.page, &f.live)
	})
	if err != nil {
		return b, err
	}
	if len(fixes) == 0 {
		b.Empty = "no fix has been confirmed live in the last six months, so there is nothing to measure yet."
		return b, nil
	}
	t := Table{ID: "fixes", Title: "Fixes confirmed live", Head: []string{"Fix", "Live", "Page clicks before", "After"},
		Caption: "Clicks are the calendar month before and after the change, from Search Console."}
	measured := 0
	for _, f := range fixes {
		row := []string{f.title, f.live.UTC().Format("2 Jan 2006"), "-", "-"}
		if f.page != "" {
			before, after, ok, err := pageClicksAround(ctx, tx, f.page, f.live)
			if err != nil {
				return b, err
			}
			if ok {
				row[2], row[3] = Num(before), Num(after)
				measured++
			}
		}
		t.Rows = append(t.Rows, row)
	}
	b.Tables = append(b.Tables, t)
	b.Add(Fact{ID: "fixes_live", Label: "Fixes confirmed live", Value: Num(int64(len(fixes))), Raw: float64(len(fixes))})
	b.Add(Fact{ID: "measured", Label: "With a full month either side", Value: Num(int64(measured)), Raw: float64(measured)})

	var resolved int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM blindspots WHERE status = 'resolved' AND last_seen >= $1`,
		fixes[len(fixes)-1].live).Scan(&resolved); err != nil {
		return b, err
	}
	b.Add(Fact{ID: "blindspots_resolved", Label: "Blindspots closed since", Value: Num(int64(resolved)), Raw: float64(resolved),
		Note: "questions where an engine started mentioning you again"})
	b.Headline = fmt.Sprintf("%s fixes have been confirmed live, and %s blindspots closed since the first of them.",
		Num(int64(len(fixes))), Num(int64(resolved)))
	b.Links = append(b.Links, Link{Label: "Fixes", Href: "/fixes"})
	return b, nil
}

// pageClicksAround returns a page's clicks in the calendar month before a change and
// the month after, when both are in the rollups.
func pageClicksAround(ctx context.Context, tx pgx.Tx, page string, live time.Time) (int64, int64, bool, error) {
	month := time.Date(live.Year(), live.Month(), 1, 0, 0, 0, 0, time.UTC)
	var before, after *int64
	if err := tx.QueryRow(ctx, `SELECT clicks FROM search_page_monthly WHERE page = $1 AND month = $2::date`,
		page, month.AddDate(0, -1, 0).Format(time.DateOnly)).Scan(&before); err != nil && !isNoRows(err) {
		return 0, 0, false, err
	}
	if err := tx.QueryRow(ctx, `SELECT clicks FROM search_page_monthly WHERE page = $1 AND month = $2::date`,
		page, month.AddDate(0, 1, 0).Format(time.DateOnly)).Scan(&after); err != nil && !isNoRows(err) {
		return 0, 0, false, err
	}
	if before == nil || after == nil {
		return 0, 0, false, nil
	}
	return *before, *after, true, nil
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
