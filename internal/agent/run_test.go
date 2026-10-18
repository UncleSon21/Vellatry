package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// Every analysis in the catalogue, run against a real tenant's data. They are what the
// agent answers with, and a model may only put their numbers into sentences, so a bug
// here is a wrong number in front of a customer with nothing downstream to catch it.
//
// The invariants are the ones that hold for all eleven: an analysis either answers or
// says why it cannot; it names itself; and every figure in the headline it chose is one
// the evidence holds, because narration that repeats the headline is checked against
// exactly that evidence and would otherwise be thrown away.
func TestEveryAnalysisAnswersOrSaysWhyNot(t *testing.T) {
	pool := testdb.Pool(t)

	org, brandID := testdb.NewOrg(t, pool, "analyses")
	seedForAnalyses(t, pool, org, brandID)

	var b brand.Brand
	var comps []brand.Competitor
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		b, comps, err = brand.Load(ctx, tx)
		return err
	})

	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	for name := range registry {
		t.Run(name, func(t *testing.T) {
			var got Bundle
			tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				got, err = Run(ctx, tx, name, Input{
					Question: "what happened?", Brand: b, Competitors: comps, Now: now,
					Slots: Slots{Competitor: "Ecosa"},
				})
				return err
			})
			if got.Analysis != name {
				t.Errorf("the bundle calls itself %q", got.Analysis)
			}
			t.Logf("headline: %s", strings.TrimSpace(got.Headline+got.Empty))
			if got.Empty != "" {
				// An analysis with nothing to work from says so in a sentence, rather
				// than answering with zeroes.
				if len(got.Facts) > 0 {
					t.Errorf("an empty analysis still carried %d facts", len(got.Facts))
				}
				if !strings.HasSuffix(got.Empty, ".") || len(got.Empty) < 15 {
					t.Errorf("the reason there is nothing to say = %q", got.Empty)
				}
				return
			}
			if got.Headline == "" {
				t.Error("an analysis with facts chose no headline")
			}
			// The headline is written by code and read back by the figure check, so a
			// number in it that the evidence does not hold would make the whole
			// narration unverifiable.
			if bad := Unverified(got.Headline, Figures(got)); len(bad) > 0 {
				t.Errorf("the headline cites %v, which the evidence does not hold: %q", bad, got.Headline)
			}
			// The same for what the code itself writes when no model is involved.
			if bad := Unverified(Narrate(got), Figures(got)); len(bad) > 0 {
				t.Errorf("the written answer cites %v, which the evidence does not hold", bad)
			}
			for _, f := range got.Facts {
				if f.Label == "" || f.Value == "" {
					t.Errorf("a fact with nothing to show: %+v", f)
				}
			}
			for _, tbl := range got.Tables {
				for _, row := range tbl.Rows {
					if len(row) != len(tbl.Head) {
						t.Errorf("table %q has a row of %d against %d headings", tbl.ID, len(row), len(tbl.Head))
					}
				}
			}
		})
	}
}

// An unknown analysis is refused by name rather than answered with nothing.
func TestRunRefusesAnAnalysisItDoesNotHave(t *testing.T) {
	pool := testdb.Pool(t)
	org, _ := testdb.NewOrg(t, pool, "unknown-analysis")
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := Run(ctx, tx, "make_it_up", Input{Question: "?"})
		if err == nil || !strings.Contains(err.Error(), "make_it_up") {
			t.Errorf("an invented analysis returned %v", err)
		}
		return nil
	})
}

// The flagship question. A drop that is really a competitor's rise has to read as one,
// which means the competitors are in the evidence beside the brand.
func TestVisibilityChangeExplainsTheMove(t *testing.T) {
	pool := testdb.Pool(t)
	org, brandID := testdb.NewOrg(t, pool, "vis-change")
	seedForAnalyses(t, pool, org, brandID)

	var got Bundle
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		b, comps, err := brand.Load(ctx, tx)
		if err != nil {
			return err
		}
		got, err = Run(ctx, tx, VisibilityChange, Input{
			Question: "why did our AI visibility drop?", Brand: b, Competitors: comps,
			Now:   time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
			Slots: Slots{From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)},
		})
		return err
	})
	if got.Empty != "" {
		t.Fatalf("nothing to answer with: %s", got.Empty)
	}

	facts := map[string]Fact{}
	for _, f := range got.Facts {
		facts[f.ID] = f
	}
	// This week 2 of 8 answers mentioned the brand; the week before, 6 of 8.
	if f := facts["visibility"]; f.Raw != 25 || f.Trend != "down" {
		t.Errorf("visibility fact = %+v, want 25%% and falling", f)
	}
	if f := facts["previous"]; f.Raw != 75 {
		t.Errorf("visibility before = %+v, want 75%%", f)
	}
	if f := facts["change"]; f.Raw != -50 {
		t.Errorf("change = %+v, want -50 points", f)
	}
	if _, ok := facts["answers"]; !ok {
		t.Error("the answer does not say how many AI answers it is based on")
	}
	tables := map[string]Table{}
	for _, tbl := range got.Tables {
		tables[tbl.ID] = tbl
	}
	if len(tables["by_engine"].Rows) == 0 {
		t.Error("no engine breakdown, so nothing says where the drop came from")
	}
	if rows := tables["competitors"].Rows; len(rows) < 2 {
		t.Errorf("competitors table = %v; a drop that is a competitor's rise must read as one", rows)
	}
	if !strings.Contains(strings.ToLower(got.Headline), "visibility") {
		t.Errorf("headline = %q", got.Headline)
	}
}
