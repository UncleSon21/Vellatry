package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/brand"
)

// Input is everything an analysis runs with.
type Input struct {
	Question    string
	Slots       Slots
	Brand       brand.Brand
	Competitors []brand.Competitor
	Now         time.Time
}

// Period is the range the analysis covers.
func (in Input) Period() Period { return in.Slots.Period() }

// CompetitorDomains lists every tracked competitor's domains.
func (in Input) CompetitorDomains() []string {
	var out []string
	for _, c := range in.Competitors {
		out = append(out, c.Domains...)
	}
	return out
}

// ErrUnknownAnalysis is returned for a name that is not in the catalogue.
var ErrUnknownAnalysis = errors.New("agent: unknown analysis")

type analysisFunc func(ctx context.Context, tx pgx.Tx, in Input) (Bundle, error)

var registry = map[string]analysisFunc{
	VisibilityChange:  visibilityChange,
	CompetitorGaps:    competitorGaps,
	CompetitorCompare: competitorCompare,
	Sources:           sourcesAnalysis,
	NegativeClaims:    negativeClaims,
	TrafficChange:     trafficChange,
	AIReferrals:       aiReferrals,
	FixOutcomes:       fixOutcomes,
	FixPriority:       fixPriority,
	SinceLastReport:   sinceLastReport,
	TopicOpportunity:  topicOpportunity,
}

// Run performs one analysis. Everything it reads is stored data: no external call, so
// the api can answer a known question while the user waits.
func Run(ctx context.Context, tx pgx.Tx, name string, in Input) (Bundle, error) {
	fn, ok := registry[name]
	if !ok {
		return Bundle{}, fmt.Errorf("%w: %q", ErrUnknownAnalysis, name)
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	if in.Slots.To.IsZero() {
		today := time.Date(in.Now.Year(), in.Now.Month(), in.Now.Day(), 0, 0, 0, 0, time.UTC)
		in.Slots.From, in.Slots.To = today.AddDate(0, 0, -DefaultDays), today.AddDate(0, 0, -1)
	}
	b, err := fn(ctx, tx, in)
	if err != nil {
		return b, err
	}
	b.Analysis, b.Question = name, in.Question
	p := in.Period()
	if b.From == "" {
		b.From, b.To = p.From.Format(time.DateOnly), p.To.Format(time.DateOnly)
	}
	return b, nil
}

// LoadKnown reads the names the router fills slots from.
func LoadKnown(ctx context.Context, tx pgx.Tx) (Known, error) {
	var k Known
	if err := tx.QueryRow(ctx, `SELECT engines FROM org_settings`).Scan(&k.Engines); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return k, err
	}
	rows, err := tx.Query(ctx, `SELECT name FROM competitors ORDER BY name`)
	if err != nil {
		return k, err
	}
	if k.Competitors, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return k, err
	}
	rows, err = tx.Query(ctx, `SELECT id::text, name FROM topics WHERE status = 'active' ORDER BY demand_monthly DESC NULLS LAST LIMIT 200`)
	if err != nil {
		return k, err
	}
	k.Topics, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (TopicRef, error) {
		var t TopicRef
		return t, r.Scan(&t.ID, &t.Name)
	})
	return k, err
}
