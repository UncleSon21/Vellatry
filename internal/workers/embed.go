package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/embed"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
)

// Embeddings keeps each tenant's topic vectors current and points out a proposed topic
// that looks like one the team already tracks.
//
// The suggestion is for a person: nothing merges, approves or rejects on its own. With
// no embedding service configured the job does nothing, and the rest of the product is
// unaffected.
type Embeddings struct {
	Pool   *pgxpool.Pool
	Client *embed.Client // nil when EMBED_URL is unset
	Logger *slog.Logger
}

// Register adds the embedding jobs.
func (e *Embeddings) Register(ws *river.Workers) {
	river.AddWorker(ws, &topicEmbedWorker{e: e})
}

type topicEmbedWorker struct {
	river.WorkerDefaults[jobargs.TopicEmbed]
	e *Embeddings
}

func (w *topicEmbedWorker) Timeout(*river.Job[jobargs.TopicEmbed]) time.Duration {
	return 5 * time.Minute
}

// embedTopic is what the job needs to know about one topic.
type embedTopic struct {
	ID     string
	Name   string
	Status string
}

func (w *topicEmbedWorker) Work(ctx context.Context, job *river.Job[jobargs.TopicEmbed]) error {
	e, org := w.e, job.Args.OrgID
	if e.Client == nil {
		return nil // EMBED_URL is unset: the feature is off, not broken
	}

	var topics []embedTopic
	var stored []embed.Stored
	if err := db.InTenant(ctx, e.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, name, status FROM topics WHERE status <> 'out_of_scope' ORDER BY created_at`)
		if err != nil {
			return err
		}
		if topics, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (embedTopic, error) {
			var t embedTopic
			err := r.Scan(&t.ID, &t.Name, &t.Status)
			return t, err
		}); err != nil {
			return err
		}
		stored, err = embed.Load(ctx, tx, embed.KindTopic)
		return err
	}); err != nil {
		return err
	}
	if len(topics) == 0 {
		return nil
	}

	have := make(map[string]embed.Stored, len(stored))
	for _, s := range stored {
		have[s.SubjectID] = s
	}

	// New or renamed topics first. The network call stays outside a transaction:
	// holding a tenant transaction open across it would pin a connection for its
	// duration.
	embedded := map[string]embed.Vector{}
	model := ""
	run := func(todo []embedTopic) error {
		if len(todo) == 0 {
			return nil
		}
		texts := make([]string, len(todo))
		for i, t := range todo {
			texts[i] = t.Name
		}
		got, err := e.Client.Embed(ctx, texts)
		if err != nil {
			return err
		}
		model = got.Model
		for i, t := range todo {
			embedded[t.ID] = got.Vectors[i]
		}
		return nil
	}

	var stale []embedTopic
	for _, t := range topics {
		if s, ok := have[t.ID]; !ok || s.TextHash != embed.Hash(t.Name) {
			stale = append(stale, t)
		}
	}
	if err := run(stale); err != nil {
		return err
	}

	// Now that the current model is known, anything held in another model is stale too:
	// vectors from two models are not comparable, so those topics would otherwise drop
	// out of the comparison and never come back.
	if model != "" {
		var wrongModel []embedTopic
		for _, t := range topics {
			if s, ok := have[t.ID]; ok && s.Model != model {
				if _, done := embedded[t.ID]; !done {
					wrongModel = append(wrongModel, t)
				}
			}
		}
		if err := run(wrongModel); err != nil {
			return err
		}
		stale = append(stale, wrongModel...)
	}

	return db.InTenant(ctx, e.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		vectors := make(map[string]embed.Vector, len(topics))
		for _, t := range stale {
			if err := embed.Save(ctx, tx, org, embed.KindTopic, t.ID, model, embed.Hash(t.Name), embedded[t.ID]); err != nil {
				return err
			}
			vectors[t.ID] = embedded[t.ID]
		}
		// Nothing was re-embedded this run, so the model is whatever the stored vectors
		// were made with.
		if model == "" && len(stored) > 0 {
			model = stored[0].Model
		}
		for _, s := range stored {
			if _, replaced := vectors[s.SubjectID]; !replaced && s.Model == model {
				vectors[s.SubjectID] = s.Vector
			}
		}

		// Vectors whose topic is gone stay behind otherwise, and would keep matching.
		live := make(map[string]bool, len(topics))
		for _, t := range topics {
			live[t.ID] = true
		}
		var gone []string
		for _, s := range stored {
			if !live[s.SubjectID] {
				gone = append(gone, s.SubjectID)
			}
		}
		if err := embed.Forget(ctx, tx, embed.KindTopic, gone); err != nil {
			return err
		}

		// A proposed topic is compared with what the team already tracks.
		var tracked []embed.Candidate
		for _, t := range topics {
			if t.Status == "active" {
				if v, ok := vectors[t.ID]; ok {
					tracked = append(tracked, embed.Candidate{ID: t.ID, Model: model, Vector: v})
				}
			}
		}
		suggested := 0
		for _, t := range topics {
			if t.Status != "proposed" {
				continue
			}
			v, ok := vectors[t.ID]
			if !ok {
				continue
			}
			var similarTo *string
			var score *float32
			if best := embed.Similar(model, v, tracked, embed.SameTopic, 1); len(best) > 0 {
				similarTo, score = &best[0].ID, &best[0].Score
				suggested++
			}
			// Written even when nothing matched: the page can then say "checked, and it
			// is new" rather than leaving the question open.
			if _, err := tx.Exec(ctx, `UPDATE topics SET similar_to = $2::uuid, similar_score = $3, similar_checked_at = now() WHERE id = $1`,
				t.ID, similarTo, score); err != nil {
				return err
			}
		}
		if e.Logger != nil {
			e.Logger.InfoContext(ctx, "topics embedded", "org", org, "topics", len(topics), "embedded", len(stale), "duplicates_suggested", suggested)
		}
		return nil
	})
}
