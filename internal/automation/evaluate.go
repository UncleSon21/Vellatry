package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/events"
)

// EngineName is how an engine is written for people.
func EngineName(e string) string {
	switch e {
	case "chatgpt":
		return "ChatGPT"
	case "gemini":
		return "Gemini"
	case "ai_overview":
		return "Google AI Overview"
	case "", "all":
		return "all engines"
	}
	return e
}

var connectionNames = map[string]string{
	"google": "Google", "search_console": "Search Console", "ga4": "Google Analytics 4", "asana": "Asana", "slack": "Slack", "email": "Email",
}

// WatchedEvents are the event kinds EvaluateEvent looks at.
var WatchedEvents = []string{domainevents.SiteCrawlCompleted, domainevents.ConnectionBroken, domainevents.BlindspotOpened}

// EvaluateEvent returns the notifications the enabled watchers raise for one event.
func EvaluateEvent(ctx context.Context, tx pgx.Tx, ev events.Stored) ([]Notification, error) {
	switch ev.Kind {
	case domainevents.SiteCrawlCompleted:
		return criticalFindings(ctx, tx, ev)
	case domainevents.ConnectionBroken:
		return connectionBroken(ctx, tx, ev)
	case domainevents.BlindspotOpened:
		return blindspotConfirmed(ctx, tx, ev)
	}
	return nil, nil
}

func forWatchers(ctx context.Context, tx pgx.Tx, kind string, build func(w Watcher) []Notification) ([]Notification, error) {
	ws, err := LoadWatchers(ctx, tx, kind)
	if err != nil || len(ws) == 0 {
		return nil, err
	}
	var out []Notification
	for _, w := range ws {
		for _, n := range build(w) {
			n.WatcherID, n.Kind, n.Delivery, n.DestinationIDs = w.ID, w.Kind, w.Delivery, w.DestinationIDs
			n.DedupeKey = w.Kind + ":" + w.ID + ":" + n.DedupeKey
			out = append(out, n)
		}
	}
	return out, nil
}

func criticalFindings(ctx context.Context, tx pgx.Tx, ev events.Stored) ([]Notification, error) {
	var p domainevents.SiteCrawlCompletedPayload
	if json.Unmarshal(ev.Payload, &p) != nil || len(p.CriticalOpened) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT id, fingerprint, rule, message, coalesce(url, '') FROM audit_findings
		WHERE fingerprint = ANY($1) AND status = 'open' ORDER BY rule, url`, p.CriticalOpened)
	if err != nil {
		return nil, err
	}
	type finding struct {
		id               int64
		fp, rule, msg, u string
	}
	fs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (finding, error) {
		var f finding
		return f, r.Scan(&f.id, &f.fp, &f.rule, &f.msg, &f.u)
	})
	if err != nil || len(fs) == 0 {
		return nil, err
	}
	return forWatchers(ctx, tx, CriticalFinding, func(Watcher) []Notification {
		out := make([]Notification, 0, len(fs))
		for _, f := range fs {
			body := f.msg
			if f.u != "" {
				body += "\nPage: " + f.u
			}
			out = append(out, Notification{
				DedupeKey: f.fp + ":" + ev.SubjectID, Severity: "critical", Title: "New critical site issue",
				Body: body, Link: "/site/findings/" + strconv.FormatInt(f.id, 10),
				Data: map[string]any{"rule": f.rule, "url": f.u, "crawl_id": ev.SubjectID},
			})
		}
		return out
	})
}

func connectionBroken(ctx context.Context, tx pgx.Tx, ev events.Stored) ([]Notification, error) {
	var p domainevents.ConnectionPayload
	if json.Unmarshal(ev.Payload, &p) != nil || p.Kind == "" {
		return nil, nil
	}
	name := connectionNames[p.Kind]
	if name == "" {
		name = p.Kind
	}
	body := "Vellatry can no longer reach " + name + "."
	if p.Detail != "" {
		body = p.Detail
	}
	link := "/settings/connections"
	if p.Kind == "slack" || p.Kind == "email" {
		link = "/automations/destinations"
	}
	return forWatchers(ctx, tx, ConnectionBroken, func(Watcher) []Notification {
		return []Notification{{
			DedupeKey: p.Kind + ":" + ev.OccurredAt.UTC().Format(time.DateOnly), Severity: "warning",
			Title: name + " needs reconnecting", Body: body + " Reconnect it to keep the dashboard and reports complete.",
			Link: link, Data: map[string]string{"connection": p.Kind},
		}}
	})
}

func blindspotConfirmed(ctx context.Context, tx pgx.Tx, ev events.Stored) ([]Notification, error) {
	var p struct {
		PromptID   string `json:"prompt_id"`
		Engine     string `json:"engine"`
		Kind       string `json:"kind"`
		Confirmed  bool   `json:"confirmed"`
		Competitor string `json:"competitor"`
	}
	if json.Unmarshal(ev.Payload, &p) != nil || !p.Confirmed {
		return nil, nil
	}
	var id int64
	var prompt, brandName string
	err := tx.QueryRow(ctx, `
		SELECT b.id, p.text, br.name FROM blindspots b JOIN prompts p ON p.id = b.prompt_id JOIN brands br ON br.id = p.brand_id
		WHERE b.prompt_id = $1 AND b.engine = $2 AND b.kind = $3 AND b.status = 'open'`, p.PromptID, p.Engine, p.Kind).Scan(&id, &prompt, &brandName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	engine := EngineName(p.Engine)
	var body string
	switch {
	case p.Kind == "displacement" && p.Competitor != "":
		body = fmt.Sprintf("%s put %s ahead of %s when asked %q.", engine, p.Competitor, brandName, prompt)
	case p.Kind == "visibility":
		body = fmt.Sprintf("%s left %s out of every confirming answer to %q.", engine, brandName, prompt)
	default:
		body = fmt.Sprintf("%s: a %s blindspot on %q.", engine, p.Kind, prompt)
	}
	return forWatchers(ctx, tx, BlindspotConfirmed, func(Watcher) []Notification {
		return []Notification{{
			DedupeKey: strconv.FormatInt(id, 10) + ":" + ev.OccurredAt.UTC().Format(time.DateOnly), Severity: "warning",
			Title: "Blindspot confirmed on " + engine, Body: body, Link: "/visibility/blindspots/" + strconv.FormatInt(id, 10),
			Data: map[string]any{"blindspot_id": id, "engine": p.Engine, "kind": p.Kind, "prompt": prompt, "competitor": p.Competitor},
		}}
	})
}

// ---- daily watchers ----------------------------------------------------------------

// MinAnswersForDrop is the answers each week needs before a visibility change counts.
const MinAnswersForDrop = 20

// Window is mention counts over a period.
type Window struct {
	Answers   int
	Mentioned int
}

// Rate is the share of answers mentioning the brand, in percent.
func (w Window) Rate() float64 {
	if w.Answers == 0 {
		return 0
	}
	return 100 * float64(w.Mentioned) / float64(w.Answers)
}

// Drop returns how many points visibility fell from prev to cur, and whether both
// weeks had enough answers for that to mean anything.
func Drop(cur, prev Window) (float64, bool) {
	if cur.Answers < MinAnswersForDrop || prev.Answers < MinAnswersForDrop {
		return 0, false
	}
	return math.Round((prev.Rate()-cur.Rate())*10) / 10, true
}

// isoWeek names the week of t, so a weekly condition alerts at most once a week.
func isoWeek(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// EvaluateDaily runs the watchers that compare weeks: visibility drops and competitors
// overtaking. today is the UTC date the evaluation runs for; weeks end yesterday.
func EvaluateDaily(ctx context.Context, tx pgx.Tx, today time.Time) ([]Notification, error) {
	today = today.UTC().Truncate(24 * time.Hour)
	curFrom, prevFrom := today.AddDate(0, 0, -7), today.AddDate(0, 0, -14)
	week := isoWeek(today)

	drops, err := visibilityDrops(ctx, tx, prevFrom, curFrom, today, week)
	if err != nil {
		return nil, err
	}
	over, err := competitorOvertakes(ctx, tx, prevFrom, curFrom, today, week)
	if err != nil {
		return nil, err
	}
	return append(drops, over...), nil
}

func visibilityDrops(ctx context.Context, tx pgx.Tx, prevFrom, curFrom, today time.Time, week string) ([]Notification, error) {
	ws, err := LoadWatchers(ctx, tx, VisibilityDrop)
	if err != nil || len(ws) == 0 {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT engine, day >= $2::date AS cur, sum(answers)::int, sum(mentioned)::int
		FROM visibility_daily WHERE day >= $1::date AND day < $3::date GROUP BY engine, cur`,
		prevFrom.Format(time.DateOnly), curFrom.Format(time.DateOnly), today.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	cur, prev := map[string]Window{}, map[string]Window{}
	for rows.Next() {
		var engine string
		var isCur bool
		var w Window
		if err := rows.Scan(&engine, &isCur, &w.Answers, &w.Mentioned); err != nil {
			return nil, err
		}
		m := prev
		if isCur {
			m = cur
		}
		m[engine] = w
		all := m[""]
		all.Answers, all.Mentioned = all.Answers+w.Answers, all.Mentioned+w.Mentioned
		m[""] = all
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Notification
	for _, w := range ws {
		drop, ok := Drop(cur[w.Params.Engine], prev[w.Params.Engine])
		if !ok || drop < w.Params.Points {
			continue
		}
		engine := EngineName(w.Params.Engine)
		c, p := cur[w.Params.Engine].Rate(), prev[w.Params.Engine].Rate()
		out = append(out, Notification{
			WatcherID: w.ID, Kind: w.Kind, Delivery: w.Delivery, DestinationIDs: w.DestinationIDs,
			DedupeKey: w.Kind + ":" + w.ID + ":" + week, Severity: "warning",
			Title: fmt.Sprintf("AI visibility down %.1f points on %s", drop, engine),
			Body:  fmt.Sprintf("Your brand appeared in %.1f%% of AI answers in the last 7 days, down from %.1f%% the week before.", c, p),
			Link:  "/visibility/performance",
			Data:  map[string]any{"engine": w.Params.Engine, "current": round1(c), "previous": round1(p), "drop": drop},
		})
	}
	return out, nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// PromptAnswer is one answer to a tracked prompt, reduced to what the overtake check needs.
type PromptAnswer struct {
	PromptID    string
	Prompt      string
	Current     bool // in the latest week
	Brand       bool
	Competitors []string
}

// Overtake is a competitor that now beats the brand on one prompt.
type Overtake struct {
	PromptID, Prompt, Competitor string
	Brand, Rival                 [2]Window // [previous, current]
}

// MinAnswersForOvertake is the answers each week needs on a prompt.
const MinAnswersForOvertake = 3

// Overtakes finds competitors mentioned more often than the brand this week on a
// prompt where they were not ahead last week. competitor filters to one ("" = any).
func Overtakes(answers []PromptAnswer, competitor string) []Overtake {
	type key struct{ prompt, comp string }
	type counts struct{ total, brand [2]int }
	prompts := map[string]*counts{}
	text := map[string]string{}
	rival := map[key]*[2]int{}
	for _, a := range answers {
		i := 0
		if a.Current {
			i = 1
		}
		c := prompts[a.PromptID]
		if c == nil {
			c = &counts{}
			prompts[a.PromptID] = c
		}
		text[a.PromptID] = a.Prompt
		c.total[i]++
		if a.Brand {
			c.brand[i]++
		}
		seen := map[string]bool{}
		for _, comp := range a.Competitors {
			if seen[comp] || (competitor != "" && !strings.EqualFold(comp, competitor)) {
				continue
			}
			seen[comp] = true
			k := key{a.PromptID, comp}
			if rival[k] == nil {
				rival[k] = &[2]int{}
			}
			rival[k][i]++
		}
	}
	var out []Overtake
	for k, r := range rival {
		c := prompts[k.prompt]
		if c.total[0] < MinAnswersForOvertake || c.total[1] < MinAnswersForOvertake {
			continue
		}
		// Compare rates, since the weeks may have different answer counts.
		ahead := func(i int) bool { return float64(r[i])/float64(c.total[i]) > float64(c.brand[i])/float64(c.total[i]) }
		if ahead(1) && !ahead(0) {
			out = append(out, Overtake{
				PromptID: k.prompt, Prompt: text[k.prompt], Competitor: k.comp,
				Brand: [2]Window{{c.total[0], c.brand[0]}, {c.total[1], c.brand[1]}},
				Rival: [2]Window{{c.total[0], r[0]}, {c.total[1], r[1]}},
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Prompt != out[j].Prompt {
			return out[i].Prompt < out[j].Prompt
		}
		return out[i].Competitor < out[j].Competitor
	})
	return out
}

func competitorOvertakes(ctx context.Context, tx pgx.Tx, prevFrom, curFrom, today time.Time, week string) ([]Notification, error) {
	ws, err := LoadWatchers(ctx, tx, CompetitorOvertakes)
	if err != nil || len(ws) == 0 {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT a.prompt_id::text, p.text, a.collected_at >= $2, s.brand_mentioned, s.competitors_mentioned
		FROM answers a
		JOIN prompts p ON p.id = a.prompt_id
		JOIN answer_signals s ON s.answer_id = a.id
		WHERE p.status = 'tracked' AND a.present AND a.collected_at >= $1 AND a.collected_at < $3`, prevFrom, curFrom, today)
	if err != nil {
		return nil, err
	}
	answers, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (PromptAnswer, error) {
		var a PromptAnswer
		return a, r.Scan(&a.PromptID, &a.Prompt, &a.Current, &a.Brand, &a.Competitors)
	})
	if err != nil {
		return nil, err
	}
	var brandName string
	if err := tx.QueryRow(ctx, `SELECT name FROM brands ORDER BY created_at LIMIT 1`).Scan(&brandName); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var out []Notification
	for _, w := range ws {
		for _, o := range Overtakes(answers, w.Params.Competitor) {
			out = append(out, Notification{
				WatcherID: w.ID, Kind: w.Kind, Delivery: w.Delivery, DestinationIDs: w.DestinationIDs,
				DedupeKey: w.Kind + ":" + w.ID + ":" + o.PromptID + ":" + strings.ToLower(o.Competitor) + ":" + week,
				Severity:  "warning",
				Title:     o.Competitor + " overtook " + brandName,
				Body: fmt.Sprintf("On %q, %s was mentioned in %d of %d AI answers this week against %d for %s.",
					o.Prompt, o.Competitor, o.Rival[1].Mentioned, o.Rival[1].Answers, o.Brand[1].Mentioned, brandName),
				Link: "/visibility/prompts/" + o.PromptID,
				Data: map[string]any{"prompt_id": o.PromptID, "prompt": o.Prompt, "competitor": o.Competitor,
					"brand": []int{o.Brand[0].Mentioned, o.Brand[1].Mentioned}, "rival": []int{o.Rival[0].Mentioned, o.Rival[1].Mentioned},
					"answers": []int{o.Brand[0].Answers, o.Brand[1].Answers}},
			})
		}
	}
	return out, nil
}
