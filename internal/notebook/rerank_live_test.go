package notebook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/embed"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// What reranking is worth, measured against the real thing: a real notebook in Postgres,
// real full-text search, real vectors, real reciprocal rank fusion, and then the real
// cross-encoder. Skipped unless EMBED_URL is set.
//
//	python -m uvicorn app:app --port 8099 --app-dir ml/embed
//	EMBED_URL=http://localhost:8099 go test ./internal/notebook/ -run Live -v
//
// The corpus and the questions are the ones in ml/embed/eval, written by hand. They are
// somebody's idea of what should rank first rather than a customer's, so this is a guard
// against reranking making retrieval worse, not a measurement of how much better it is.
// It fails if reranking loses ground, which is the thing worth catching automatically;
// the numbers behind the choice of model are in ml/embed/README.md.
func TestLiveRerankBeatsSearchAlone(t *testing.T) {
	url := os.Getenv("EMBED_URL")
	if url == "" {
		t.Skip("set EMBED_URL to measure reranking against a real service")
	}
	pool := testdb.Pool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	org, _ := testdb.NewOrg(t, pool, "rerank-live")
	client := &embed.Client{URL: url}

	var corpus struct {
		Passages  map[string]string `json:"passages"`
		Questions []struct {
			Q    string   `json:"q"`
			Want []string `json:"want"`
		} `json:"questions"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "ml", "embed", "eval", "corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}

	// Each passage is its own source, so a passage's name is the source's title and the
	// retrieved results say which one came back.
	var nb Notebook
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if nb, err = Create(ctx, tx, org, "Reranking", ""); err != nil {
			return err
		}
		for name, text := range corpus.Passages {
			s, err := AddSource(ctx, tx, org, nb.ID, New{Kind: "text", Title: name, Body: text})
			if err != nil {
				return err
			}
			if err := SourceRead(ctx, tx, org, s.ID, name, text, Split(text)); err != nil {
				return err
			}
		}
		return nil
	})

	// Vectors, as the worker's embedding pass gives them.
	var model string
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		todo, err := Unembedded(ctx, tx, "", 500)
		if err != nil {
			return err
		}
		texts := make([]string, len(todo))
		for i, p := range todo {
			texts[i] = p.Text
		}
		res, err := client.Embed(ctx, texts)
		if err != nil {
			return err
		}
		model = res.Model
		for i, p := range todo {
			if err := SaveVector(ctx, tx, p.ID, res.Model, res.Vectors[i]); err != nil {
				return err
			}
		}
		return nil
	})

	var searchRR, rerankRR float64
	var searchTop, rerankTop int
	for _, q := range corpus.Questions {
		qv, err := client.Embed(ctx, []string{q.Q})
		if err != nil {
			t.Fatal(err)
		}
		var shortlist []Passage
		tenant(func(ctx context.Context, tx pgx.Tx) error {
			var err error
			shortlist, err = Retrieve(ctx, tx, nb.ID, q.Q, model, qv.Vectors[0])
			return err
		})
		if len(shortlist) == 0 {
			t.Errorf("%q matched nothing at all", q.Q)
			continue
		}
		texts := make([]string, len(shortlist))
		for i, p := range shortlist {
			texts[i] = p.Text
		}
		ranked, err := client.Rerank(ctx, q.Q, texts)
		if err != nil {
			t.Fatal(err)
		}

		want := map[string]bool{}
		for _, w := range q.Want {
			want[w] = true
		}
		rank := func(ps []Passage) (float64, int) {
			for i, p := range ps {
				if want[p.Title] {
					if i == 0 {
						return 1 / float64(i+1), 1
					}
					return 1 / float64(i+1), 0
				}
			}
			return 0, 0
		}
		sr, st := rank(Top(shortlist, Retrieved))
		rr, rt := rank(Best(shortlist, ranked.Scores, Retrieved))
		searchRR, searchTop = searchRR+sr, searchTop+st
		rerankRR, rerankTop = rerankRR+rr, rerankTop+rt
		if rr < sr {
			t.Logf("reranking pushed %q down: search %.2f, reranked %.2f", q.Q, sr, rr)
		}
	}

	n := float64(len(corpus.Questions))
	t.Logf("search alone: MRR %.3f, right answer first %d of %d", searchRR/n, searchTop, len(corpus.Questions))
	t.Logf("reranked:     MRR %.3f, right answer first %d of %d", rerankRR/n, rerankTop, len(corpus.Questions))
	if rerankRR < searchRR {
		t.Errorf("reranking made retrieval worse: MRR %.3f against %.3f. Either the model changed or the shortlist is too shallow",
			rerankRR/n, searchRR/n)
	}
	if rerankTop < searchTop {
		t.Errorf("reranking put the right passage first less often: %d against %d", rerankTop, searchTop)
	}
}
