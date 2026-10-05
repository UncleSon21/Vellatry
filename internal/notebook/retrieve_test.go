package notebook

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// Retrieval against a real database: a question is not a search-box query, and the
// passage that answers it rarely contains every word of it.
func TestRetrievalRanksByHowMuchMatches(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "retrieve")

	var nb Notebook
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}
	add := func(title, body string) Source {
		t.Helper()
		var s Source
		tenant(func(ctx context.Context, tx pgx.Tx) error {
			var err error
			if s, err = AddSource(ctx, tx, org, nb.ID, New{Kind: "text", Title: title, Body: body}); err != nil {
				return err
			}
			return SourceRead(ctx, tx, org, s.ID, title, body, Split(body))
		})
		return s
	}

	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		nb, err = Create(ctx, tx, org, "Positioning", "lead@example.com")
		return err
	})
	pricing := add("Pricing guidance", "We never discount for enterprise customers. Enterprise pricing is quoted per seat.")
	add("Office", "The Sydney office is open from nine until five on weekdays.")

	// Not one passage holds every word of this question, which is normal.
	var hits []Passage
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		hits, err = ByText(ctx, tx, nb.ID, "what do we say about pricing for enterprise customers?", Candidates)
		return err
	})
	if len(hits) == 0 {
		t.Fatal("a question whose words are spread across the passage found nothing")
	}
	if hits[0].Source != pricing.ID {
		t.Errorf("the top passage is from %s, want the pricing guidance", hits[0].Title)
	}

	// Full text alone answers when there is no embedding service, which is what makes a
	// notebook usable before the vectors arrive.
	var fused []Passage
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		fused, err = Retrieve(ctx, tx, nb.ID, "enterprise pricing", "", nil)
		return err
	})
	if len(fused) == 0 || fused[0].Source != pricing.ID {
		t.Errorf("retrieval without vectors = %+v", fused)
	}

	// A question of nothing but stop words asks for nothing, rather than erroring or
	// matching every passage.
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		got, err := ByText(ctx, tx, nb.ID, "the and of it", Candidates)
		if err != nil {
			return err
		}
		if len(got) != 0 {
			t.Errorf("a question of stop words matched %d passages", len(got))
		}
		// So does punctuation a query parser would choke on.
		got, err = ByText(ctx, tx, nb.ID, `"<>!()&|:* ' --`, Candidates)
		if err != nil {
			t.Errorf("punctuation broke the search: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("punctuation matched %d passages", len(got))
		}
		return nil
	})

	// Another organisation's notebook is not searchable from here, however the id is
	// come by: row-level security, not a WHERE clause we could forget.
	other, _ := testdb.NewOrg(t, pool, "retrieve-other")
	if err := db.InTenant(ctx, pool, other, func(ctx context.Context, tx pgx.Tx) error {
		got, err := ByText(ctx, tx, nb.ID, "enterprise pricing", Candidates)
		if err != nil {
			return err
		}
		if len(got) != 0 {
			t.Errorf("another organisation read %d passages of this notebook", len(got))
		}
		if _, err := Load(ctx, tx, nb.ID); err != ErrNotFound {
			t.Errorf("another organisation loaded this notebook: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Deleting a source takes its passages with it.
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		if err := DeleteSource(ctx, tx, pricing.ID); err != nil {
			return err
		}
		got, err := ByText(ctx, tx, nb.ID, "enterprise pricing", Candidates)
		if err != nil {
			return err
		}
		if len(got) != 0 {
			t.Errorf("a deleted source left %d passages behind", len(got))
		}
		return nil
	})
}

// A notebook will not queue paid work without end because a page is stuck.
func TestAskStopsAtThreeWaiting(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "asking")

	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		nb, err := Create(ctx, tx, org, "Questions", "")
		if err != nil {
			return err
		}
		for i := 0; i < MaxThinking; i++ {
			if _, err := Ask(ctx, tx, org, nb.ID, "lead@example.com", "What does it say about pricing?"); err != nil {
				return err
			}
		}
		if _, err := Ask(ctx, tx, org, nb.ID, "lead@example.com", "And about support?"); err != ErrAnswersWaiting {
			t.Errorf("a fourth waiting question = %v", err)
		}
		// Answering one makes room for the next.
		msgs, err := Messages(ctx, tx, nb.ID, 10)
		if err != nil {
			return err
		}
		if err := SaveAnswer(ctx, tx, msgs[0].ID, "It says nothing [1].", "answered", []Citation{{Marker: 1, Text: "x"}}, 0); err != nil {
			return err
		}
		if _, err := Ask(ctx, tx, org, nb.ID, "lead@example.com", "And about support?"); err != nil {
			t.Errorf("after one was answered: %v", err)
		}
		// An empty question is not stored, and neither is one of a notebook that is gone.
		if _, err := Ask(ctx, tx, org, nb.ID, "lead@example.com", "   "); err == nil {
			t.Error("an empty question was stored")
		}
		if _, err := Ask(ctx, tx, org, "11111111-1111-1111-1111-111111111111", "x", "What?"); err != ErrNotFound {
			t.Errorf("a question of a notebook that does not exist = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
