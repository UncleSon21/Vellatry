package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/agent"
	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
)

// Agent answers the questions the router was not sure about. A model chooses which
// analysis fits and puts its numbers into sentences; the numbers themselves are always
// the ones the analysis computed, and a sentence citing anything else is dropped.
type Agent struct {
	Pool    *pgxpool.Pool
	Bus     *events.Bus
	Logger  *slog.Logger
	Planner Completer // nil: the agent answers only what the router recognises
	Now     func() time.Time
}

// Register adds the agent job.
func (a *Agent) Register(ws *river.Workers) {
	if a.Now == nil {
		a.Now = time.Now
	}
	river.AddWorker(ws, &agentAnswerWorker{a: a})
}

type agentAnswerWorker struct {
	river.WorkerDefaults[jobargs.AgentAnswer]
	a *Agent
}

func (w *agentAnswerWorker) Timeout(*river.Job[jobargs.AgentAnswer]) time.Duration {
	return 2 * time.Minute
}

func (w *agentAnswerWorker) Work(ctx context.Context, job *river.Job[jobargs.AgentAnswer]) error {
	a, org := w.a, job.Args.OrgID
	started := time.Now()
	var asked agent.Asked
	var known agent.Known
	var b brand.Brand
	var comps []brand.Competitor
	err := db.InTenant(ctx, a.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if asked, err = agent.Load(ctx, tx, job.Args.QuestionID); err != nil {
			return err
		}
		if asked.Status != "thinking" {
			return errAlreadyAnswered
		}
		if known, err = agent.LoadKnown(ctx, tx); err != nil {
			return err
		}
		b, comps, err = brand.Load(ctx, tx)
		if errors.Is(err, brand.ErrNoBrand) {
			return nil
		}
		return err
	})
	if errors.Is(err, errAlreadyAnswered) || errors.Is(err, agent.ErrNoQuestion) {
		return nil
	}
	if err != nil {
		return err
	}

	if a.Planner == nil {
		return a.refuse(ctx, org, asked.ID, "I can only answer the questions in the list for now. Try one of these: "+catalogueList()+".")
	}
	choice, err := a.plan(ctx, org, asked, known)
	if err != nil {
		a.Logger.WarnContext(ctx, "agent planner failed", "org_id", org, "question_id", asked.ID, "error", err)
		return a.refuse(ctx, org, asked.ID, "I could not work out which analysis answers that. Try one of these: "+catalogueList()+".")
	}
	if choice.Analysis == "" {
		reason := strings.TrimSpace(choice.Reason)
		if reason == "" {
			reason = "that is not something Vellatry measures yet"
		}
		return a.refuse(ctx, org, asked.ID, "I cannot answer that: "+reason+". I can answer: "+catalogueList()+".")
	}

	var ctxIn agent.Context
	_ = json.Unmarshal(asked.Context, &ctxIn)
	route := agent.Pick(asked.Question, ctxIn, known, a.Now().UTC())
	slots := route.Slots
	if choice.Engine != "" {
		slots.Engine = choice.Engine
	}
	if choice.Competitor != "" {
		slots.Competitor = choice.Competitor
	}

	var bundle agent.Bundle
	err = db.InTenant(ctx, a.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		bundle, err = agent.Run(ctx, tx, choice.Analysis, agent.Input{Question: asked.Question, Slots: slots, Brand: b, Competitors: comps, Now: a.Now().UTC()})
		return err
	})
	if errors.Is(err, agent.ErrUnknownAnalysis) {
		return a.refuse(ctx, org, asked.ID, "I cannot answer that yet. I can answer: "+catalogueList()+".")
	}
	if err != nil {
		return err
	}

	answer, problems := agent.Checked(a.narrate(ctx, org, bundle), bundle)
	if len(problems) > 0 {
		a.Logger.WarnContext(ctx, "narration dropped: figures not in the evidence",
			"org_id", org, "question_id", asked.ID, "figures", strings.Join(problems, ", "))
	}
	return db.InTenant(ctx, a.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := agent.Answered(ctx, tx, asked.ID, bundle, answer, true, problems, time.Since(started)); err != nil {
			return err
		}
		_, err := a.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.AgentAnswered, SubjectID: itoa64(asked.ID), Actor: "agent",
			Payload: map[string]any{"question_id": asked.ID, "analysis": bundle.Analysis, "checked": len(problems) == 0}})
		return err
	})
}

var errAlreadyAnswered = errors.New("already answered")

func (a *Agent) refuse(ctx context.Context, org string, id int64, why string) error {
	return db.InTenant(ctx, a.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := agent.Failed(ctx, tx, id, "refused", why); err != nil {
			return err
		}
		_, err := a.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.AgentAnswered, SubjectID: itoa64(id), Actor: "agent",
			Payload: map[string]any{"question_id": id, "refused": true}})
		return err
	})
}

func catalogueList() string {
	var out []string
	for _, c := range agent.Catalogue {
		out = append(out, strings.TrimSuffix(c.Question, "?"))
	}
	return strings.Join(out[:4], "; ") + "; and " + fmt.Sprint(len(out)-4) + " more"
}

// choice is the planner's answer: which analysis to run, or none.
type choice struct {
	Analysis   string `json:"analysis"`
	Engine     string `json:"engine"`
	Competitor string `json:"competitor"`
	Reason     string `json:"reason"`
}

const plannerPrompt = `You choose which of Vellatry's analyses answers a marketing team's question about their own data. You do not answer the question yourself.

Rules:
- Answer with one analysis name from the list, or an empty name when none of them fits.
- Never invent an analysis name.
- Only set "engine" or "competitor" when the question names one, spelled as the list spells it.
- "reason" is one short sentence: why that analysis, or why none fits.`

func (a *Agent) plan(ctx context.Context, org string, asked agent.Asked, known agent.Known) (choice, error) {
	var sb strings.Builder
	sb.WriteString("Analyses:\n")
	for _, c := range agent.Catalogue {
		sb.WriteString("- " + c.Name + ": " + c.Question + " (reads " + c.Needs + ")\n")
	}
	if len(known.Engines) > 0 {
		sb.WriteString("\nEngines: " + strings.Join(known.Engines, ", ") + "\n")
	}
	if len(known.Competitors) > 0 {
		sb.WriteString("Competitors: " + strings.Join(known.Competitors, ", ") + "\n")
	}
	sb.WriteString("\nQuestion: " + asked.Question + "\n")

	resp, err := a.Planner.Complete(ctx, gateway.Request{
		Purpose: "agent_plan", OrgID: org, System: plannerPrompt,
		Messages: []gateway.Message{{Role: "user", Content: sb.String()}},
		Schema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"analysis", "reason"},
			"properties": map[string]any{
				"analysis":   map[string]any{"type": "string"},
				"engine":     map[string]any{"type": "string"},
				"competitor": map[string]any{"type": "string"},
				"reason":     map[string]any{"type": "string"},
			},
		},
	})
	if err != nil {
		return choice{}, err
	}
	var c choice
	if err := json.Unmarshal([]byte(resp.Text), &c); err != nil {
		return c, fmt.Errorf("agent: planner reply: %w", err)
	}
	c.Analysis = strings.TrimSpace(c.Analysis)
	if c.Analysis != "" {
		known := false
		for _, x := range agent.Catalogue {
			known = known || x.Name == c.Analysis
		}
		if !known {
			return choice{Reason: c.Reason}, nil // an invented name is the same as none
		}
	}
	return c, nil
}

// narrate asks the model to put the evidence into sentences. It is handed the evidence
// and nothing else; whatever comes back is checked before anyone sees it.
func (a *Agent) narrate(ctx context.Context, org string, b agent.Bundle) string {
	if a.Planner == nil || b.Empty != "" {
		return ""
	}
	resp, err := a.Planner.Complete(ctx, gateway.Request{
		Purpose: "agent_narrate", OrgID: org, System: agent.NarrationPrompt,
		Messages: []gateway.Message{{Role: "user", Content: agent.Evidence(b)}},
	})
	if err != nil {
		a.Logger.WarnContext(ctx, "agent narration skipped", "org_id", org, "error", err)
		return ""
	}
	return resp.Text
}
