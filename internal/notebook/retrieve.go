package notebook

import (
	"context"
	"math"
	"sort"

	"github.com/jackc/pgx/v5"
)

// Retrieval is hybrid: Postgres full text finds the passages that use the question's
// words, the embedding finds the ones that mean the same thing in other words, and
// reciprocal rank fusion puts the two lists together.
//
// Both halves are needed, and each fails in a way the other covers. Full text alone
// misses "how do we talk about pricing" against a passage headed "commercial
// positioning". Meaning-matching alone misses a product name, a competitor or a figure,
// which is most of what a marketing team actually searches by, because a rare token
// barely moves a sentence embedding.
//
// Fusion is by RANK, not by score. A full-text rank and a cosine similarity are not on
// one scale and never will be, so any weighted sum of them is a tuning parameter
// pretending to be a measurement. RRF needs no tuning and no calibration: it asks only
// which list put a passage near the top.

// RRFk is the constant in 1/(k+rank). 60 is the value the method was published with and
// is what makes a result ranked first worth about as much as being ranked second and
// third elsewhere: a passage both halves like beats one that either half loves.
const RRFk = 60.0

// Retrieved is how many passages an answer may read.
const Retrieved = 8

// Candidates is how deep each half of the search looks before the two are fused.
const Candidates = 30

// Passage is one chunk, with where it came from.
type Passage struct {
	ID     int64
	Source string
	Seq    int
	Text   string
	Title  string
	URL    string
	Score  float64 // the fused score, for ordering only; never shown to anyone
}

// ByText returns the passages whose words match the question, best first.
//
// The query is every word of the question joined with OR, not AND, and the ranking does
// the rest. Postgres's own websearch_to_tsquery and plainto_tsquery both join with AND,
// which is right for a search box and wrong for a question: "what do we say about
// pricing for enterprise customers" would require all six words in one passage, and
// almost nothing has them. With OR, ts_rank_cd puts the passage carrying the most of the
// question's words, closest together, at the top — which is what was wanted.
//
// The lexemes come out of to_tsvector, so they are already normalised and quoting them
// cannot inject anything: the question never reaches the parser as query syntax.
const textSearch = `
	WITH q AS (
	    SELECT to_tsquery('english', string_agg(quote_literal(lexeme), ' | ')) AS tsq
	    FROM unnest(to_tsvector('english', $2))
	)
	SELECT c.id, c.source_id::text, c.seq, c.text, s.title, coalesce(s.url, '')
	FROM notebook_chunks c JOIN notebook_sources s ON s.id = c.source_id, q
	WHERE c.notebook_id::text = $1 AND q.tsq IS NOT NULL AND c.tsv @@ q.tsq
	ORDER BY ts_rank_cd(c.tsv, q.tsq) DESC, c.id LIMIT $3`

func ByText(ctx context.Context, tx pgx.Tx, notebookID, question string, limit int) ([]Passage, error) {
	rows, err := tx.Query(ctx, textSearch, notebookID, question, limit)
	if err != nil {
		return nil, err
	}
	return collectPassages(rows)
}

// ByMeaning returns the passages closest to the question's vector, best first. The scan
// is in Go over one notebook's passages, for the reason the topic vectors are: a
// notebook holds hundreds to low thousands of them, and an approximate index would buy
// nothing for the cost of an extension.
func ByMeaning(ctx context.Context, tx pgx.Tx, notebookID, model string, q []float32, limit int) ([]Passage, error) {
	if len(q) == 0 || model == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT c.id, c.source_id::text, c.seq, c.text, s.title, coalesce(s.url, ''), c.vector
		FROM notebook_chunks c JOIN notebook_sources s ON s.id = c.source_id
		WHERE c.notebook_id::text = $1 AND c.embed_model = $2 AND c.vector IS NOT NULL`, notebookID, model)
	if err != nil {
		return nil, err
	}
	type scored struct {
		p Passage
		s float64
	}
	var all []scored
	for rows.Next() {
		var p Passage
		var v []float32
		if err := rows.Scan(&p.ID, &p.Source, &p.Seq, &p.Text, &p.Title, &p.URL, &v); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, scored{p, cosine(q, v)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].s != all[j].s {
			return all[i].s > all[j].s
		}
		return all[i].p.ID < all[j].p.ID
	})
	out := make([]Passage, 0, limit)
	for i, s := range all {
		if i == limit {
			break
		}
		out = append(out, s.p)
	}
	return out, nil
}

func collectPassages(rows pgx.Rows) ([]Passage, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Passage, error) {
		var p Passage
		err := r.Scan(&p.ID, &p.Source, &p.Seq, &p.Text, &p.Title, &p.URL)
		return p, err
	})
}

// Fuse merges ranked lists with reciprocal rank fusion. A passage in both lists scores
// the sum of its two contributions, so agreement wins without either list's score being
// compared with the other's.
func Fuse(limit int, lists ...[]Passage) []Passage {
	score := map[int64]float64{}
	seen := map[int64]Passage{}
	order := []int64{}
	for _, list := range lists {
		for rank, p := range list {
			if _, ok := seen[p.ID]; !ok {
				seen[p.ID] = p
				order = append(order, p.ID)
			}
			score[p.ID] += 1 / (RRFk + float64(rank+1))
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if score[a] != score[b] {
			return score[a] > score[b]
		}
		return a < b // a stable order for passages nothing separates
	})
	out := make([]Passage, 0, limit)
	for _, id := range order {
		if len(out) == limit {
			break
		}
		p := seen[id]
		p.Score = score[id]
		out = append(out, p)
	}
	return out
}

// Retrieve runs both halves and fuses them. A nil query vector (no embedding service, or
// nothing embedded yet) leaves full text to answer alone, which is worse but is not
// nothing: the notebook keeps working while the vectors catch up.
func Retrieve(ctx context.Context, tx pgx.Tx, notebookID, question, model string, q []float32) ([]Passage, error) {
	text, err := ByText(ctx, tx, notebookID, question, Candidates)
	if err != nil {
		return nil, err
	}
	meaning, err := ByMeaning(ctx, tx, notebookID, model, q, Candidates)
	if err != nil {
		return nil, err
	}
	return Fuse(Retrieved, text, meaning), nil
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot, na, nb = dot+x*y, na+x*x, nb+y*y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
