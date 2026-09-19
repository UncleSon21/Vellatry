package engine

import (
	"math"
	"sort"
	"time"
)

// Purpose of a paid answer.
type Purpose string

const (
	PurposeScreen  Purpose = "screen"
	PurposeConfirm Purpose = "confirm"
	PurposeTrack   Purpose = "track"
	PurposeCheck   Purpose = "check" // user pressed "Check now"
)

// TaskSpec is one answer to request.
type TaskSpec struct {
	PromptID string
	Engine   string
	Purpose  Purpose
	Round    int
}

// OpenCell is a cell with work left in its round.
type OpenCell struct {
	PromptID string
	Engine   string
	Round    int
	Phase    Phase
	Answers  int
	Pending  int // tasks requested but not yet collected
}

// Candidate is a prompt that has never been screened.
type Candidate struct {
	PromptID    string
	TopicDemand int // monthly searches of its topic; 0 when unknown
	CreatedAt   time.Time
}

// Due is a tracked prompt whose weekly refresh is due.
type Due struct {
	PromptID  string
	NextRound int
}

// PlanInput is everything the planner needs for one tenant.
type PlanInput struct {
	Budget        int     // answers still allowed today
	DiscoverShare float64 // share of the free budget for discovery vs tracking
	Engines       []string
	Open          []OpenCell
	Due           []Due
	Candidates    []Candidate
}

// Plan decides which answers to request, never exceeding the budget:
//
//  1. Finish open cells first (closest to settling first): work already paid for is
//     worthless until it settles.
//  2. Split what remains between tracking refreshes and discovery by DiscoverShare.
//  3. Allocate whole prompts only (every engine, full depth), so no cell starts
//     without the budget to reach a decision.
//  4. Budget one side cannot use flows to the other.
func Plan(in PlanInput) []TaskSpec {
	budget := in.Budget
	var out []TaskSpec

	open := append([]OpenCell(nil), in.Open...)
	sort.SliceStable(open, func(i, j int) bool { return open[i].Answers > open[j].Answers })
	for _, c := range open {
		need := Target(c.Phase) - c.Answers - c.Pending
		purpose := PurposeConfirm
		if c.Phase == Screening {
			purpose = PurposeScreen
		}
		for ; need > 0 && budget > 0; need-- {
			out = append(out, TaskSpec{PromptID: c.PromptID, Engine: c.Engine, Purpose: purpose, Round: c.Round})
			budget--
		}
	}
	if budget <= 0 || len(in.Engines) == 0 {
		return out
	}

	share := math.Min(math.Max(in.DiscoverShare, 0), 1)
	discoverBudget := int(math.Round(float64(budget) * share))
	trackBudget := budget - discoverBudget

	cands := append([]Candidate(nil), in.Candidates...)
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].TopicDemand != cands[j].TopicDemand {
			return cands[i].TopicDemand > cands[j].TopicDemand
		}
		return cands[i].CreatedAt.Before(cands[j].CreatedAt)
	})

	perTrack := ConfirmAttempts * len(in.Engines)
	perDiscover := ScreenAttempts * len(in.Engines)
	due := in.Due

	takeDue := func(b int) int {
		for len(due) > 0 && b >= perTrack {
			d := due[0]
			due = due[1:]
			for _, e := range in.Engines {
				for i := 0; i < ConfirmAttempts; i++ {
					out = append(out, TaskSpec{PromptID: d.PromptID, Engine: e, Purpose: PurposeTrack, Round: d.NextRound})
				}
			}
			b -= perTrack
		}
		return b
	}
	takeCands := func(b int) int {
		for len(cands) > 0 && b >= perDiscover {
			c := cands[0]
			cands = cands[1:]
			for _, e := range in.Engines {
				for i := 0; i < ScreenAttempts; i++ {
					out = append(out, TaskSpec{PromptID: c.PromptID, Engine: e, Purpose: PurposeScreen, Round: 1})
				}
			}
			b -= perDiscover
		}
		return b
	}

	leftTrack := takeDue(trackBudget)
	leftDiscover := takeCands(discoverBudget)
	// Unused budget flows across.
	leftDiscover = takeCands(leftDiscover + leftTrack)
	takeDue(leftDiscover)
	return out
}
