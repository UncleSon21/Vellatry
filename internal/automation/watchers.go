// Package automation holds watchers, notifications and the weekly digest: what is worth
// telling the team, decided by code from stored results. It never calls an external
// service (the worker delivers through internal/slack, internal/email and
// internal/asana), so the api role may use it to validate what users configure.
package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Watcher kinds. Each is a condition written in code; users choose which to run and
// set their parameters.
const (
	VisibilityDrop      = "visibility_drop"      // AI visibility fell by at least N points week on week
	CompetitorOvertakes = "competitor_overtakes" // a competitor is now mentioned more than the brand on a tracked prompt
	CriticalFinding     = "critical_finding"     // a crawl opened a critical audit finding
	ConnectionBroken    = "connection_broken"    // Search Console, GA4, Asana or a destination stopped working
	BlindspotConfirmed  = "blindspot_confirmed"  // a blindspot was confirmed with five answers
)

// Kinds lists every watcher kind with a description for the Automations page.
var Kinds = []KindInfo{
	{VisibilityDrop, "AI visibility drops", "Overall (or one engine's) visibility fell by at least the set number of points, last 7 days against the 7 before."},
	{CompetitorOvertakes, "A competitor overtakes you", "On a tracked prompt, a competitor is now mentioned more often than your brand, and was not the week before."},
	{CriticalFinding, "New critical site issue", "A crawl found a new critical problem, such as robots.txt blocking an AI search crawler."},
	{ConnectionBroken, "A connection breaks", "Search Console, GA4, Asana or a Slack destination stopped working and needs someone to reconnect it."},
	{BlindspotConfirmed, "Blindspot confirmed", "An engine left your brand out (or put a competitor first) in all five confirming answers."},
}

// KindInfo describes a watcher kind.
type KindInfo struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Params are a watcher's settings. Only the fields its kind uses are kept.
type Params struct {
	Points     float64 `json:"points,omitempty"`     // visibility_drop: percentage points
	Engine     string  `json:"engine,omitempty"`     // visibility_drop: one engine, or all
	Competitor string  `json:"competitor,omitempty"` // competitor_overtakes: one competitor, or any
}

// Watcher is a stored watcher.
type Watcher struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Params         Params   `json:"params"`
	Delivery       string   `json:"delivery"`
	DestinationIDs []string `json:"destination_ids"`
	Enabled        bool     `json:"enabled"`
	Source         string   `json:"source"`
}

// ErrInvalid is wrapped by every validation error; its message is safe to show.
var ErrInvalid = errors.New("invalid")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Message strips the ErrInvalid prefix for display.
func Message(err error) string {
	return strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")
}

// Normalise validates a watcher's kind, delivery and params and drops params its kind
// does not use.
func Normalise(w Watcher, engines []string) (Watcher, error) {
	known := false
	for _, k := range Kinds {
		known = known || k.Kind == w.Kind
	}
	if !known {
		return w, invalid("unknown watcher %q", w.Kind)
	}
	if w.Delivery == "" {
		w.Delivery = "immediate"
	}
	if w.Delivery != "immediate" && w.Delivery != "digest" {
		return w, invalid("delivery must be immediate or digest")
	}
	p := Params{}
	switch w.Kind {
	case VisibilityDrop:
		p.Points = w.Params.Points
		if p.Points == 0 {
			p.Points = 10
		}
		if p.Points < 1 || p.Points > 100 {
			return w, invalid("points must be between 1 and 100")
		}
		if e := w.Params.Engine; e != "" {
			ok := false
			for _, x := range engines {
				ok = ok || x == e
			}
			if !ok {
				return w, invalid("engine %q is not one of your engines", e)
			}
			p.Engine = e
		}
	case CompetitorOvertakes:
		p.Competitor = strings.TrimSpace(w.Params.Competitor)
	}
	w.Params = p
	if w.DestinationIDs == nil {
		w.DestinationIDs = []string{}
	}
	return w, nil
}

// Defaults are the watchers every new organisation starts with.
func Defaults() []Watcher {
	return []Watcher{
		{Kind: CriticalFinding, Delivery: "immediate"},
		{Kind: ConnectionBroken, Delivery: "immediate"},
		{Kind: VisibilityDrop, Delivery: "immediate", Params: Params{Points: 10}},
		{Kind: BlindspotConfirmed, Delivery: "digest"},
		{Kind: CompetitorOvertakes, Delivery: "digest"},
	}
}

// InsertDefaults adds the default watchers for a new organisation.
func InsertDefaults(ctx context.Context, tx pgx.Tx, org string) error {
	for _, w := range Defaults() {
		params, _ := json.Marshal(w.Params)
		if _, err := tx.Exec(ctx, `INSERT INTO watchers (org_id, kind, params, delivery, source) VALUES ($1, $2, $3, $4, 'default')`,
			org, w.Kind, params, w.Delivery); err != nil {
			return err
		}
	}
	return nil
}

const watcherCols = `id::text, kind, params, delivery, destination_ids::text[], enabled, source`

func scanWatcher(r pgx.Row) (Watcher, error) {
	var w Watcher
	var params []byte
	if err := r.Scan(&w.ID, &w.Kind, &params, &w.Delivery, &w.DestinationIDs, &w.Enabled, &w.Source); err != nil {
		return w, err
	}
	_ = json.Unmarshal(params, &w.Params)
	return w, nil
}

// LoadWatchers returns the organisation's watchers, or only the enabled ones of kind.
func LoadWatchers(ctx context.Context, tx pgx.Tx, kind string) ([]Watcher, error) {
	q := `SELECT ` + watcherCols + ` FROM watchers ORDER BY created_at`
	args := []any{}
	if kind != "" {
		q = `SELECT ` + watcherCols + ` FROM watchers WHERE enabled AND kind = $1 ORDER BY created_at`
		args = append(args, kind)
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Watcher, error) { return scanWatcher(r) })
}
