package read

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// Everything the Visibility pages show comes out of this package, and until now none of
// its five reads was run by a test: the pure scoring was covered and the SQL that
// produces every number on Performance, Blindspots, Sources and Answers was not.

var (
	day1 = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	day2 = time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
)

func tenant(t *testing.T, pool *pgxpool.Pool, org string, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(context.Background(), pool, org, fn); err != nil {
		t.Fatal(err)
	}
}

func f(v *float64) float64 {
	if v == nil {
		return -1
	}
	return *v
}

// Performance is the page a team checks first, and every figure on it is a ratio of two
// stored counts. The denominators are what this pins: visibility is of the answers the
// brand could have appeared in, not of every answer collected.
func TestLoadPerformance(t *testing.T) {
	pool := testdb.Pool(t)
	org, _ := testdb.NewOrg(t, pool, "perf")

	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		// Two engines over two days. 20 answers collected, 16 of them on topic
		// ("present"), 6 mentioning the brand.
		for _, r := range []struct {
			day                                       time.Time
			engine                                    string
			answers, present, mentioned, posSum, posN int
			sentSum                                   float64
			sentN                                     int
		}{
			{day1, "chatgpt", 5, 4, 2, 3, 2, 14, 2},
			{day2, "chatgpt", 5, 4, 1, 1, 1, 6, 1},
			{day1, "gemini", 5, 4, 2, 4, 2, 0, 0},
			{day2, "gemini", 5, 4, 1, 2, 1, 0, 0},
		} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO visibility_daily (org_id, day, engine, answers, present, mentioned, position_sum, position_n, sentiment_sum, sentiment_n, method_version)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'vis-1')`,
				org, r.day, r.engine, r.answers, r.present, r.mentioned, r.posSum, r.posN, r.sentSum, r.sentN); err != nil {
				return err
			}
		}
		// Who was mentioned: the brand 6 times, two competitors 2 each. Equal mentions
		// on purpose — the order of those two must not depend on a map.
		for _, e := range []struct {
			day               time.Time
			engine, name      string
			isBrand           bool
			answers, mentions int
		}{
			{day1, "chatgpt", "Koala", true, 2, 4},
			{day2, "chatgpt", "Koala", true, 1, 2},
			{day1, "chatgpt", "Ecosa", false, 2, 2},
			{day1, "chatgpt", "Emma", false, 2, 2},
		} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO visibility_daily_entities (org_id, day, engine, entity, is_brand, answers, mentions)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, org, e.day, e.engine, e.name, e.isBrand, e.answers, e.mentions); err != nil {
				return err
			}
		}
		// A day outside the period, which nothing should count.
		_, err := tx.Exec(ctx, `
			INSERT INTO visibility_daily (org_id, day, engine, answers, present, mentioned, method_version)
			VALUES ($1, '2026-07-31', 'chatgpt', 99, 99, 99, 'vis-0')`, org)
		return err
	})

	var p Performance
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		p, err = LoadPerformance(ctx, tx, day1, day2)
		return err
	})

	if p.From != "2026-08-01" || p.To != "2026-08-02" {
		t.Errorf("period = %s..%s", p.From, p.To)
	}
	if p.Overall.Answers != 20 || p.Overall.Present != 16 || p.Overall.Mentioned != 6 {
		t.Errorf("overall counts = %+v; the day outside the period should not be in them", p.Overall)
	}
	// 6 of the 16 answers the brand could have appeared in.
	if got := f(p.Overall.Visibility); got != 37.5 {
		t.Errorf("overall visibility = %v, want 37.5 (mentioned over present, not over answers)", got)
	}
	// 6 brand mentions of 10 mentions of any tracked brand.
	if got := f(p.Overall.ShareOfVoice); got != 60 {
		t.Errorf("overall share of voice = %v, want 60", got)
	}
	if got := f(p.Overall.AvgPosition); got != 1.67 {
		t.Errorf("overall average position = %v, want 1.67 (10 over 6)", got)
	}
	// Judged answers only: 20 over 3, and the unjudged engine does not drag it down.
	if got := f(p.Overall.Sentiment); got != 6.67 {
		t.Errorf("overall sentiment = %v, want 6.67 over the judged answers alone", got)
	}
	if len(p.MethodVersions) != 1 || p.MethodVersions[0] != "vis-1" {
		t.Errorf("method versions = %v", p.MethodVersions)
	}

	if len(p.ByEngine) != 2 || p.ByEngine[0].Engine != "chatgpt" || p.ByEngine[1].Engine != "gemini" {
		t.Fatalf("by engine = %+v, want chatgpt then gemini", p.ByEngine)
	}
	if got := f(p.ByEngine[0].Visibility); got != 37.5 {
		t.Errorf("chatgpt visibility = %v", got)
	}
	// Every mention recorded is on chatgpt, so the brand's share there is its share of all.
	if got := f(p.ByEngine[0].ShareOfVoice); got != 60 {
		t.Errorf("chatgpt share of voice = %v", got)
	}
	// Nothing was judged on gemini, so it has no sentiment rather than a zero.
	if p.ByEngine[1].Sentiment != nil {
		t.Errorf("gemini sentiment = %v, want none", *p.ByEngine[1].Sentiment)
	}

	if len(p.Series) != 4 {
		t.Errorf("series = %d points, want one per day and engine", len(p.Series))
	}
	if p.Series[0].Day != "2026-08-01" || p.Series[0].Engine != "chatgpt" || f(p.Series[0].Visibility) != 50 {
		t.Errorf("first point = %+v", p.Series[0])
	}

	// The brand leads on mentions; the two competitors are tied, and a tie has to come
	// out the same way every time or the table reshuffles itself between page loads.
	if len(p.Entities) != 3 || !p.Entities[0].IsBrand || p.Entities[0].Mentions != 6 {
		t.Fatalf("entities = %+v", p.Entities)
	}
	if got := f(p.Entities[0].ShareOfVoice); got != 60 {
		t.Errorf("the brand's share of voice among entities = %v", got)
	}
	if p.Entities[1].Name != "Ecosa" || p.Entities[2].Name != "Emma" {
		t.Errorf("entities on equal mentions = %q then %q, want them in name order",
			p.Entities[1].Name, p.Entities[2].Name)
	}
	for i := 0; i < 8; i++ {
		var again Performance
		tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			again, err = LoadPerformance(ctx, tx, day1, day2)
			return err
		})
		for k := range again.Entities {
			if again.Entities[k].Name != p.Entities[k].Name {
				t.Fatalf("entity %d is %q on one read and %q on the next: the order of equal mentions is not decided",
					k, p.Entities[k].Name, again.Entities[k].Name)
			}
		}
	}

	// A period with nothing in it is empty, not zeroes: a rate over no answers is
	// unknown, and the dashboard shows "-" rather than 0%.
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		empty, err := LoadPerformance(ctx, tx, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
		if err != nil {
			return err
		}
		if empty.Overall.Visibility != nil || empty.Overall.ShareOfVoice != nil {
			t.Errorf("a period with no answers reported %+v, want no rates at all", empty.Overall)
		}
		if empty.ByEngine == nil || empty.Series == nil || empty.Entities == nil {
			t.Error("the empty page must send empty lists, not nulls")
		}
		return nil
	})

	// Another organisation's rollups are not in these numbers.
	other, _ := testdb.NewOrg(t, pool, "perf-other")
	tenant(t, pool, other, func(ctx context.Context, tx pgx.Tx) error {
		mine, err := LoadPerformance(ctx, tx, day1, day2)
		if err != nil {
			return err
		}
		if mine.Overall.Answers != 0 {
			t.Errorf("another organisation saw %d answers", mine.Overall.Answers)
		}
		return nil
	})
}
