package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/visibility/read"
)

// visibilityChange answers "why did our AI visibility change?": the overall move, then
// the engine that moved it most, then what the competitors did.
func visibilityChange(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error) {
	p := in.Period()
	cur, err := read.LoadPerformance(ctx, tx, p.From, p.To)
	if err != nil {
		return Bundle{}, err
	}
	var b Bundle
	if cur.Overall.Answers == 0 {
		b.Empty = "no AI answers were collected between " + p.Label() + "."
		return b, nil
	}
	prev, err := read.LoadPerformance(ctx, tx, p.Previous().From, p.Previous().To)
	if err != nil {
		return b, err
	}
	now, before := value(cur.Overall.Visibility), value(prev.Overall.Visibility)
	delta := now - before
	b.Add(Fact{ID: "visibility", Label: "AI visibility", Value: Pct(now), Raw: now, Unit: "%",
		Note: fmt.Sprintf("%s of %s AI answers mentioned %s", Pct(now), Num(int64(cur.Overall.Answers)), in.Brand.Name), Trend: Trend(delta)})
	if prev.Overall.Answers > 0 {
		b.Add(Fact{ID: "previous", Label: "Visibility before", Value: Pct(before), Raw: before, Unit: "%",
			Note: "the " + fmt.Sprint(p.Days()) + " days before"})
		b.Add(Fact{ID: "change", Label: "Change", Value: Points(delta), Raw: delta, Unit: "points", Trend: Trend(delta)})
	}
	b.Add(Fact{ID: "answers", Label: "Answers collected", Value: Num(int64(cur.Overall.Answers)), Raw: float64(cur.Overall.Answers)})
	if cur.Overall.ShareOfVoice != nil {
		sov := *cur.Overall.ShareOfVoice
		b.Add(Fact{ID: "share_of_voice", Label: "Share of voice", Value: Pct(sov), Raw: sov, Unit: "%",
			Note: "of all mentions of you and your competitors"})
	}

	// Which engine moved the number most.
	beforeEngine := map[string]float64{}
	for _, m := range prev.ByEngine {
		if m.Answers > 0 {
			beforeEngine[m.Engine] = value(m.Visibility)
		}
	}
	t := Table{ID: "by_engine", Title: "By engine", Head: []string{"Engine", "Visibility", "Change", "Answers"}}
	type move struct {
		engine string
		delta  float64
	}
	var moves []move
	for _, m := range cur.ByEngine {
		if m.Answers == 0 {
			continue
		}
		v := value(m.Visibility)
		row := []string{EngineName(m.Engine), Pct(v), "", Num(int64(m.Answers))}
		if bv, ok := beforeEngine[m.Engine]; ok {
			row[2] = Points(v - bv)
			moves = append(moves, move{m.Engine, v - bv})
		}
		t.Rows = append(t.Rows, row)
	}
	b.Tables = append(b.Tables, t)
	sort.Slice(moves, func(i, j int) bool { return abs(moves[i].delta) > abs(moves[j].delta) })

	// The competitors, so a drop that is really a competitor's rise reads as one.
	comp := Table{ID: "competitors", Title: "You and your competitors", Head: []string{"Brand", "Visibility", "Share of voice"}}
	for _, e := range cur.Entities {
		name := e.Name
		if e.IsBrand {
			name += " (you)"
		}
		comp.Rows = append(comp.Rows, []string{name, Pct(value(e.Visibility)), Pct(value(e.ShareOfVoice))})
		if len(comp.Rows) == 6 {
			break
		}
	}
	if len(comp.Rows) > 1 {
		b.Tables = append(b.Tables, comp)
	}
	b.Series = append(b.Series, dailySeries("visibility", "AI visibility", "%", cur.Series))

	var opened int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM blindspots WHERE confirmed AND first_seen >= $1 AND first_seen < $2`,
		p.From, p.To.AddDate(0, 0, 1)).Scan(&opened); err != nil {
		return b, err
	}
	if opened > 0 {
		b.Add(Fact{ID: "blindspots_new", Label: "Blindspots confirmed", Value: Num(int64(opened)), Raw: float64(opened),
			Note: "questions where an engine consistently left you out in this period"})
	}

	switch {
	case prev.Overall.Answers == 0:
		b.Headline = fmt.Sprintf("%s is mentioned in %s of AI answers; there is nothing to compare with yet.", in.Brand.Name, Pct(now))
	case len(moves) > 0 && abs(moves[0].delta) >= 1 && sameDirection(moves[0].delta, delta):
		b.Headline = fmt.Sprintf("AI visibility is %s, %s, mostly on %s (%s).", Pct(now), Points(delta), EngineName(moves[0].engine), Points(moves[0].delta))
	default:
		b.Headline = fmt.Sprintf("AI visibility is %s, %s.", Pct(now), Points(delta))
	}
	b.Links = append(b.Links, Link{Label: "Performance", Href: "/visibility/performance"})
	if delta < -5 {
		b.Actions = append(b.Actions, Action{Kind: ActionWatcher, Label: "Tell me if it drops again",
			Confirm: "Create a watcher that alerts you when AI visibility falls 10 points week on week.",
			Params:  map[string]string{"kind": "visibility_drop", "points": "10"}})
	}
	return b, nil
}

// competitorGaps answers "where are competitors beating us?".
func competitorGaps(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error) {
	spots, err := read.LoadBlindspots(ctx, tx, "open")
	if err != nil {
		return Bundle{}, err
	}
	var b Bundle
	byCompetitor := map[string]int{}
	t := Table{ID: "gaps", Title: "Where a competitor is recommended instead", Head: []string{"Engine", "Question", "Competitor", "Topic"}}
	for _, s := range spots {
		if s.Kind != "displacement" || s.Competitor == nil {
			continue
		}
		if in.Slots.Competitor != "" && !strings.EqualFold(*s.Competitor, in.Slots.Competitor) {
			continue
		}
		if in.Slots.Engine != "" && s.Engine != in.Slots.Engine {
			continue
		}
		byCompetitor[*s.Competitor]++
		if len(t.Rows) < 10 {
			topic := "-"
			if s.Topic != nil {
				topic = *s.Topic
			}
			t.Rows = append(t.Rows, []string{EngineName(s.Engine), s.Prompt, *s.Competitor, topic})
		}
	}
	if len(byCompetitor) == 0 {
		b.Empty = "no competitor is being recommended ahead of you on the questions Vellatry has confirmed so far."
		return b, nil
	}
	type pair struct {
		name string
		n    int
	}
	var ranked []pair
	total := 0
	for name, n := range byCompetitor {
		ranked = append(ranked, pair{name, n})
		total += n
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].n != ranked[j].n {
			return ranked[i].n > ranked[j].n
		}
		return ranked[i].name < ranked[j].name
	})
	b.Add(Fact{ID: "gaps", Label: "Questions a competitor wins", Value: Num(int64(total)), Raw: float64(total)})
	b.Add(Fact{ID: "worst", Label: "Most often", Value: ranked[0].name, Note: fmt.Sprintf("named ahead of you on %s questions", Num(int64(ranked[0].n)))})
	b.Tables = append(b.Tables, t)
	summary := Table{ID: "by_competitor", Title: "By competitor", Head: []string{"Competitor", "Questions"}}
	for _, r := range ranked {
		summary.Rows = append(summary.Rows, []string{r.name, Num(int64(r.n))})
	}
	b.Tables = append(b.Tables, summary)
	b.Headline = fmt.Sprintf("%s is recommended ahead of you on %s of the %s questions where a competitor wins.",
		ranked[0].name, Num(int64(ranked[0].n)), Num(int64(total)))
	b.Links = append(b.Links, Link{Label: "Blindspots", Href: "/visibility/blindspots"})
	b.Actions = append(b.Actions, Action{Kind: ActionWatcher, Label: "Watch for competitors overtaking",
		Confirm: "Create a watcher that alerts you when a competitor overtakes you on a tracked question.",
		Params:  map[string]string{"kind": "competitor_overtakes"}})
	return b, nil
}

// competitorCompare answers "how do we compare with X?".
func competitorCompare(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error) {
	p := in.Period()
	cur, err := read.LoadPerformance(ctx, tx, p.From, p.To)
	if err != nil {
		return Bundle{}, err
	}
	var b Bundle
	if cur.Overall.Answers == 0 {
		b.Empty = "no AI answers were collected between " + p.Label() + "."
		return b, nil
	}
	want := in.Slots.Competitor
	var us, them read.Entity
	var others []read.Entity
	for _, e := range cur.Entities {
		switch {
		case e.IsBrand:
			us = e
		case want != "" && strings.EqualFold(e.Name, want):
			them = e
		default:
			others = append(others, e)
		}
	}
	if them.Name == "" {
		if len(others) == 0 {
			b.Empty = "no competitor was mentioned in the AI answers collected between " + p.Label() + "."
			return b, nil
		}
		them = others[0] // the strongest competitor, when the question named none
	}
	b.Add(Fact{ID: "us", Label: in.Brand.Name + " visibility", Value: Pct(value(us.Visibility)), Raw: value(us.Visibility), Unit: "%"})
	b.Add(Fact{ID: "them", Label: them.Name + " visibility", Value: Pct(value(them.Visibility)), Raw: value(them.Visibility), Unit: "%"})
	gap := value(us.Visibility) - value(them.Visibility)
	b.Add(Fact{ID: "gap", Label: "Difference", Value: Points(gap), Raw: gap, Unit: "points", Trend: Trend(gap)})
	b.Add(Fact{ID: "us_sov", Label: in.Brand.Name + " share of voice", Value: Pct(value(us.ShareOfVoice)), Raw: value(us.ShareOfVoice), Unit: "%"})
	b.Add(Fact{ID: "them_sov", Label: them.Name + " share of voice", Value: Pct(value(them.ShareOfVoice)), Raw: value(them.ShareOfVoice), Unit: "%"})

	t := Table{ID: "side_by_side", Title: "Side by side", Head: []string{"Brand", "Visibility", "Share of voice", "Answers mentioning"}}
	for _, e := range []read.Entity{us, them} {
		name := e.Name
		if e.IsBrand {
			name += " (you)"
		}
		t.Rows = append(t.Rows, []string{name, Pct(value(e.Visibility)), Pct(value(e.ShareOfVoice)), Num(int64(e.Mentions))})
	}
	b.Tables = append(b.Tables, t)

	// Where they win: the confirmed questions this competitor takes.
	rows, err := tx.Query(ctx, `
		SELECT b.engine, p.text FROM blindspots b JOIN prompts p ON p.id = b.prompt_id
		WHERE b.status = 'open' AND b.kind = 'displacement' AND lower(coalesce(b.winning_competitor, '')) = lower($1)
		ORDER BY b.last_seen DESC LIMIT 8`, them.Name)
	if err != nil {
		return b, err
	}
	wins := Table{ID: "they_win", Title: "Questions " + them.Name + " wins", Head: []string{"Engine", "Question"}}
	for rows.Next() {
		var engine, prompt string
		if err := rows.Scan(&engine, &prompt); err != nil {
			return b, err
		}
		wins.Rows = append(wins.Rows, []string{EngineName(engine), prompt})
	}
	if err := rows.Err(); err != nil {
		return b, err
	}
	if len(wins.Rows) > 0 {
		b.Tables = append(b.Tables, wins)
	}
	switch {
	case gap > 0:
		b.Headline = fmt.Sprintf("%s is ahead of %s: %s against %s of AI answers.", in.Brand.Name, them.Name, Pct(value(us.Visibility)), Pct(value(them.Visibility)))
	case gap < 0:
		b.Headline = fmt.Sprintf("%s is ahead of you: %s against %s of AI answers.", them.Name, Pct(value(them.Visibility)), Pct(value(us.Visibility)))
	default:
		b.Headline = fmt.Sprintf("%s and %s are level at %s of AI answers.", in.Brand.Name, them.Name, Pct(value(us.Visibility)))
	}
	b.Links = append(b.Links, Link{Label: "Performance", Href: "/visibility/performance"})
	return b, nil
}

// sourcesAnalysis answers "which sources do AI engines trust?", including the ones that
// cite competitors and not us: the clearest list of where to get written about.
func sourcesAnalysis(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error) {
	p := in.Period()
	srcs, err := read.LoadSources(ctx, tx, p.From, p.To, in.Brand.Domain, in.CompetitorDomains(), 12)
	if err != nil {
		return Bundle{}, err
	}
	var b Bundle
	if len(srcs) == 0 {
		b.Empty = "the AI answers collected between " + p.Label() + " cited no sources."
		return b, nil
	}
	t := Table{ID: "sources", Title: "Most cited sources", Head: []string{"Website", "Type", "Citations"}}
	var ours int
	total := 0
	for _, s := range srcs {
		t.Rows = append(t.Rows, []string{s.Domain, sourceLabel(string(s.Type)), Num(int64(s.Citations))})
		total += s.Citations
		if s.Type == read.Owned {
			ours += s.Citations
		}
	}
	b.Tables = append(b.Tables, t)
	b.Add(Fact{ID: "citations", Label: "Citations counted", Value: Num(int64(total)), Raw: float64(total)})
	b.Add(Fact{ID: "ours", Label: "Citations of your own site", Value: Num(int64(ours)), Raw: float64(ours)})

	rows, err := tx.Query(ctx, `
		SELECT lower(s->>'domain') AS domain, count(*)::int
		FROM answers a
		JOIN answer_signals g ON g.answer_id = a.id, LATERAL jsonb_array_elements(a.sources) s
		WHERE a.collected_at >= $1 AND a.collected_at < $2 AND NOT g.brand_mentioned
		  AND cardinality(g.competitors_mentioned) > 0 AND coalesce(s->>'domain', '') <> ''
		GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 10`, p.From, p.To.AddDate(0, 0, 1))
	if err != nil {
		return b, err
	}
	miss := Table{ID: "cite_competitors", Title: "Cited where a competitor is named and you are not",
		Head: []string{"Website", "Answers"}, Caption: "Being written about here is the most direct way in."}
	own := strings.ToLower(strings.TrimPrefix(in.Brand.Domain, "www."))
	for rows.Next() {
		var domain string
		var n int
		if err := rows.Scan(&domain, &n); err != nil {
			return b, err
		}
		if domain == own || strings.HasSuffix(domain, "."+own) {
			continue
		}
		miss.Rows = append(miss.Rows, []string{domain, Num(int64(n))})
	}
	if err := rows.Err(); err != nil {
		return b, err
	}
	if len(miss.Rows) > 0 {
		b.Tables = append(b.Tables, miss)
		b.Add(Fact{ID: "missing_from", Label: "Top source that cites competitors, not you", Value: miss.Rows[0][0],
			Note: "cited in " + miss.Rows[0][1] + " answers that named a competitor and not you"})
		b.Headline = fmt.Sprintf("The engines lean on %s most; %s is cited where competitors are named and you are not.", srcs[0].Domain, miss.Rows[0][0])
	} else {
		b.Headline = fmt.Sprintf("The engines lean on %s most, with %s citations counted in this period.", srcs[0].Domain, Num(int64(total)))
	}
	b.Links = append(b.Links, Link{Label: "Sources", Href: "/visibility/sources"})
	return b, nil
}

func sourceLabel(t string) string {
	switch t {
	case "owned":
		return "Your site"
	case "competitor":
		return "Competitor"
	case "ugc":
		return "Forum or social"
	case "review":
		return "Reviews"
	case "reference":
		return "Reference"
	}
	return "Editorial"
}

// negativeClaims answers "what does AI say about us that is wrong or negative?" from
// judged answers, quoting the sentence the judge kept.
func negativeClaims(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error) {
	p := in.Period()
	var b Bundle
	rows, err := tx.Query(ctx, `
		SELECT a.engine, p.text, coalesce(j.evidence, ''), a.collected_at
		FROM judgments j JOIN answers a ON a.id = j.answer_id JOIN prompts p ON p.id = a.prompt_id
		WHERE j.dimension = 'framing_negative' AND j.value = 'true'::jsonb
		  AND a.collected_at >= $1 AND a.collected_at < $2
		ORDER BY a.collected_at DESC LIMIT 10`, p.From, p.To.AddDate(0, 0, 1))
	if err != nil {
		return b, err
	}
	t := Table{ID: "negative", Title: "Where an engine framed you negatively", Head: []string{"Engine", "Question", "What it said"}}
	for rows.Next() {
		var engine, prompt, evidence string
		var at time.Time
		if err := rows.Scan(&engine, &prompt, &evidence, &at); err != nil {
			return b, err
		}
		if evidence == "" {
			evidence = "(the judge kept no sentence)"
		}
		t.Rows = append(t.Rows, []string{EngineName(engine), prompt, evidence})
	}
	if err := rows.Err(); err != nil {
		return b, err
	}
	var judged int
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT j.answer_id) FROM judgments j JOIN answers a ON a.id = j.answer_id
		WHERE a.collected_at >= $1 AND a.collected_at < $2`, p.From, p.To.AddDate(0, 0, 1)).Scan(&judged); err != nil {
		return b, err
	}
	if judged == 0 {
		b.Empty = "no answers from this period have been judged, so there is nothing to report on tone."
		return b, nil
	}
	b.Add(Fact{ID: "judged", Label: "Answers judged", Value: Num(int64(judged)), Raw: float64(judged)})
	b.Add(Fact{ID: "negative", Label: "Answers framing you negatively", Value: Num(int64(len(t.Rows))), Raw: float64(len(t.Rows))})
	if len(t.Rows) == 0 {
		b.Headline = fmt.Sprintf("Nothing negative: none of the %s judged answers framed %s badly.", Num(int64(judged)), in.Brand.Name)
		return b, nil
	}
	b.Tables = append(b.Tables, t)
	b.Headline = fmt.Sprintf("%s of %s judged answers framed %s negatively; the sentences are below.",
		Num(int64(len(t.Rows))), Num(int64(judged)), in.Brand.Name)
	b.Links = append(b.Links, Link{Label: "Answers", Href: "/visibility/answers"})
	return b, nil
}

func dailySeries(id, title, unit string, points []read.Point) Series {
	type acc struct{ weighted, n float64 }
	days := map[string]*acc{}
	var order []string
	for _, pt := range points {
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
	s := Series{ID: id, Title: title, Unit: unit}
	for _, d := range order {
		if a := days[d]; a.n > 0 {
			s.Points = append(s.Points, Point{Day: d, Value: round1(a.weighted / a.n)})
		}
	}
	return s
}

func value(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func sameDirection(a, b float64) bool { return (a >= 0) == (b >= 0) }
