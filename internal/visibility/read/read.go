// Package read answers the dashboard's Visibility questions from stored rows only.
// It is safe for the api role: no external calls, and every query runs in a
// transaction scoped to one org.
package read

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// Metrics are Performance numbers for one engine or overall.
type Metrics struct {
	Engine       string   `json:"engine"`
	Answers      int      `json:"answers"`
	Present      int      `json:"present"`
	Mentioned    int      `json:"mentioned"`
	Visibility   *float64 `json:"visibility"`     // % of AI answers mentioning the brand
	ShareOfVoice *float64 `json:"share_of_voice"` // % of tracked-brand mentions that are the brand
	AvgPosition  *float64 `json:"avg_position"`   // rank among tracked brands when mentioned
	Sentiment    *float64 `json:"sentiment"`      // 1-10, judged answers only
}

// Point is one day of one engine.
type Point struct {
	Day        string   `json:"day"`
	Engine     string   `json:"engine"`
	Visibility *float64 `json:"visibility"`
	Answers    int      `json:"answers"`
}

// Entity is the brand or a competitor over the period.
type Entity struct {
	Name         string   `json:"name"`
	IsBrand      bool     `json:"is_brand"`
	Mentions     int      `json:"mentions"`
	Answers      int      `json:"answers"`
	Visibility   *float64 `json:"visibility"`
	ShareOfVoice *float64 `json:"share_of_voice"`
}

// Performance is the Performance page.
type Performance struct {
	From           string    `json:"from"`
	To             string    `json:"to"`
	Overall        Metrics   `json:"overall"`
	ByEngine       []Metrics `json:"by_engine"`
	Series         []Point   `json:"series"`
	Entities       []Entity  `json:"entities"`
	MethodVersions []string  `json:"method_versions"`
}

// LoadPerformance computes Performance for [from, to] (UTC dates, inclusive).
func LoadPerformance(ctx context.Context, tx pgx.Tx, from, to time.Time) (Performance, error) {
	p := Performance{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), ByEngine: []Metrics{}, Series: []Point{}, Entities: []Entity{}}
	type sums struct {
		answers, present, mentioned, posSum, posN, sentN int
		sentSum                                          float64
	}
	byEngine := map[string]*sums{}
	var overall sums
	versions := map[string]bool{}

	rows, err := tx.Query(ctx, `
		SELECT day::text, engine, answers, present, mentioned, position_sum, position_n, sentiment_sum::float8, sentiment_n, method_version
		FROM visibility_daily WHERE day BETWEEN $1::date AND $2::date ORDER BY day, engine`, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var day, eng, version string
		var s sums
		if err := rows.Scan(&day, &eng, &s.answers, &s.present, &s.mentioned, &s.posSum, &s.posN, &s.sentSum, &s.sentN, &version); err != nil {
			return p, err
		}
		versions[version] = true
		p.Series = append(p.Series, Point{Day: day, Engine: eng, Visibility: pct(s.mentioned, s.present), Answers: s.answers})
		e := byEngine[eng]
		if e == nil {
			e = &sums{}
			byEngine[eng] = e
		}
		for _, t := range []*sums{e, &overall} {
			t.answers += s.answers
			t.present += s.present
			t.mentioned += s.mentioned
			t.posSum += s.posSum
			t.posN += s.posN
			t.sentSum += s.sentSum
			t.sentN += s.sentN
		}
	}
	if err := rows.Err(); err != nil {
		return p, err
	}

	// Mentions per entity, overall and per engine, for share of voice.
	type ent struct {
		isBrand           bool
		mentions, answers int
	}
	entities := map[string]*ent{}
	brandByEngine, allByEngine := map[string]int{}, map[string]int{}
	brandAll, all := 0, 0
	rows, err = tx.Query(ctx, `
		SELECT engine, entity, is_brand, sum(mentions)::int, sum(answers)::int
		FROM visibility_daily_entities WHERE day BETWEEN $1::date AND $2::date GROUP BY engine, entity, is_brand`, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var eng, name string
		var isBrand bool
		var mentions, answers int
		if err := rows.Scan(&eng, &name, &isBrand, &mentions, &answers); err != nil {
			return p, err
		}
		e := entities[name]
		if e == nil {
			e = &ent{isBrand: isBrand}
			entities[name] = e
		}
		e.mentions += mentions
		e.answers += answers
		allByEngine[eng] += mentions
		all += mentions
		if isBrand {
			brandByEngine[eng] += mentions
			brandAll += mentions
		}
	}
	if err := rows.Err(); err != nil {
		return p, err
	}

	metrics := func(name string, s sums, brandMentions, allMentions int) Metrics {
		m := Metrics{Engine: name, Answers: s.answers, Present: s.present, Mentioned: s.mentioned,
			Visibility: pct(s.mentioned, s.present), ShareOfVoice: pct(brandMentions, allMentions)}
		if s.posN > 0 {
			m.AvgPosition = round2(float64(s.posSum) / float64(s.posN))
		}
		if s.sentN > 0 {
			m.Sentiment = round2(s.sentSum / float64(s.sentN))
		}
		return m
	}
	p.Overall = metrics("all", overall, brandAll, all)
	names := make([]string, 0, len(byEngine))
	for n := range byEngine {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p.ByEngine = append(p.ByEngine, metrics(n, *byEngine[n], brandByEngine[n], allByEngine[n]))
	}
	for name, e := range entities {
		p.Entities = append(p.Entities, Entity{Name: name, IsBrand: e.isBrand, Mentions: e.mentions, Answers: e.answers,
			Visibility: pct(e.answers, overall.present), ShareOfVoice: pct(e.mentions, all)})
	}
	sort.Slice(p.Entities, func(i, j int) bool { return p.Entities[i].Mentions > p.Entities[j].Mentions })
	for v := range versions {
		p.MethodVersions = append(p.MethodVersions, v)
	}
	sort.Strings(p.MethodVersions)
	return p, nil
}

// TopicCell summarises one topic on one engine.
type TopicCell struct {
	Prompts     int      `json:"prompts"`
	Settled     int      `json:"settled"`
	Blindspots  int      `json:"blindspots"`
	MentionRate *float64 `json:"mention_rate"` // % of AI answers mentioning the brand
	State       string   `json:"state"`        // unexplored | screening | confirmed
}

// GridRow is one topic across engines.
type GridRow struct {
	TopicID string               `json:"topic_id"`
	Topic   string               `json:"topic"`
	Demand  *int                 `json:"demand"`
	Cells   map[string]TopicCell `json:"cells"`
}

// LoadGrid returns the topic x engine grid for the Blindspots page.
func LoadGrid(ctx context.Context, tx pgx.Tx, engines []string) ([]GridRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id::text, t.name, t.demand_monthly, c.engine,
		       count(DISTINCT c.prompt_id)::int,
		       count(DISTINCT c.prompt_id) FILTER (WHERE c.phase = 'settled')::int,
		       coalesce(sum(c.mentions), 0)::int, coalesce(sum(c.present), 0)::int,
		       (SELECT count(*) FROM blindspots b JOIN prompts bp ON bp.id = b.prompt_id
		         WHERE bp.topic_id = t.id AND b.engine = c.engine AND b.status = 'open')::int
		FROM topics t
		LEFT JOIN prompts p ON p.topic_id = t.id AND p.status IN ('active', 'tracked')
		LEFT JOIN cells c ON c.prompt_id = p.id
		WHERE t.status = 'active'
		GROUP BY t.id, t.name, t.demand_monthly, c.engine
		ORDER BY t.demand_monthly DESC NULLS LAST, t.name`)
	if err != nil {
		return nil, err
	}
	byTopic := map[string]*GridRow{}
	var order []string
	for rows.Next() {
		var id, name string
		var demand *int
		var eng *string
		var prompts, settled, mentions, present, blind int
		if err := rows.Scan(&id, &name, &demand, &eng, &prompts, &settled, &mentions, &present, &blind); err != nil {
			return nil, err
		}
		r := byTopic[id]
		if r == nil {
			r = &GridRow{TopicID: id, Topic: name, Demand: demand, Cells: map[string]TopicCell{}}
			byTopic[id] = r
			order = append(order, id)
		}
		if eng == nil {
			continue
		}
		state := "screening"
		if prompts > 0 && settled == prompts {
			state = "confirmed"
		}
		r.Cells[*eng] = TopicCell{Prompts: prompts, Settled: settled, Blindspots: blind, MentionRate: pct(mentions, present), State: state}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]GridRow, 0, len(order))
	for _, id := range order {
		r := byTopic[id]
		for _, e := range engines {
			if _, ok := r.Cells[e]; !ok {
				r.Cells[e] = TopicCell{State: "unexplored"}
			}
		}
		out = append(out, *r)
	}
	return out, nil
}

// Blindspot is one row of the Blindspots list.
type Blindspot struct {
	ID               int64     `json:"id"`
	PromptID         string    `json:"prompt_id"`
	Prompt           string    `json:"prompt"`
	TopicID          *string   `json:"topic_id"`
	Topic            *string   `json:"topic"`
	Demand           *int      `json:"demand"`
	Engine           string    `json:"engine"`
	Kind             string    `json:"kind"`
	Confirmed        bool      `json:"confirmed"`
	Competitor       *string   `json:"competitor"`
	EvidenceAnswerID *int64    `json:"evidence_answer_id"`
	FirstSeen        time.Time `json:"first_seen"`
	LastSeen         time.Time `json:"last_seen"`
	Priority         Priority  `json:"priority"`
}

// Priority is demand x severity x certainty, with every component shown.
type Priority struct {
	Score     float64 `json:"score"`
	Demand    float64 `json:"demand"`    // log-scaled monthly searches, 1 when unknown
	Severity  float64 `json:"severity"`  // by kind
	Certainty float64 `json:"certainty"` // 1 confirmed, 0.5 provisional
}

var severity = map[string]float64{"displacement": 1.0, "visibility": 0.9, "sentiment": 0.8, "differentiator": 0.6}

// ScorePriority is exported for tests and for the agent's "what should we fix first".
func ScorePriority(demand *int, kind string, confirmed bool) Priority {
	d := 1.0
	if demand != nil && *demand > 0 {
		d = 1 + math.Log10(float64(*demand))
	}
	c := 0.5
	if confirmed {
		c = 1
	}
	s := severity[kind]
	return Priority{Score: math.Round(d*s*c*100) / 100, Demand: math.Round(d*100) / 100, Severity: s, Certainty: c}
}

// LoadBlindspots returns open blindspots, highest priority first.
func LoadBlindspots(ctx context.Context, tx pgx.Tx, status string) ([]Blindspot, error) {
	rows, err := tx.Query(ctx, `
		SELECT b.id, b.prompt_id::text, p.text, t.id::text, t.name, t.demand_monthly, b.engine, b.kind, b.confirmed,
		       b.winning_competitor, b.evidence_answer_id, b.first_seen, b.last_seen
		FROM blindspots b JOIN prompts p ON p.id = b.prompt_id LEFT JOIN topics t ON t.id = p.topic_id
		WHERE b.status = $1`, status)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Blindspot, error) {
		var b Blindspot
		err := r.Scan(&b.ID, &b.PromptID, &b.Prompt, &b.TopicID, &b.Topic, &b.Demand, &b.Engine, &b.Kind, &b.Confirmed,
			&b.Competitor, &b.EvidenceAnswerID, &b.FirstSeen, &b.LastSeen)
		b.Priority = ScorePriority(b.Demand, b.Kind, b.Confirmed)
		return b, err
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority.Score > out[j].Priority.Score })
	return out, err
}

// Answer is one stored answer with its signals and judged summary.
type Answer struct {
	ID          int64           `json:"id"`
	Engine      string          `json:"engine"`
	Round       int             `json:"round"`
	CollectedAt time.Time       `json:"collected_at"`
	Present     bool            `json:"present"`
	Text        string          `json:"text"`
	Sources     json.RawMessage `json:"sources"`
	FanOut      []string        `json:"fan_out"`
	Mentioned   bool            `json:"mentioned"`
	Position    int             `json:"position"`
	Competitors []string        `json:"competitors"`
	Sentiment   *float64        `json:"sentiment"`
	Role        *string         `json:"role"`
}

// LoadAnswers returns the newest answers for one prompt.
func LoadAnswers(ctx context.Context, tx pgx.Tx, promptID string, limit int) ([]Answer, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id, a.engine, a.round, a.collected_at, a.present, a.text, a.sources, a.fan_out,
		       s.brand_mentioned, s.brand_position, s.competitors_mentioned,
		       (SELECT score::float8 FROM judgments j WHERE j.answer_id = a.id AND j.dimension = 'sentiment' ORDER BY j.created_at DESC LIMIT 1),
		       (SELECT value #>> '{}' FROM judgments j WHERE j.answer_id = a.id AND j.dimension = 'role' ORDER BY j.created_at DESC LIMIT 1)
		FROM answers a JOIN answer_signals s ON s.answer_id = a.id
		WHERE a.prompt_id = $1 ORDER BY a.collected_at DESC LIMIT $2`, promptID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Answer, error) {
		var a Answer
		err := r.Scan(&a.ID, &a.Engine, &a.Round, &a.CollectedAt, &a.Present, &a.Text, &a.Sources, &a.FanOut,
			&a.Mentioned, &a.Position, &a.Competitors, &a.Sentiment, &a.Role)
		return a, err
	})
}

func pct(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	return round2(100 * float64(n) / float64(d))
}

func round2(v float64) *float64 {
	r := math.Round(v*100) / 100
	return &r
}
