package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/keywords"
	"github.com/UncleSon21/vellatry/internal/platform/events"
)

// requestResearch asks the worker for a keyword research run. The api never calls
// DataForSEO; it records the request and the dashboard follows the run's events.
func (s *Server) requestResearch(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		Seeds []string `json:"seeds"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if len(in.Seeds) > 50 {
		s.fail(w, r, badRequest("Add up to 50 seed keywords."))
		return
	}
	var seeds []string
	for _, x := range in.Seeds {
		if x = strings.TrimSpace(strings.ToLower(x)); x != "" && len(x) <= 80 {
			seeds = append(seeds, x)
		}
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		busy, err := keywords.InFlight(ctx, tx)
		if err != nil {
			return err
		}
		if busy {
			return conflict("Keyword research is already running.")
		}
		_, err = s.Bus.Emit(ctx, tx, sessionFrom(ctx).OrgID, events.Event{Kind: domainevents.KeywordRunRequested, Actor: sessionFrom(ctx).UserID,
			Payload: map[string]any{"seeds": nonNil(seeds)}})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

func (s *Server) listResearchRuns(w http.ResponseWriter, r *http.Request) {
	var out []keywords.Run
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = keywords.ListRuns(ctx, tx, limitParam(r, 20, 100))
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

func (s *Server) listKeywords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := q.Get("status")
	switch status {
	case "", "candidate", "approved", "rejected":
	default:
		s.fail(w, r, badRequest("Status must be candidate, approved or rejected."))
		return
	}
	topic := q.Get("topic")
	if topic != "" && !validUUID(topic) {
		s.fail(w, r, badRequest("Unknown topic."))
		return
	}
	var out []keywords.KeywordRow
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = keywords.List(ctx, tx, topic, status, q.Get("q"), limitParam(r, 200, 2000))
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

// patchKeyword records the team's decision. A rejection keeps its reason: it is what
// the relevance classifier will learn from.
func (s *Server) patchKeyword(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Status != "approved" && in.Status != "rejected" && in.Status != "candidate" {
		s.fail(w, r, badRequest("Status must be approved, rejected or candidate."))
		return
	}
	if len(in.Reason) > 200 {
		s.fail(w, r, badRequest("Keep the reason under 200 characters."))
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		err := keywords.SetStatus(ctx, tx, r.PathValue("keyword"), in.Status, in.Reason)
		if errors.Is(err, keywords.ErrNoKeyword) {
			return notFound("That keyword isn't in your set.")
		}
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// topicDetail is one topic with what research worked out about it.
type topicDetail struct {
	topic
	Intent      *string               `json:"intent"`
	KeywordN    int                   `json:"keyword_count"`
	PageURL     *string               `json:"page_url"`
	PageSource  *string               `json:"page_source"`
	PageKind    *string               `json:"page_kind"`
	Opportunity json.RawMessage       `json:"opportunity"`
	Issues      json.RawMessage       `json:"issues"`
	Keywords    []keywords.KeywordRow `json:"keywords,omitempty"`
	UpdatedAt   time.Time             `json:"updated_at"`
	// A topic this one looks like, if any: a suggestion from the embedding job for a
	// person to act on, never a decision. SimilarChecked distinguishes "checked, and
	// it is new" from "not looked at yet".
	SimilarTo      *string  `json:"similar_to"`
	SimilarName    *string  `json:"similar_name"`
	SimilarScore   *float32 `json:"similar_score"`
	SimilarChecked bool     `json:"similar_checked"`
}

const topicDetailCols = `t.id::text, t.name, t.source, t.demand_monthly, t.status, t.created_at,
	(SELECT count(*) FROM prompts p WHERE p.topic_id = t.id AND p.status <> 'rejected')::int,
	t.intent, t.keyword_count, t.page_url, t.page_source, t.page_kind, t.opportunity, t.issues, t.updated_at,
	t.similar_to::text, (SELECT s.name FROM topics s WHERE s.id = t.similar_to), t.similar_score, t.similar_checked_at IS NOT NULL`

func scanTopicDetail(r pgx.Row) (topicDetail, error) {
	var d topicDetail
	err := r.Scan(&d.ID, &d.Name, &d.Source, &d.DemandMonthly, &d.Status, &d.CreatedAt, &d.Prompts,
		&d.Intent, &d.KeywordN, &d.PageURL, &d.PageSource, &d.PageKind, &d.Opportunity, &d.Issues, &d.UpdatedAt,
		&d.SimilarTo, &d.SimilarName, &d.SimilarScore, &d.SimilarChecked)
	return d, err
}

// listTopicsDetail lists topics with their research, most valuable first.
func (s *Server) listTopicsDetail(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	switch status {
	case "", "proposed", "active", "out_of_scope":
	default:
		s.fail(w, r, badRequest("Status must be proposed, active or out_of_scope."))
		return
	}
	var out []topicDetail
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+topicDetailCols+` FROM topics t WHERE ($1 = '' OR t.status = $1)
			ORDER BY (t.opportunity->>'score')::float8 DESC NULLS LAST, t.demand_monthly DESC NULLS LAST, t.name LIMIT $2`,
			status, limitParam(r, 200, 1000))
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (topicDetail, error) { return scanTopicDetail(r) })
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

func (s *Server) getTopic(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, r, notFound("Topic not found."))
		return
	}
	var out topicDetail
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if out, err = scanTopicDetail(tx.QueryRow(ctx, `SELECT `+topicDetailCols+` FROM topics t WHERE t.id::text = $1`, id)); err != nil {
			if isNoRows(err) {
				return notFound("Topic not found.")
			}
			return err
		}
		out.Keywords, err = keywords.List(ctx, tx, id, "", "", 500)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
