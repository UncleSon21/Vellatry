package engine

import (
	"testing"
	"time"

	"github.com/UncleSon21/vellatry/internal/visibility/gap"
)

func TestAdvance(t *testing.T) {
	tests := []struct {
		name string
		in   Cell
		want Phase
	}{
		{"one screening answer stays screening", Cell{Phase: Screening, Answers: 1, Present: 1, Mentions: 1}, Screening},
		{"2 of 2 mentioned settles early", Cell{Phase: Screening, Answers: 2, Present: 2, Mentions: 2}, Settled},
		{"1 of 2 escalates", Cell{Phase: Screening, Answers: 2, Present: 2, Mentions: 1}, Confirming},
		{"0 of 2 escalates", Cell{Phase: Screening, Answers: 2, Present: 2}, Confirming},
		{"no AI answer at all settles", Cell{Phase: Screening, Answers: 2}, Settled},
		{"one missing AI answer still escalates", Cell{Phase: Screening, Answers: 2, Present: 1, Mentions: 1}, Confirming},
		{"confirming settles at 5", Cell{Phase: Confirming, Answers: 5, Present: 5, Mentions: 1}, Settled},
		{"confirming waits below 5", Cell{Phase: Confirming, Answers: 4, Present: 4}, Confirming},
		{"settled stays settled", Cell{Phase: Settled, Answers: 2, Present: 2, Mentions: 2}, Settled},
	}
	for _, tt := range tests {
		if got := Advance(tt.in).Phase; got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestBandAndFindings(t *testing.T) {
	tests := []struct {
		name       string
		c          Cell
		band       gap.Band
		vis, displ Finding
	}{
		{"screened visible", Cell{Phase: Settled, Answers: 2, Present: 2, Mentions: 2}, gap.Visible, None, None},
		{"confirmed blindspot with displacement", Cell{Phase: Settled, Answers: 5, Present: 5, Mentions: 1, Displacements: 4}, gap.Blindspot, Confirmed, Confirmed},
		{"weak, no blindspot", Cell{Phase: Settled, Answers: 5, Present: 5, Mentions: 3, Displacements: 2}, gap.Weak, None, None},
		{"provisional while confirming", Cell{Phase: Confirming, Answers: 2, Present: 2, Mentions: 0, Displacements: 2}, gap.Insufficient, Provisional, Provisional},
		{"no AI answer is not a blindspot", Cell{Phase: Settled, Answers: 2}, NoAnswer, None, None},
		{"too few AI answers to judge", Cell{Phase: Settled, Answers: 5, Present: 2, Mentions: 0, Displacements: 2}, gap.Insufficient, None, None},
	}
	for _, tt := range tests {
		if got := Band(tt.c); got != tt.band {
			t.Errorf("%s: band %s, want %s", tt.name, got, tt.band)
		}
		v, d := Findings(tt.c)
		if v != tt.vis || d != tt.displ {
			t.Errorf("%s: findings (%q, %q), want (%q, %q)", tt.name, v, d, tt.vis, tt.displ)
		}
	}
}

func count(tasks []TaskSpec, p Purpose) int {
	n := 0
	for _, t := range tasks {
		if t.Purpose == p {
			n++
		}
	}
	return n
}

var engines = []string{"chatgpt", "gemini", "ai_overview"}

func TestPlanFinishesOpenCellsFirst(t *testing.T) {
	out := Plan(PlanInput{
		Budget:  4,
		Engines: engines,
		Open: []OpenCell{
			{PromptID: "a", Engine: "chatgpt", Round: 1, Phase: Confirming, Answers: 2, Pending: 1}, // needs 2
			{PromptID: "b", Engine: "gemini", Round: 1, Phase: Screening, Answers: 1},               // needs 1
		},
		Candidates: []Candidate{{PromptID: "c"}},
	})
	if len(out) != 3 || count(out, PurposeConfirm) != 2 || count(out, PurposeScreen) != 1 {
		t.Fatalf("got %+v", out)
	}
	// One unit left is too little for a whole candidate (3 engines x 2), so none starts.
	for _, task := range out {
		if task.PromptID == "c" {
			t.Errorf("started a candidate without budget to screen it on every engine")
		}
	}
}

func TestPlanNeverExceedsBudget(t *testing.T) {
	for budget := 0; budget < 80; budget++ {
		out := Plan(PlanInput{
			Budget: budget, DiscoverShare: 0.6, Engines: engines,
			Open:       []OpenCell{{PromptID: "o", Engine: "chatgpt", Phase: Confirming, Answers: 2}},
			Due:        []Due{{PromptID: "t1", NextRound: 2}, {PromptID: "t2", NextRound: 2}},
			Candidates: []Candidate{{PromptID: "c1"}, {PromptID: "c2"}, {PromptID: "c3"}, {PromptID: "c4"}},
		})
		if len(out) > budget {
			t.Fatalf("budget %d: planned %d", budget, len(out))
		}
	}
}

func TestPlanSplitsAndFlowsBudget(t *testing.T) {
	// 60 answers, 60% discovery: 36 discovery (6 prompts x 6) and 24 tracking (1 prompt x 15, 9 left).
	out := Plan(PlanInput{
		Budget: 60, DiscoverShare: 0.6, Engines: engines,
		Due:        []Due{{PromptID: "t1", NextRound: 3}, {PromptID: "t2", NextRound: 3}},
		Candidates: manyCandidates(10),
	})
	if got := count(out, PurposeTrack); got != 15 {
		t.Errorf("tracking answers = %d, want 15", got)
	}
	// Discovery gets its 36 plus the 9 tracking could not use: 45 -> 7 prompts (42).
	if got := count(out, PurposeScreen); got != 42 {
		t.Errorf("screening answers = %d, want 42", got)
	}
	for _, task := range out {
		if task.Purpose == PurposeTrack && task.Round != 3 {
			t.Errorf("tracked refresh in round %d, want 3", task.Round)
		}
	}

	// No candidates: all of it goes to tracking.
	out = Plan(PlanInput{Budget: 30, DiscoverShare: 0.6, Engines: engines, Due: []Due{{PromptID: "t1", NextRound: 2}, {PromptID: "t2", NextRound: 2}}})
	if got := count(out, PurposeTrack); got != 30 {
		t.Errorf("tracking with no candidates = %d, want 30", got)
	}
}

func TestPlanPrefersHighDemandCandidates(t *testing.T) {
	now := time.Now()
	out := Plan(PlanInput{
		Budget: 6, DiscoverShare: 1, Engines: engines,
		Candidates: []Candidate{
			{PromptID: "low", TopicDemand: 10, CreatedAt: now},
			{PromptID: "high", TopicDemand: 5000, CreatedAt: now},
		},
	})
	if len(out) != 6 || out[0].PromptID != "high" {
		t.Errorf("got %+v, want the high-demand prompt screened", out)
	}
}

func manyCandidates(n int) []Candidate {
	out := make([]Candidate, n)
	for i := range out {
		out[i] = Candidate{PromptID: string(rune('a' + i)), CreatedAt: time.Unix(int64(i), 0)}
	}
	return out
}
