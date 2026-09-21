package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/agent"
	"github.com/UncleSon21/vellatry/internal/automation"
	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/events"
)

type askResponse struct {
	ID       int64        `json:"id"`
	Status   string       `json:"status"` // answered | thinking | refused
	Answer   string       `json:"answer,omitempty"`
	Bundle   agent.Bundle `json:"bundle,omitempty"`
	Analysis string       `json:"analysis,omitempty"`
	Intent   string       `json:"intent,omitempty"`
	Sure     float64      `json:"confidence,omitempty"`
}

// ask answers a question about the tenant's own data. A question the router recognises
// is answered here and now from stored rows: no external call, no waiting. Anything
// else is handed to the worker, which may use a model to choose an analysis, and the
// dashboard hears back over the event stream.
func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Question string        `json:"question"`
		Context  agent.Context `json:"context"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	in.Question = strings.TrimSpace(in.Question)
	if in.Question == "" || len(in.Question) > 500 {
		s.fail(w, r, badRequest("Ask a question of up to 500 characters."))
		return
	}
	var out askResponse
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		org, user := sessionFrom(ctx).OrgID, sessionFrom(ctx).UserID
		known, err := agent.LoadKnown(ctx, tx)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		route := agent.Pick(in.Question, in.Context, known, now)
		out.Intent, out.Sure = route.Intent, route.Confidence

		if route.Intent == "" || route.Confidence < agent.Confident {
			id, err := agent.Record(ctx, tx, org, user, in.Question, in.Context, route, "thinking")
			if err != nil {
				return err
			}
			out.ID, out.Status = id, "thinking"
			_, err = s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.AgentQuestionAsked, SubjectID: strconv.FormatInt(id, 10),
				Actor: user, Payload: map[string]any{"question_id": id}})
			return err
		}

		b, comps, err := brand.Load(ctx, tx)
		if err != nil && !errors.Is(err, brand.ErrNoBrand) {
			return err
		}
		started := time.Now()
		bundle, err := agent.Run(ctx, tx, route.Intent, agent.Input{Question: in.Question, Slots: route.Slots, Brand: b, Competitors: comps, Now: now})
		if err != nil {
			return err
		}
		answer := agent.Narrate(bundle)
		id, err := agent.Record(ctx, tx, org, user, in.Question, in.Context, route, "answered")
		if err != nil {
			return err
		}
		if err := agent.Answered(ctx, tx, id, bundle, answer, false, nil, time.Since(started)); err != nil {
			return err
		}
		out = askResponse{ID: id, Status: "answered", Answer: answer, Bundle: bundle, Analysis: bundle.Analysis,
			Intent: route.Intent, Sure: route.Confidence}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if out.Status == "thinking" {
		status = http.StatusAccepted
	}
	writeJSON(w, status, out)
}

func (s *Server) agentQuestion(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, notFound("Question not found."))
		return
	}
	var out agent.Asked
	err = s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = agent.Load(ctx, tx, id)
		if errors.Is(err, agent.ErrNoQuestion) {
			return notFound("Question not found.")
		}
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) agentHistory(w http.ResponseWriter, r *http.Request) {
	var out []agent.Asked
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = agent.Recent(ctx, tx, limitParam(r, 50, 200))
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"questions": nonNilT(out), "analyses": agent.Catalogue})
}

// agentAction runs an action the agent proposed, after the person confirmed it. Every
// one of these is something they could do themselves on the page it links to.
func (s *Server) agentAction(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		Kind   string            `json:"kind"`
		Params map[string]string `json:"params"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	var done string
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		org, user := sessionFrom(ctx).OrgID, sessionFrom(ctx).UserID
		switch in.Kind {
		case agent.ActionCrawlSite:
			done = "Vellatry is crawling the site."
			_, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.SiteCrawlRequested, Actor: user})
			return err
		case agent.ActionResearch:
			done = "Keyword research has started."
			_, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.KeywordRunRequested, Actor: user, Payload: map[string]any{"seeds": []string{}}})
			return err
		case agent.ActionCheckNow:
			id := in.Params["prompt_id"]
			if !validUUID(id) {
				return badRequest("That question isn't one of your tracked prompts.")
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM prompts WHERE id::text = $1)`, id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return notFound("Prompt not found.")
			}
			done = "Vellatry is asking every engine that question now."
			_, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.CheckRequested, SubjectID: id, Actor: user})
			return err
		case agent.ActionWatcher:
			kind := in.Params["kind"]
			w := automation.Watcher{Kind: kind, Delivery: "immediate", Enabled: true}
			if p := in.Params["points"]; p != "" {
				v, err := strconv.ParseFloat(p, 64)
				if err != nil {
					return badRequest("The number of points must be a number.")
				}
				w.Params.Points = v
			}
			if c := in.Params["competitor"]; c != "" {
				w.Params.Competitor = c
			}
			eng, err := engines(ctx, tx)
			if err != nil {
				return err
			}
			wt, err := automation.Normalise(w, eng)
			if err != nil {
				return automationErr(err)
			}
			params, _ := json.Marshal(wt.Params)
			done = "The watcher is on."
			_, err = tx.Exec(ctx, `INSERT INTO watchers (org_id, kind, params, delivery, source, created_by)
				VALUES ($1, $2, $3, $4, 'agent', $5)`, org, wt.Kind, params, wt.Delivery, user)
			return err
		case agent.ActionApproveTopic:
			id := in.Params["topic_id"]
			if !validUUID(id) {
				return badRequest("Unknown topic.")
			}
			tag, err := tx.Exec(ctx, `UPDATE topics SET status = 'active', updated_at = now() WHERE id::text = $1 AND status = 'proposed'`, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return notFound("No proposed topic with that id.")
			}
			if _, err := promptsForTopic(ctx, tx, id); err != nil {
				return err
			}
			done = "The topic is approved; Vellatry will start measuring it."
			_, err = s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.TopicAdded, SubjectID: id, Actor: user})
			return err
		case agent.ActionSendToAsana:
			return badRequest("Send it to Asana from the fix or blindspot itself.")
		}
		return badRequest("That isn't something the agent can do.")
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"done": done})
}
