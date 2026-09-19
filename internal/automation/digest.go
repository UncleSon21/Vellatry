package automation

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/searchread"
	"github.com/UncleSon21/vellatry/internal/visibility/read"
)

// Digest is the weekly summary for the marketing team. Every figure comes from stored
// rollups; nothing is written by a model. A section with no data is left out.
type Digest struct {
	Org         string            `json:"org"`
	Brand       string            `json:"brand"`
	From        string            `json:"from"`
	To          string            `json:"to"`
	Visibility  *DigestVisibility `json:"visibility,omitempty"`
	Blindspots  *DigestBlindspots `json:"blindspots,omitempty"`
	Search      *DigestSearch     `json:"search,omitempty"`
	AIReferrals *DigestReferrals  `json:"ai_referrals,omitempty"`
	Site        *DigestSite       `json:"site,omitempty"`
	Alerts      []DigestAlert     `json:"alerts,omitempty"`
	Broken      []string          `json:"broken,omitempty"` // connections and destinations needing attention
}

// DigestVisibility is AI visibility this week against last.
type DigestVisibility struct {
	Answers      int          `json:"answers"`
	Visibility   *float64     `json:"visibility"`
	Previous     *float64     `json:"previous"`
	ShareOfVoice *float64     `json:"share_of_voice"`
	PreviousSoV  *float64     `json:"previous_sov"`
	ByEngine     []EngineLine `json:"by_engine"`
}

// EngineLine is one engine's visibility this week and last.
type EngineLine struct {
	Engine     string   `json:"engine"`
	Visibility *float64 `json:"visibility"`
	Previous   *float64 `json:"previous"`
}

// DigestBlindspots is the week's blindspot movement.
type DigestBlindspots struct {
	Open     int               `json:"open"`
	New      int               `json:"new"`
	Resolved int               `json:"resolved"`
	Top      []DigestBlindspot `json:"top"`
}

// DigestBlindspot is one new confirmed blindspot.
type DigestBlindspot struct {
	ID         int64   `json:"id"`
	Engine     string  `json:"engine"`
	Kind       string  `json:"kind"`
	Prompt     string  `json:"prompt"`
	Competitor *string `json:"competitor"`
}

// DigestSearch is Search Console's latest seven days with data against the seven before.
type DigestSearch struct {
	From            string   `json:"from"`
	To              string   `json:"to"`
	Clicks          int64    `json:"clicks"`
	PrevClicks      int64    `json:"prev_clicks"`
	Impressions     int64    `json:"impressions"`
	PrevImpressions int64    `json:"prev_impressions"`
	Position        *float64 `json:"position"`
	PrevPosition    *float64 `json:"prev_position"`
}

// DigestReferrals is sessions referred by AI assistants.
type DigestReferrals struct {
	From         string          `json:"from"`
	To           string          `json:"to"`
	Sessions     int64           `json:"sessions"`
	PrevSessions int64           `json:"prev_sessions"`
	Top          []NamedSessions `json:"top"`
}

// NamedSessions is sessions from one assistant.
type NamedSessions struct {
	Name     string `json:"name"`
	Sessions int64  `json:"sessions"`
}

// DigestSite is the technical side.
type DigestSite struct {
	LastCrawl     string `json:"last_crawl"`
	Critical      int    `json:"critical"`
	Warning       int    `json:"warning"`
	FixesLive     int    `json:"fixes_live"`     // confirmed live this week
	FixesProposed int    `json:"fixes_proposed"` // waiting for someone
}

// DigestAlert is a watcher notification that was set to go in the digest.
type DigestAlert struct {
	Severity    string `json:"severity"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Link        string `json:"link,omitempty"`
	Occurrences int    `json:"occurrences"`
}

// Empty reports whether there is nothing worth sending.
func (d Digest) Empty() bool {
	return d.Visibility == nil && d.Blindspots == nil && d.Search == nil && d.AIReferrals == nil && d.Site == nil &&
		len(d.Alerts) == 0 && len(d.Broken) == 0
}

// BuildDigest assembles the digest for [from, to] (UTC dates, inclusive) and returns
// the ids of the pending digest notifications it included.
func BuildDigest(ctx context.Context, tx pgx.Tx, from, to time.Time) (Digest, []int64, error) {
	d := Digest{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly)}
	if err := tx.QueryRow(ctx, `SELECT o.name, coalesce(b.name, '') FROM orgs o LEFT JOIN brands b ON b.org_id = o.id ORDER BY b.created_at LIMIT 1`).
		Scan(&d.Org, &d.Brand); err != nil {
		return d, nil, err
	}
	var err error
	if d.Visibility, err = digestVisibility(ctx, tx, from, to); err != nil {
		return d, nil, err
	}
	if d.Visibility != nil {
		if d.Blindspots, err = digestBlindspots(ctx, tx, from, to); err != nil {
			return d, nil, err
		}
	}
	if d.Search, err = digestSearch(ctx, tx); err != nil {
		return d, nil, err
	}
	if d.AIReferrals, err = digestReferrals(ctx, tx); err != nil {
		return d, nil, err
	}
	if d.Site, err = digestSite(ctx, tx, from, to); err != nil {
		return d, nil, err
	}
	pending, err := PendingDigest(ctx, tx, 20)
	if err != nil {
		return d, nil, err
	}
	var ids []int64
	for _, n := range pending {
		a := DigestAlert{Severity: n.Severity, Title: n.Title, Body: n.Body, Occurrences: n.Occurrences}
		if n.Link != nil {
			a.Link = *n.Link
		}
		d.Alerts = append(d.Alerts, a)
		ids = append(ids, n.ID)
	}
	rows, err := tx.Query(ctx, `
		SELECT kind FROM connections WHERE status = 'broken'
		UNION ALL SELECT name FROM destinations WHERE status = 'broken'
		ORDER BY 1`)
	if err != nil {
		return d, nil, err
	}
	broken, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return d, nil, err
	}
	for _, b := range broken {
		if n := connectionNames[b]; n != "" {
			b = n
		}
		d.Broken = append(d.Broken, b)
	}
	return d, ids, nil
}

func digestVisibility(ctx context.Context, tx pgx.Tx, from, to time.Time) (*DigestVisibility, error) {
	cur, err := read.LoadPerformance(ctx, tx, from, to)
	if err != nil || cur.Overall.Answers == 0 {
		return nil, err
	}
	prev, err := read.LoadPerformance(ctx, tx, from.AddDate(0, 0, -7), to.AddDate(0, 0, -7))
	if err != nil {
		return nil, err
	}
	v := &DigestVisibility{Answers: cur.Overall.Answers, Visibility: cur.Overall.Visibility, ShareOfVoice: cur.Overall.ShareOfVoice}
	if prev.Overall.Answers > 0 {
		v.Previous, v.PreviousSoV = prev.Overall.Visibility, prev.Overall.ShareOfVoice
	}
	before := map[string]*float64{}
	for _, m := range prev.ByEngine {
		if m.Answers > 0 {
			before[m.Engine] = m.Visibility
		}
	}
	for _, m := range cur.ByEngine {
		if m.Answers > 0 {
			v.ByEngine = append(v.ByEngine, EngineLine{Engine: m.Engine, Visibility: m.Visibility, Previous: before[m.Engine]})
		}
	}
	return v, nil
}

func digestBlindspots(ctx context.Context, tx pgx.Tx, from, to time.Time) (*DigestBlindspots, error) {
	end := to.AddDate(0, 0, 1)
	b := &DigestBlindspots{Top: []DigestBlindspot{}}
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'open' AND confirmed),
		       count(*) FILTER (WHERE status = 'open' AND confirmed AND first_seen >= $1 AND first_seen < $2),
		       count(*) FILTER (WHERE status = 'resolved' AND last_seen >= $1 AND last_seen < $2)
		FROM blindspots`, from, end).Scan(&b.Open, &b.New, &b.Resolved); err != nil {
		return nil, err
	}
	if b.Open == 0 && b.New == 0 && b.Resolved == 0 {
		return nil, nil
	}
	open, err := read.LoadBlindspots(ctx, tx, "open")
	if err != nil {
		return nil, err
	}
	for _, s := range open {
		if len(b.Top) == 3 {
			break
		}
		if s.Confirmed && !s.FirstSeen.Before(from) && s.FirstSeen.Before(end) {
			b.Top = append(b.Top, DigestBlindspot{ID: s.ID, Engine: s.Engine, Kind: s.Kind, Prompt: s.Prompt, Competitor: s.Competitor})
		}
	}
	return b, nil
}

// latestWeek is the seven days ending on the newest day that has data in table.
func latestWeek(ctx context.Context, tx pgx.Tx, table string) (time.Time, time.Time, bool, error) {
	var last *time.Time
	if err := tx.QueryRow(ctx, `SELECT max(day) FROM `+table).Scan(&last); err != nil || last == nil {
		return time.Time{}, time.Time{}, false, err
	}
	to := last.UTC()
	return to.AddDate(0, 0, -6), to, true, nil
}

func digestSearch(ctx context.Context, tx pgx.Tx) (*DigestSearch, error) {
	from, to, ok, err := latestWeek(ctx, tx, "search_daily")
	if err != nil || !ok {
		return nil, err
	}
	o, err := searchread.LoadOverview(ctx, tx, from, to)
	if err != nil || o.Current.Days == 0 {
		return nil, err
	}
	return &DigestSearch{From: o.From, To: o.To, Clicks: o.Current.Clicks, PrevClicks: o.Previous.Clicks,
		Impressions: o.Current.Impressions, PrevImpressions: o.Previous.Impressions,
		Position: o.Current.Position, PrevPosition: o.Previous.Position}, nil
}

func digestReferrals(ctx context.Context, tx pgx.Tx) (*DigestReferrals, error) {
	from, to, ok, err := latestWeek(ctx, tx, "ai_referral_daily")
	if err != nil || !ok {
		return nil, err
	}
	cur, _, err := searchread.LoadChannels(ctx, tx, "assistant", from, to)
	if err != nil {
		return nil, err
	}
	prev, _, err := searchread.LoadChannels(ctx, tx, "assistant", from.AddDate(0, 0, -7), to.AddDate(0, 0, -7))
	if err != nil {
		return nil, err
	}
	r := &DigestReferrals{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Top: []NamedSessions{}}
	for _, c := range cur {
		r.Sessions += c.Sessions
		if len(r.Top) < 3 && c.Sessions > 0 {
			r.Top = append(r.Top, NamedSessions{Name: c.Name, Sessions: c.Sessions})
		}
	}
	for _, c := range prev {
		r.PrevSessions += c.Sessions
	}
	if r.Sessions == 0 && r.PrevSessions == 0 {
		return nil, nil
	}
	return r, nil
}

func digestSite(ctx context.Context, tx pgx.Tx, from, to time.Time) (*DigestSite, error) {
	var last *time.Time
	if err := tx.QueryRow(ctx, `SELECT max(finished_at) FROM crawls WHERE status = 'done'`).Scan(&last); err != nil || last == nil {
		return nil, err
	}
	s := &DigestSite{LastCrawl: last.UTC().Format(time.DateOnly)}
	err := tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM audit_findings WHERE status = 'open' AND severity = 'critical'),
		       (SELECT count(*) FROM audit_findings WHERE status = 'open' AND severity = 'warning'),
		       (SELECT count(*) FROM fixes WHERE live_at >= $1 AND live_at < $2),
		       (SELECT count(*) FROM fixes WHERE status = 'proposed')`,
		from, to.AddDate(0, 0, 1)).Scan(&s.Critical, &s.Warning, &s.FixesLive, &s.FixesProposed)
	return s, err
}
