package workers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/embed"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// fakeEmbeddings stands in for ml/embed. Texts sharing their first word get nearly the
// same vector, which is what the real model does with "mattresses" and "mattress".
func fakeEmbeddings(t *testing.T, model string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		vectors := make([][]float32, len(in.Texts))
		for i, text := range in.Texts {
			head := strings.Fields(strings.ToLower(text))[0]
			v := []float32{0, 0, 0}
			switch {
			case strings.HasPrefix(head, "mattress"):
				v = []float32{1, 0.05, 0}
			case strings.HasPrefix(head, "sofa"):
				v = []float32{0, 1, 0}
			default:
				v = []float32{0, 0, 1}
			}
			// A second word nudges it, so "mattresses" and "mattress for back pain"
			// are close but not identical.
			if len(strings.Fields(text)) > 1 {
				v[2] += 0.2
			}
			vectors[i] = v
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "dim": 3, "vectors": vectors})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestTopicEmbedSuggestsDuplicates(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, brand := testdb.NewOrg(t, pool, "embed")

	add := func(name, status string) string {
		t.Helper()
		var id string
		if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO topics (org_id, brand_id, name, source, status)
				VALUES ($1, $2, $3, 'manual', $4) RETURNING id::text`, org, brand, name, status).Scan(&id)
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	type suggestion struct {
		SimilarTo *string
		Score     *float32
		Checked   bool
	}
	read := func(id string) suggestion {
		t.Helper()
		var s suggestion
		if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT similar_to::text, similar_score, similar_checked_at IS NOT NULL FROM topics WHERE id = $1`, id).
				Scan(&s.SimilarTo, &s.Score, &s.Checked)
		}); err != nil {
			t.Fatal(err)
		}
		return s
	}

	tracked := add("mattresses", "active")
	dupe := add("mattress", "proposed")               // the same topic in other words
	fresh := add("sofa beds", "proposed")             // genuinely new
	skipped := add("mattress covers", "out_of_scope") // decided already: left alone

	srv, calls := fakeEmbeddings(t, "test-model")
	w := &topicEmbedWorker{e: &Embeddings{Pool: pool, Client: &embed.Client{URL: srv.URL}, Logger: slog.Default()}}
	if err := w.Work(ctx, job(jobargs.TopicEmbed{OrgID: org})); err != nil {
		t.Fatal(err)
	}

	if got := read(dupe); got.SimilarTo == nil || *got.SimilarTo != tracked || got.Score == nil || *got.Score < embed.SameTopic {
		t.Errorf("'mattress' should look like the tracked 'mattresses': %+v", got)
	}
	if got := read(fresh); got.SimilarTo != nil || !got.Checked {
		t.Errorf("'sofa beds' is new, and should be marked checked with nothing similar: %+v", got)
	}
	if got := read(skipped); got.Checked {
		t.Error("a topic the team already ruled out was checked anyway")
	}

	// Vectors are stored per tenant, and a second run embeds nothing again.
	var vectors int
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM embeddings WHERE kind = 'topic'`).Scan(&vectors)
	}); err != nil {
		t.Fatal(err)
	}
	if vectors != 3 {
		t.Errorf("%d vectors stored, want 3 (out_of_scope topics are not embedded)", vectors)
	}
	before := *calls
	if err := w.Work(ctx, job(jobargs.TopicEmbed{OrgID: org})); err != nil {
		t.Fatal(err)
	}
	if *calls != before {
		t.Errorf("a second run embedded again (%d calls, was %d); nothing had changed", *calls, before)
	}

	// A rename makes that topic stale, and only that one.
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE topics SET name = 'sofa bed' WHERE id = $1`, fresh)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before = *calls
	if err := w.Work(ctx, job(jobargs.TopicEmbed{OrgID: org})); err != nil {
		t.Fatal(err)
	}
	if *calls != before+1 {
		t.Errorf("a rename should cost one call, got %d", *calls-before)
	}
}

func TestTopicEmbedWithoutAService(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "embed-off")
	w := &topicEmbedWorker{e: &Embeddings{Pool: pool, Logger: slog.Default()}} // no client
	if err := w.Work(ctx, job(jobargs.TopicEmbed{OrgID: org})); err != nil {
		t.Errorf("with no embedding service the job should do nothing, not fail: %v", err)
	}
}

func TestTopicEmbedReplacesVectorsFromAnotherModel(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, brand := testdb.NewOrg(t, pool, "embed-model")

	var tracked, proposed string
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO topics (org_id, brand_id, name, source, status)
			VALUES ($1, $2, 'mattresses', 'manual', 'active') RETURNING id::text`, org, brand).Scan(&tracked); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO topics (org_id, brand_id, name, source, status)
			VALUES ($1, $2, 'mattress', 'manual', 'proposed') RETURNING id::text`, org, brand).Scan(&proposed); err != nil {
			return err
		}
		// The tracked topic was embedded by an older model, with a matching text hash.
		return embed.Save(ctx, tx, org, embed.KindTopic, tracked, "older-model", embed.Hash("mattresses"), embed.Vector{0.2, 0.9, 0.1})
	}); err != nil {
		t.Fatal(err)
	}

	srv, _ := fakeEmbeddings(t, "test-model")
	w := &topicEmbedWorker{e: &Embeddings{Pool: pool, Client: &embed.Client{URL: srv.URL}, Logger: slog.Default()}}
	if err := w.Work(ctx, job(jobargs.TopicEmbed{OrgID: org})); err != nil {
		t.Fatal(err)
	}

	// The stale-model vector was replaced, so the pair can be compared at all.
	var model string
	var similar *string
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT model FROM embeddings WHERE kind = 'topic' AND subject_id = $1`, tracked).Scan(&model); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT similar_to::text FROM topics WHERE id = $1`, proposed).Scan(&similar)
	}); err != nil {
		t.Fatal(err)
	}
	if model != "test-model" {
		t.Errorf("the old model's vector survived: %s", model)
	}
	if similar == nil || *similar != tracked {
		t.Errorf("after re-embedding, the duplicate should be spotted: %v", similar)
	}
}
