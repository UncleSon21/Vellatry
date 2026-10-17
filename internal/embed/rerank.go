package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Reranking is the second half of finding a passage, and a different kind of model from
// the first. An embedding turns a question and a passage into vectors separately and
// compares them, which is fast enough to run over a whole corpus and blunt: the question
// is never read next to the passage. A cross-encoder reads the two together and scores
// the pair, which is far better at telling an answer from something merely on the same
// subject, and far too slow to run over everything.
//
// So the two are used in that order: search shortlists, this decides.

// MaxRerank is how many passages the service will score at once.
const MaxRerank = 64

// Ranked is what a question's shortlist scored.
type Ranked struct {
	Model  string
	Scores []float64 // one per passage, in the order they were sent
}

// Rerank scores each passage against the query. The scores are the model's own, which
// are comparable with each other for this one query and mean nothing on their own: order
// by them, and never show one to anyone.
func (c *Client) Rerank(ctx context.Context, query string, passages []string) (Ranked, error) {
	var out Ranked
	if strings.TrimSpace(query) == "" {
		return out, fmt.Errorf("rerank: the query is empty")
	}
	if len(passages) == 0 {
		return out, nil
	}
	if len(passages) > MaxRerank {
		return out, fmt.Errorf("rerank: %d passages, the service takes %d", len(passages), MaxRerank)
	}
	for i, p := range passages {
		if strings.TrimSpace(p) == "" {
			return out, fmt.Errorf("rerank: passage %d is empty", i)
		}
	}
	body, err := json.Marshal(map[string]any{"query": query, "passages": passages})
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/rerank", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return out, fmt.Errorf("rerank: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return out, fmt.Errorf("rerank: %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return out, fmt.Errorf("rerank: bad response: %w", err)
	}
	if out.Model == "" {
		return Ranked{}, fmt.Errorf("rerank: response names no model")
	}
	if len(out.Scores) != len(passages) {
		// Without one score per passage there is no way to know which went with which,
		// and guessing would reorder the answer's citations silently.
		return Ranked{}, fmt.Errorf("rerank: %d passages scored, sent %d", len(out.Scores), len(passages))
	}
	return out, nil
}
