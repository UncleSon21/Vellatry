package reports

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/searchread"
	"github.com/UncleSon21/vellatry/internal/visibility/read"
)

// SnapshotVersion is recorded on every snapshot so old reports keep rendering as they
// were published after the builder changes.
const SnapshotVersion = "report-1"

// Section keys, in the default order.
const (
	SecVisibility  = "visibility"
	SecBlindspots  = "blindspots"
	SecSearch      = "search"
	SecAIReferrals = "ai_referrals"
	SecTraffic     = "traffic"
	SecSite        = "site"
	SecFixes       = "fixes"
)

// AllSections lists every section a series can include.
var AllSections = []string{SecVisibility, SecBlindspots, SecSearch, SecAIReferrals, SecTraffic, SecSite, SecFixes}

// Snapshot is a report's data, frozen when the draft is made. Publishing copies it
// into a version, so a published report never changes under the reader.
type Snapshot struct {
	Version     string    `json:"version"`
	Org         string    `json:"org"`
	Brand       string    `json:"brand"`
	Period      Period    `json:"period"`
	Previous    Period    `json:"previous"`
	GeneratedAt time.Time `json:"generated_at"`
	Order       []string  `json:"order"` // sections present, in the series' order

	Visibility  *VisibilitySection `json:"visibility,omitempty"`
	Blindspots  *BlindspotSection  `json:"blindspots,omitempty"`
	Search      *SearchSection     `json:"search,omitempty"`
	AIReferrals *ChannelSection    `json:"ai_referrals,omitempty"`
	Traffic     *ChannelSection    `json:"traffic,omitempty"`
	Site        *SiteSection       `json:"site,omitempty"`
	Fixes       *FixesSection      `json:"fixes,omitempty"`

	// Team only, never rendered for the CMO: why a section is missing, and data that is
	// present but incomplete. The absence rule keeps these out of the report itself.
	Omitted  []Note `json:"omitted,omitempty"`
	Warnings []Note `json:"warnings,omitempty"`
}

// Note is a team-only remark about a section.
type Note struct {
	Section string `json:"section"`
	Reason  string `json:"reason"`
}

// Empty reports whether the snapshot has nothing to report.
func (s Snapshot) Empty() bool { return len(s.Order) == 0 }

// Pair is a current and a previous value.
type Pair struct {
	Current  *float64 `json:"current"`
	Previous *float64 `json:"previous"`
}

// IntPair is a current and a previous count.
type IntPair struct {
	Current  int64  `json:"current"`
	Previous *int64 `json:"previous"` // nil when the previous period has no data
}

// DayValue is one point of a chart.
type DayValue struct {
	Day   string   `json:"day"`
	Value *float64 `json:"value"`
}

// VisibilitySection is AI visibility against competitors.
type VisibilitySection struct {
	Answers      int         `json:"answers"`
	Visibility   Pair        `json:"visibility"`
	ShareOfVoice Pair        `json:"share_of_voice"`
	Position     Pair        `json:"position"`
	ByEngine     []EngineRow `json:"by_engine"`
	Entities     []EntityRow `json:"entities"`
	Sources      []SourceRow `json:"sources"`
	Series       []DayValue  `json:"series"` // overall daily visibility, %
}

// EngineRow is one engine.
type EngineRow struct {
	Engine     string `json:"engine"`
	Answers    int    `json:"answers"`
	Visibility Pair   `json:"visibility"`
}

// EntityRow is the brand or one competitor.
type EntityRow struct {
	Name         string   `json:"name"`
	IsBrand      bool     `json:"is_brand"`
	Visibility   *float64 `json:"visibility"`
	ShareOfVoice *float64 `json:"share_of_voice"`
}

// SourceRow is one cited domain.
type SourceRow struct {
	Domain    string `json:"domain"`
	Type      string `json:"type"`
	Citations int    `json:"citations"`
}

// BlindspotSection is blindspot movement in the period.
type BlindspotSection struct {
	Confirmed int            `json:"confirmed"` // newly confirmed in the period
	Resolved  int            `json:"resolved"`
	OpenNow   int            `json:"open_now"`
	Top       []BlindspotRow `json:"top"`
}

// BlindspotRow is one open blindspot.
type BlindspotRow struct {
	Engine     string  `json:"engine"`
	Kind       string  `json:"kind"`
	Prompt     string  `json:"prompt"`
	Topic      *string `json:"topic"`
	Competitor *string `json:"competitor"`
}

// SearchSection is Google Search performance from Search Console.
type SearchSection struct {
	Clicks      IntPair    `json:"clicks"`
	Impressions IntPair    `json:"impressions"`
	CTR         Pair       `json:"ctr"`
	Position    Pair       `json:"position"`
	Series      []DayValue `json:"series"` // clicks per day
	Queries     []TopRow   `json:"queries"`
	Pages       []TopRow   `json:"pages"`
}

// TopRow is one query or page.
type TopRow struct {
	Key         string   `json:"key"`
	Clicks      int64    `json:"clicks"`
	Impressions int64    `json:"impressions"`
	Position    *float64 `json:"position"`
}

// ChannelSection is GA4 sessions by channel or by AI assistant.
type ChannelSection struct {
	Sessions  IntPair      `json:"sessions"`
	KeyEvents Pair         `json:"key_events"`
	Rows      []ChannelRow `json:"rows"`
}

// ChannelRow is one channel or assistant.
type ChannelRow struct {
	Name     string `json:"name"`
	Sessions int64  `json:"sessions"`
	Previous *int64 `json:"previous"`
}

// SiteSection is the technical state at the time of the report.
type SiteSection struct {
	CrawledOn string      `json:"crawled_on"`
	Pages     int         `json:"pages"`
	Critical  int         `json:"critical"`
	Warning   int         `json:"warning"`
	LLMSTxt   bool        `json:"llms_txt"`
	AIAccess  []BotAccess `json:"ai_access"`
}

// BotAccess is one AI crawler's access.
type BotAccess struct {
	Agent   string `json:"agent"`
	Product string `json:"product"`
	Purpose string `json:"purpose"`
	Blocked bool   `json:"blocked"`
}

// FixesSection is what the team shipped.
type FixesSection struct {
	Live       []FixRow `json:"live"` // confirmed live in the period
	InProgress int      `json:"in_progress"`
}

// FixRow is one fix confirmed live.
type FixRow struct {
	Title  string  `json:"title"`
	Page   *string `json:"page"`
	LiveOn string  `json:"live_on"`
}

// Build freezes the data for one report. Sections whose source is missing, broken or
// empty for the period are left out and noted for the team only.
func Build(ctx context.Context, tx pgx.Tx, sections []string, rule string, p Period, now time.Time) (Snapshot, error) {
	s := Snapshot{Version: SnapshotVersion, Period: p, Previous: Previous(rule, p), GeneratedAt: now.UTC()}
	b, comps, err := brand.Load(ctx, tx)
	if err != nil {
		return s, err
	}
	s.Brand = b.Name
	if err := tx.QueryRow(ctx, `SELECT name FROM orgs`).Scan(&s.Org); err != nil {
		return s, err
	}
	conns, err := connectionStatus(ctx, tx)
	if err != nil {
		return s, err
	}
	for _, sec := range sections {
		var reason string
		switch sec {
		case SecVisibility:
			var compDomains []string
			for _, c := range comps {
				compDomains = append(compDomains, c.Domains...)
			}
			s.Visibility, reason, err = buildVisibility(ctx, tx, p, s.Previous, b.Domain, compDomains)
		case SecBlindspots:
			var answers int
			if err = tx.QueryRow(ctx, `SELECT coalesce(sum(answers), 0)::int FROM visibility_daily WHERE day BETWEEN $1::date AND $2::date`,
				p.Start.Format(time.DateOnly), p.End.Format(time.DateOnly)).Scan(&answers); err == nil {
				if answers == 0 {
					reason = "no AI answers were collected in this period"
				} else {
					s.Blindspots, err = buildBlindspots(ctx, tx, p)
				}
			}
		case SecSearch:
			if reason = connReason(conns, "search_console", "Search Console"); reason == "" {
				s.Search, reason, err = buildSearch(ctx, tx, p, s.Previous, &s)
			}
		case SecAIReferrals, SecTraffic:
			if reason = connReason(conns, "ga4", "Google Analytics 4"); reason == "" {
				var c *ChannelSection
				kind := "channel"
				if sec == SecAIReferrals {
					kind = "assistant"
				}
				c, reason, err = buildChannels(ctx, tx, kind, p, s.Previous, &s, sec)
				if sec == SecAIReferrals {
					s.AIReferrals = c
				} else {
					s.Traffic = c
				}
			}
		case SecSite:
			s.Site, reason, err = buildSite(ctx, tx)
		case SecFixes:
			s.Fixes, reason, err = buildFixes(ctx, tx, p)
		default:
			continue
		}
		if err != nil {
			return s, err
		}
		if reason != "" {
			s.Omitted = append(s.Omitted, Note{Section: sec, Reason: reason})
			continue
		}
		if s.present(sec) {
			s.Order = append(s.Order, sec)
		}
	}
	return s, nil
}

func (s Snapshot) present(sec string) bool {
	switch sec {
	case SecVisibility:
		return s.Visibility != nil
	case SecBlindspots:
		return s.Blindspots != nil
	case SecSearch:
		return s.Search != nil
	case SecAIReferrals:
		return s.AIReferrals != nil
	case SecTraffic:
		return s.Traffic != nil
	case SecSite:
		return s.Site != nil
	case SecFixes:
		return s.Fixes != nil
	}
	return false
}

func connectionStatus(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT kind, status FROM connections`)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for rows.Next() {
		var k, st string
		if err := rows.Scan(&k, &st); err != nil {
			return nil, err
		}
		out[k] = st
	}
	return out, rows.Err()
}

func connReason(conns map[string]string, kind, name string) string {
	switch conns[kind] {
	case "connected":
		return ""
	case "":
		return name + " is not connected"
	}
	return name + " is " + conns[kind] + "; reconnect it and redraft"
}

func round(v float64, places int) *float64 {
	p := math.Pow(10, float64(places))
	r := math.Round(v*p) / p
	return &r
}

func buildVisibility(ctx context.Context, tx pgx.Tx, p, prev Period, brandDomain string, compDomains []string) (*VisibilitySection, string, error) {
	cur, err := read.LoadPerformance(ctx, tx, p.Start, p.End)
	if err != nil {
		return nil, "", err
	}
	if cur.Overall.Answers == 0 {
		return nil, "no AI answers were collected in this period", nil
	}
	before, err := read.LoadPerformance(ctx, tx, prev.Start, prev.End)
	if err != nil {
		return nil, "", err
	}
	v := &VisibilitySection{
		Answers:      cur.Overall.Answers,
		Visibility:   Pair{Current: cur.Overall.Visibility},
		ShareOfVoice: Pair{Current: cur.Overall.ShareOfVoice},
		Position:     Pair{Current: cur.Overall.AvgPosition},
		ByEngine:     []EngineRow{}, Entities: []EntityRow{}, Sources: []SourceRow{}, Series: []DayValue{},
	}
	if before.Overall.Answers > 0 {
		v.Visibility.Previous, v.ShareOfVoice.Previous, v.Position.Previous = before.Overall.Visibility, before.Overall.ShareOfVoice, before.Overall.AvgPosition
	}
	prevEngine := map[string]*float64{}
	for _, m := range before.ByEngine {
		if m.Answers > 0 {
			prevEngine[m.Engine] = m.Visibility
		}
	}
	for _, m := range cur.ByEngine {
		if m.Answers > 0 {
			v.ByEngine = append(v.ByEngine, EngineRow{Engine: m.Engine, Answers: m.Answers, Visibility: Pair{m.Visibility, prevEngine[m.Engine]}})
		}
	}
	for _, e := range cur.Entities {
		v.Entities = append(v.Entities, EntityRow{Name: e.Name, IsBrand: e.IsBrand, Visibility: e.Visibility, ShareOfVoice: e.ShareOfVoice})
		if len(v.Entities) == 8 {
			break
		}
	}
	// Overall visibility per day, weighted by each engine's answers.
	type acc struct{ weighted, n float64 }
	days := map[string]*acc{}
	var order []string
	for _, pt := range cur.Series {
		a := days[pt.Day]
		if a == nil {
			a = &acc{}
			days[pt.Day] = a
			order = append(order, pt.Day)
		}
		if pt.Visibility != nil && pt.Answers > 0 {
			a.weighted += *pt.Visibility * float64(pt.Answers)
			a.n += float64(pt.Answers)
		}
	}
	for _, d := range order {
		dv := DayValue{Day: d}
		if a := days[d]; a.n > 0 {
			dv.Value = round(a.weighted/a.n, 1)
		}
		v.Series = append(v.Series, dv)
	}
	srcs, err := read.LoadSources(ctx, tx, p.Start, p.End, brandDomain, compDomains, 8)
	if err != nil {
		return nil, "", err
	}
	for _, r := range srcs {
		v.Sources = append(v.Sources, SourceRow{Domain: r.Domain, Type: string(r.Type), Citations: r.Citations})
	}
	return v, "", nil
}

func buildBlindspots(ctx context.Context, tx pgx.Tx, p Period) (*BlindspotSection, error) {
	end := p.End.AddDate(0, 0, 1)
	b := &BlindspotSection{Top: []BlindspotRow{}}
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE confirmed AND first_seen >= $1 AND first_seen < $2),
		       count(*) FILTER (WHERE status = 'resolved' AND last_seen >= $1 AND last_seen < $2),
		       count(*) FILTER (WHERE status = 'open' AND confirmed)
		FROM blindspots`, p.Start, end).Scan(&b.Confirmed, &b.Resolved, &b.OpenNow); err != nil {
		return nil, err
	}
	open, err := read.LoadBlindspots(ctx, tx, "open")
	if err != nil {
		return nil, err
	}
	for _, o := range open {
		if !o.Confirmed {
			continue
		}
		b.Top = append(b.Top, BlindspotRow{Engine: o.Engine, Kind: o.Kind, Prompt: o.Prompt, Topic: o.Topic, Competitor: o.Competitor})
		if len(b.Top) == 5 {
			break
		}
	}
	return b, nil
}

func buildSearch(ctx context.Context, tx pgx.Tx, p, prev Period, s *Snapshot) (*SearchSection, string, error) {
	o, err := searchread.LoadOverview(ctx, tx, p.Start, p.End)
	if err != nil {
		return nil, "", err
	}
	if o.Current.Days == 0 {
		return nil, "Search Console has no data for this period", nil
	}
	if o.Current.Days < p.Days() {
		s.Warnings = append(s.Warnings, Note{Section: SecSearch,
			Reason: "Search Console has " + itoa(o.Current.Days) + " of " + itoa(p.Days()) + " days for this period; totals are for the days available"})
	}
	sec := &SearchSection{
		Clicks:      IntPair{Current: o.Current.Clicks},
		Impressions: IntPair{Current: o.Current.Impressions},
		CTR:         Pair{Current: o.Current.CTR}, Position: Pair{Current: o.Current.Position},
		Series: []DayValue{}, Queries: []TopRow{}, Pages: []TopRow{},
	}
	if o.Previous.Days > 0 {
		c, i := o.Previous.Clicks, o.Previous.Impressions
		sec.Clicks.Previous, sec.Impressions.Previous = &c, &i
		sec.CTR.Previous, sec.Position.Previous = o.Previous.CTR, o.Previous.Position
	}
	for _, d := range o.Series {
		v := float64(d.Clicks)
		sec.Series = append(sec.Series, DayValue{Day: d.Day, Value: &v})
	}
	for _, kind := range []string{"query", "page"} {
		rows, err := topInPeriod(ctx, tx, kind, p)
		if err != nil {
			return nil, "", err
		}
		if kind == "query" {
			sec.Queries = rows
		} else {
			sec.Pages = rows
		}
	}
	return sec, "", nil
}

// topInPeriod sums the monthly top lists over the months the period covers.
func topInPeriod(ctx context.Context, tx pgx.Tx, kind string, p Period) ([]TopRow, error) {
	table, col := "search_query_monthly", "query"
	if kind == "page" {
		table, col = "search_page_monthly", "page"
	}
	rows, err := tx.Query(ctx, `
		SELECT `+col+`, sum(clicks)::bigint, sum(impressions)::bigint, sum(position_sum)::float8
		FROM `+table+` WHERE month >= date_trunc('month', $1::date) AND month <= $2::date
		GROUP BY `+col+` ORDER BY sum(clicks) DESC, sum(impressions) DESC LIMIT 10`,
		p.Start.Format(time.DateOnly), p.End.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (TopRow, error) {
		var t TopRow
		var posSum float64
		if err := r.Scan(&t.Key, &t.Clicks, &t.Impressions, &posSum); err != nil {
			return t, err
		}
		if t.Impressions > 0 {
			t.Position = round(posSum/float64(t.Impressions), 1)
		}
		return t, nil
	})
	if out == nil {
		out = []TopRow{}
	}
	return out, err
}

func buildChannels(ctx context.Context, tx pgx.Tx, kind string, p, prev Period, s *Snapshot, sec string) (*ChannelSection, string, error) {
	var days int
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT day) FROM analytics_daily WHERE day BETWEEN $1::date AND $2::date`,
		p.Start.Format(time.DateOnly), p.End.Format(time.DateOnly)).Scan(&days); err != nil {
		return nil, "", err
	}
	if days == 0 {
		return nil, "Google Analytics 4 has no data for this period", nil
	}
	if days < p.Days() {
		s.Warnings = append(s.Warnings, Note{Section: sec, Reason: "GA4 has " + itoa(days) + " of " + itoa(p.Days()) + " days for this period"})
	}
	cur, _, err := searchread.LoadChannels(ctx, tx, kind, p.Start, p.End)
	if err != nil {
		return nil, "", err
	}
	before, _, err := searchread.LoadChannels(ctx, tx, kind, prev.Start, prev.End)
	if err != nil {
		return nil, "", err
	}
	var prevDays int
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT day) FROM analytics_daily WHERE day BETWEEN $1::date AND $2::date`,
		prev.Start.Format(time.DateOnly), prev.End.Format(time.DateOnly)).Scan(&prevDays); err != nil {
		return nil, "", err
	}
	c := &ChannelSection{Rows: []ChannelRow{}}
	prevBy := map[string]int64{}
	var prevSessions int64
	var prevKey float64
	for _, x := range before {
		prevBy[x.Name] = x.Sessions
		prevSessions += x.Sessions
		prevKey += x.KeyEvents
	}
	var key float64
	for _, x := range cur {
		c.Sessions.Current += x.Sessions
		key += x.KeyEvents
		row := ChannelRow{Name: x.Name, Sessions: x.Sessions}
		if prevDays > 0 {
			v := prevBy[x.Name]
			row.Previous = &v
		}
		if len(c.Rows) < 8 {
			c.Rows = append(c.Rows, row)
		}
	}
	c.KeyEvents.Current = round(key, 0)
	if prevDays > 0 {
		c.Sessions.Previous = &prevSessions
		c.KeyEvents.Previous = round(prevKey, 0)
	}
	return c, "", nil
}

func buildSite(ctx context.Context, tx pgx.Tx) (*SiteSection, string, error) {
	var finished time.Time
	var pages int
	var summary []byte
	// The latest crawl: site health is reported as of that date, not reconstructed.
	err := tx.QueryRow(ctx, `SELECT finished_at, pages, summary FROM crawls WHERE status = 'done'
		ORDER BY finished_at DESC LIMIT 1`).Scan(&finished, &pages, &summary)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "the site has not been crawled yet", nil
	}
	if err != nil {
		return nil, "", err
	}
	s := &SiteSection{CrawledOn: finished.UTC().Format(time.DateOnly), Pages: pages, AIAccess: []BotAccess{}}
	var sum struct {
		LLMSTxt  bool        `json:"llms_txt"`
		AIAccess []BotAccess `json:"ai_access"`
	}
	_ = json.Unmarshal(summary, &sum)
	s.LLMSTxt = sum.LLMSTxt
	for _, a := range sum.AIAccess {
		if a.Purpose == "search" {
			s.AIAccess = append(s.AIAccess, a)
		}
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE severity = 'critical'), count(*) FILTER (WHERE severity = 'warning')
		FROM audit_findings WHERE status = 'open'`).Scan(&s.Critical, &s.Warning)
	return s, "", err
}

func buildFixes(ctx context.Context, tx pgx.Tx, p Period) (*FixesSection, string, error) {
	rows, err := tx.Query(ctx, `SELECT title, page_url, live_at FROM fixes WHERE live_at >= $1 AND live_at < $2 ORDER BY live_at LIMIT 30`,
		p.Start, p.End.AddDate(0, 0, 1))
	if err != nil {
		return nil, "", err
	}
	live, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (FixRow, error) {
		var f FixRow
		var at time.Time
		err := r.Scan(&f.Title, &f.Page, &at)
		f.LiveOn = at.UTC().Format(time.DateOnly)
		return f, err
	})
	if err != nil {
		return nil, "", err
	}
	f := &FixesSection{Live: live}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM fixes WHERE status = 'sent'`).Scan(&f.InProgress); err != nil {
		return nil, "", err
	}
	if len(f.Live) == 0 && f.InProgress == 0 {
		return nil, "no fixes went live or were in progress in this period", nil
	}
	if f.Live == nil {
		f.Live = []FixRow{}
	}
	return f, "", nil
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
