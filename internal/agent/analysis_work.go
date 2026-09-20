package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/keywords"
	"github.com/UncleSon21/vellatry/internal/visibility/read"
)

// fixPriority answers "what should we fix first?" by putting every kind of work on one
// list: site problems that stop engines reading the site, blindspots worth closing, and
// fixes already written and waiting for someone.
func fixPriority(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error) {
	var b Bundle
	type item struct {
		what, why, where string
		rank             float64
	}
	var items []item

	rows, err := tx.Query(ctx, `
		SELECT f.title, coalesce(f.severity, 'info'), coalesce(f.page_url, ''), f.status
		FROM fixes f WHERE f.status IN ('proposed', 'sent') ORDER BY f.created_at DESC LIMIT 50`)
	if err != nil {
		return b, err
	}
	for rows.Next() {
		var title, severity, page, status string
		if err := rows.Scan(&title, &severity, &page, &status); err != nil {
			return b, err
		}
		rank := map[string]float64{"critical": 100, "warning": 40}[severity] + 10
		why := "a written fix is ready" + map[bool]string{true: ", already sent to someone", false: ""}[status == "sent"]
		items = append(items, item{title, why, page, rank})
	}
	if err := rows.Err(); err != nil {
		return b, err
	}

	spots, err := read.LoadBlindspots(ctx, tx, "open")
	if err != nil {
		return b, err
	}
	for _, s := range spots {
		if !s.Confirmed {
			continue
		}
		what := fmt.Sprintf("Answer %q for %s", s.Prompt, EngineName(s.Engine))
		why := "the engine leaves you out"
		if s.Kind == "displacement" && s.Competitor != nil {
			why = "the engine recommends " + *s.Competitor + " instead"
		}
		items = append(items, item{what, why, "/visibility/blindspots", 20 + s.Priority.Score*10})
		if len(items) > 80 {
			break
		}
	}

	var critical, warning int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE severity = 'critical'), count(*) FILTER (WHERE severity = 'warning')
		FROM audit_findings WHERE status = 'open'`).Scan(&critical, &warning); err != nil {
		return b, err
	}
	if len(items) == 0 {
		b.Empty = "there is nothing waiting: no fixes proposed, no confirmed blindspots and no open site issues."
		return b, nil
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].rank > items[j].rank })

	t := Table{ID: "first", Title: "Start here", Head: []string{"Work", "Why", "Where"}}
	for _, it := range items {
		where := it.where
		if where == "" {
			where = "-"
		}
		t.Rows = append(t.Rows, []string{it.what, it.why, where})
		if len(t.Rows) == 5 {
			break
		}
	}
	b.Tables = append(b.Tables, t)
	b.Add(Fact{ID: "waiting", Label: "Pieces of work waiting", Value: Num(int64(len(items))), Raw: float64(len(items))})
	b.Add(Fact{ID: "critical", Label: "Critical site issues", Value: Num(int64(critical)), Raw: float64(critical)})
	b.Add(Fact{ID: "warnings", Label: "Site warnings", Value: Num(int64(warning)), Raw: float64(warning)})
	b.Add(Fact{ID: "top", Label: "Start with", Value: items[0].what, Note: items[0].why})
	b.Headline = fmt.Sprintf("Start with: %s, because %s.", items[0].what, items[0].why)
	b.Links = append(b.Links, Link{Label: "Fixes", Href: "/fixes"}, Link{Label: "Blindspots", Href: "/visibility/blindspots"})
	return b, nil
}

// sinceLastReport answers "what changed since the last report?" against the period the
// last published report covered.
func sinceLastReport(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error) {
	var b Bundle
	var since time.Time
	var title string
	err := tx.QueryRow(ctx, `SELECT period_end, title FROM reports WHERE status = 'published' ORDER BY period_end DESC LIMIT 1`).Scan(&since, &title)
	if errors.Is(err, pgx.ErrNoRows) {
		since = in.Period().From
		title = ""
	} else if err != nil {
		return b, err
	}
	until := in.Now.UTC()
	from := since.AddDate(0, 0, 1)
	if title != "" {
		b.Add(Fact{ID: "last_report", Label: "Last report", Value: title, Note: "covered up to " + since.Format("2 Jan 2006")})
	}
	b.From, b.To = from.Format(time.DateOnly), until.Format(time.DateOnly)

	var opened, resolved int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE confirmed AND first_seen >= $1),
		       count(*) FILTER (WHERE status = 'resolved' AND last_seen >= $1) FROM blindspots`, from).Scan(&opened, &resolved); err != nil {
		return b, err
	}
	var fixesLive, findings int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM fixes WHERE live_at >= $1),
		(SELECT count(*) FROM audit_findings WHERE status = 'open' AND severity = 'critical' AND first_seen >= $1)`, from).
		Scan(&fixesLive, &findings); err != nil {
		return b, err
	}
	b.Add(Fact{ID: "blindspots_new", Label: "Blindspots confirmed since", Value: Num(int64(opened)), Raw: float64(opened)})
	b.Add(Fact{ID: "blindspots_closed", Label: "Blindspots closed since", Value: Num(int64(resolved)), Raw: float64(resolved)})
	b.Add(Fact{ID: "fixes_live", Label: "Fixes confirmed live", Value: Num(int64(fixesLive)), Raw: float64(fixesLive)})
	b.Add(Fact{ID: "critical_new", Label: "New critical site issues", Value: Num(int64(findings)), Raw: float64(findings)})

	cur, err := read.LoadPerformance(ctx, tx, from, until)
	if err != nil {
		return b, err
	}
	span := int(until.Sub(from).Hours() / 24)
	if span < 1 {
		span = 1
	}
	prev, err := read.LoadPerformance(ctx, tx, from.AddDate(0, 0, -span), from.AddDate(0, 0, -1))
	if err != nil {
		return b, err
	}
	if cur.Overall.Answers > 0 {
		now, before := value(cur.Overall.Visibility), value(prev.Overall.Visibility)
		b.Add(Fact{ID: "visibility", Label: "AI visibility since", Value: Pct(now), Raw: now, Unit: "%", Trend: Trend(now - before)})
		if prev.Overall.Answers > 0 {
			b.Add(Fact{ID: "visibility_change", Label: "Change", Value: Points(now - before), Raw: now - before, Unit: "points"})
		}
	}

	rows, err := tx.Query(ctx, `SELECT kind, count(*)::int FROM events WHERE occurred_at >= $1
		AND kind IN ('fix.live', 'visibility.blindspot.opened', 'visibility.blindspot.resolved', 'connection.broken', 'report.published', 'keywords.run_completed')
		GROUP BY kind ORDER BY 2 DESC`, from)
	if err != nil {
		return b, err
	}
	t := Table{ID: "events", Title: "What happened", Head: []string{"Event", "Times"}}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return b, err
		}
		t.Rows = append(t.Rows, []string{eventLabel(kind), Num(int64(n))})
	}
	if err := rows.Err(); err != nil {
		return b, err
	}
	if len(t.Rows) > 0 {
		b.Tables = append(b.Tables, t)
	}
	switch {
	case opened == 0 && resolved == 0 && fixesLive == 0:
		b.Headline = "Nothing much has changed since " + since.Format("2 Jan 2006") + "."
	default:
		b.Headline = fmt.Sprintf("Since %s: %s blindspots confirmed, %s closed and %s fixes went live.",
			since.Format("2 Jan 2006"), Num(int64(opened)), Num(int64(resolved)), Num(int64(fixesLive)))
	}
	b.Links = append(b.Links, Link{Label: "Reports", Href: "/reports"})
	return b, nil
}

func eventLabel(kind string) string {
	switch kind {
	case "fix.live":
		return "Fixes confirmed live"
	case "visibility.blindspot.opened":
		return "Blindspots opened"
	case "visibility.blindspot.resolved":
		return "Blindspots closed"
	case "connection.broken":
		return "Connections broke"
	case "report.published":
		return "Reports published"
	case "keywords.run_completed":
		return "Keyword research finished"
	}
	return kind
}

// topicOpportunity answers "which topics are worth the work?" from keyword research.
func topicOpportunity(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error) {
	var b Bundle
	rows, err := tx.Query(ctx, `
		SELECT name, status, coalesce(demand_monthly, 0), coalesce(page_url, ''), opportunity, issues
		FROM topics WHERE opportunity <> '{}'::jsonb
		ORDER BY (opportunity->>'score')::float8 DESC NULLS LAST LIMIT 10`)
	if err != nil {
		return b, err
	}
	t := Table{ID: "topics", Title: "Topics by opportunity", Head: []string{"Topic", "Searches a month", "Clicks to win", "Page", "Status"}}
	var best string
	var bestScore float64
	for rows.Next() {
		var name, status, page string
		var demand int
		var oppRaw, issuesRaw []byte
		if err := rows.Scan(&name, &status, &demand, &page, &oppRaw, &issuesRaw); err != nil {
			return b, err
		}
		var opp keywords.Opportunity
		_ = json.Unmarshal(oppRaw, &opp)
		var issues []keywords.Issue
		_ = json.Unmarshal(issuesRaw, &issues)
		where := page
		if where == "" {
			where = "no page yet"
		}
		state := status
		for _, i := range issues {
			if i.Kind == keywords.IssueCannibalised {
				state += ", two pages compete"
			}
		}
		t.Rows = append(t.Rows, []string{name, Num(int64(demand)), fmt.Sprintf("%.0f", opp.Gap), where, state})
		if opp.Score > bestScore {
			best, bestScore = name, opp.Score
		}
	}
	if err := rows.Err(); err != nil {
		return b, err
	}
	if len(t.Rows) == 0 {
		b.Empty = "keyword research has not run yet, so no topics have been scored."
		return b, nil
	}
	b.Tables = append(b.Tables, t)
	b.Add(Fact{ID: "topics", Label: "Topics scored", Value: Num(int64(len(t.Rows))), Raw: float64(len(t.Rows))})
	b.Add(Fact{ID: "best", Label: "Biggest opportunity", Value: best, Note: fmt.Sprintf("about %.0f clicks a month to win", bestScore)})
	b.Headline = fmt.Sprintf("%s is the biggest opportunity: about %.0f clicks a month, adjusted for how hard it is.", best, bestScore)
	b.Links = append(b.Links, Link{Label: "Topics", Href: "/topics"})
	return b, nil
}
