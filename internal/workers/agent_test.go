package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/agent"
	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/budget"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// scriptedModel stands in for the model behind the gateway: it picks an analysis and
// writes sentences however the test says, including wrongly.
type scriptedModel struct {
	mu      sync.Mutex
	plan    func() (gateway.Response, error)
	narrate func() (gateway.Response, error)
	calls   []string // purposes, in order
}

func (m *scriptedModel) Complete(_ context.Context, req gateway.Request) (gateway.Response, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req.Purpose)
	plan, narrate := m.plan, m.narrate
	m.mu.Unlock()
	switch req.Purpose {
	case "agent_plan":
		return plan()
	case "agent_narrate":
		return narrate()
	}
	return gateway.Response{}, fmt.Errorf("unexpected purpose %q", req.Purpose)
}

func (m *scriptedModel) script(plan, narrate func() (gateway.Response, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plan, m.narrate = plan, narrate
}

func (m *scriptedModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func says(text string) func() (gateway.Response, error) {
	return func() (gateway.Response, error) { return gateway.Response{Text: text}, nil }
}

func picks(analysis, reason string) func() (gateway.Response, error) {
	b, _ := json.Marshal(map[string]string{"analysis": analysis, "reason": reason})
	return says(string(b))
}

func fails(err error) func() (gateway.Response, error) {
	return func() (gateway.Response, error) { return gateway.Response{}, err }
}

// agentOrg is an organisation with four weeks of stored visibility on ChatGPT, halving
// in the second fortnight, so the visibility analysis has real figures: 30.0% now, 60.0%
// before.
func agentOrg(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	org, brandID := testdb.NewOrg(t, pool, "Koala")
	today := time.Now().UTC().Truncate(24 * time.Hour)
	err := db.InTenant(context.Background(), pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := brand.AddCompetitor(ctx, tx, org, brandID, brand.Competitor{Name: "Ecosa", Domains: []string{"ecosa.com.au"}}); err != nil {
			return err
		}
		for i := 1; i <= 56; i++ {
			day, mentioned := today.AddDate(0, 0, -i), 12
			if i <= 28 {
				mentioned = 6
			}
			if _, err := tx.Exec(ctx, `INSERT INTO visibility_daily (org_id, day, engine, answers, present, mentioned, method_version)
				VALUES ($1, $2, 'chatgpt', 20, 20, $3, 'vis-1')`, org, day, mentioned); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO visibility_daily_entities (org_id, day, engine, entity, is_brand, answers, mentions)
				VALUES ($1, $2, 'chatgpt', 'Koala', true, $3, $3), ($1, $2, 'chatgpt', 'Ecosa', false, 10, 10)`, org, day, mentioned); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return org
}

// agentHarness asks questions the router was not sure about and runs the job that
// answers them, reading back what the team would see.
type agentHarness struct {
	t      *testing.T
	pool   *pgxpool.Pool
	org    string
	worker *agentAnswerWorker
}

func newAgentHarness(t *testing.T, planner Completer) agentHarness {
	pool := testdb.Pool(t)
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Pool: pool, Bus: events.NewBus(inserter, domainevents.Subscriptions()...), Logger: slog.Default(),
		Planner: planner, Now: time.Now}
	return agentHarness{t: t, pool: pool, org: agentOrg(t, pool), worker: &agentAnswerWorker{a: a}}
}

func (h agentHarness) tenant(fn func(ctx context.Context, tx pgx.Tx) error) {
	h.t.Helper()
	if err := db.InTenant(context.Background(), h.pool, h.org, fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h agentHarness) ask(question string) int64 {
	h.t.Helper()
	var id int64
	h.tenant(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = agent.Record(ctx, tx, h.org, "", question, agent.Context{}, agent.Route{}, "thinking")
		return err
	})
	return id
}

func (h agentHarness) work(id int64) error {
	return h.worker.Work(context.Background(), job(jobargs.AgentAnswer{OrgID: h.org, QuestionID: id}))
}

type shown struct {
	Status, Answer string
	Problems       []string
}

func (h agentHarness) shown(id int64) shown {
	h.t.Helper()
	var s shown
	var probs []byte
	h.tenant(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status, answer, problems FROM agent_questions WHERE id = $1`, id).Scan(&s.Status, &s.Answer, &probs)
	})
	if err := json.Unmarshal(probs, &s.Problems); err != nil {
		h.t.Fatal(err)
	}
	return s
}

// TestAgentNeverShowsAFigureTheModelMadeUp: the model chooses the analysis and words the
// answer; every number comes from the analysis, and wording that carries any other
// number is replaced by the code's own before anyone reads it.
func TestAgentNeverShowsAFigureTheModelMadeUp(t *testing.T) {
	model := &scriptedModel{}
	h := newAgentHarness(t, model)

	// The model's wording is kept when every figure in it is in the evidence.
	model.script(picks("visibility_change", "it asks how visibility moved"), says("AI visibility fell to 30.0% from 60.0%."))
	kept := h.ask("Talk me through our AI visibility lately")
	if err := h.work(kept); err != nil {
		t.Fatal(err)
	}
	if s := h.shown(kept); s.Status != "answered" || s.Answer != "AI visibility fell to 30.0% from 60.0%." || len(s.Problems) != 0 {
		t.Errorf("checked wording: %+v, want the model's sentence, answered, no problems", s)
	}

	// One figure the analysis did not compute and the whole wording is dropped: the team
	// reads the code's own sentences, and the made-up figure is recorded, not shown.
	model.script(picks("visibility_change", "it asks how visibility moved"),
		says("AI visibility fell to 30.0% from 60.0%, a 42.0% drop caused by Ecosa's sale."))
	invented := h.ask("Why is our AI visibility down?")
	if err := h.work(invented); err != nil {
		t.Fatal(err)
	}
	s := h.shown(invented)
	if s.Status != "answered" || strings.Contains(s.Answer, "42") || !strings.Contains(s.Answer, "30.0%") {
		t.Errorf("wording with an invented figure was shown: %+v", s)
	}
	if strings.Join(s.Problems, ",") != "42.0" {
		t.Errorf("problems recorded = %v, want the invented figure", s.Problems)
	}
	var checked string
	h.tenant(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT payload->>'checked' FROM events WHERE kind = $1 AND subject_id = $2`,
			domainevents.AgentAnswered, fmt.Sprint(invented)).Scan(&checked)
	})
	if checked != "false" {
		t.Errorf("the answered event says checked = %q, want false", checked)
	}

	// The model failing to word it is not the team's problem: the code's own answer.
	model.script(picks("visibility_change", "it asks how visibility moved"), fails(errors.New("gateway: 529 overloaded")))
	unworded := h.ask("How has our AI visibility moved?")
	if err := h.work(unworded); err != nil {
		t.Fatal(err)
	}
	if s := h.shown(unworded); s.Status != "answered" || !strings.Contains(s.Answer, "30.0%") {
		t.Errorf("without narration: %+v, want the code's own answer", s)
	}

	// Any job may run twice: an answered question is not asked of the model again.
	calls := model.count()
	if err := h.work(kept); err != nil {
		t.Fatal(err)
	}
	if model.count() != calls || h.shown(kept).Answer != "AI visibility fell to 30.0% from 60.0%." {
		t.Error("running the job again asked the model again or changed the answer")
	}
}

// TestAgentRefusesWhatTheModelCannotChoose: the model may only pick from the catalogue.
// A name it invents, a reply that is not the agreed shape and a model that is down all
// end in a refusal the team can read, never a guess and never a retry loop.
func TestAgentRefusesWhatTheModelCannotChoose(t *testing.T) {
	model := &scriptedModel{}
	h := newAgentHarness(t, model)
	cases := []struct {
		name string
		plan func() (gateway.Response, error)
		want string
	}{
		{name: "an analysis the model invents", plan: picks("churn_forecast", "it asks about churn"), want: "I cannot answer that: it asks about churn."},
		{name: "a reply that is not the agreed JSON", plan: says("Sure, I would use visibility_change."), want: "I could not work out which analysis answers that."},
		{name: "a model that is out of budget", plan: fails(fmt.Errorf("gateway: %w", budget.ErrExceeded)), want: "I could not work out which analysis answers that."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			model.script(c.plan, fails(errors.New("narration must not be asked for")))
			id := h.ask("Will our customers churn next quarter?")
			if err := h.work(id); err != nil {
				t.Fatalf("the job returned %v, want it to finish with a refusal", err)
			}
			if s := h.shown(id); s.Status != "refused" || !strings.HasPrefix(s.Answer, c.want) {
				t.Errorf("shown %+v, want refused, starting %q", s, c.want)
			}
		})
	}
	for _, purpose := range model.calls {
		if purpose != "agent_plan" {
			t.Errorf("the model was asked to %s after it could not choose", purpose)
		}
	}

	t.Run("without a model, only what the router knows", func(t *testing.T) {
		h := newAgentHarness(t, nil)
		id := h.ask("Will our customers churn next quarter?")
		if err := h.work(id); err != nil {
			t.Fatal(err)
		}
		if s := h.shown(id); s.Status != "refused" || !strings.HasPrefix(s.Answer, "I can only answer the questions in the list") {
			t.Errorf("shown %+v", s)
		}
	})
}
