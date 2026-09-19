// Package engine holds the Visibility engine's decisions as pure functions: how a
// prompt x engine cell moves from screening to confirming to settled, what blindspots
// its answers imply, and which paid answers to request within a tenant's daily budget.
//
// The cell's counts are always recomputed from the answers stored for its round, never
// incremented, so every decision here is idempotent under job retries.
package engine

import "github.com/UncleSon21/vellatry/internal/visibility/gap"

// MethodVersion is stamped on every stored answer and rollup. Bump it when a rule in
// this package or in internal/visibility/gap changes, so trend lines can mark the change.
const MethodVersion = "vis-1"

const (
	// ScreenAttempts is how many answers per engine a new prompt gets before deciding.
	ScreenAttempts = 2
	// ConfirmAttempts is the full depth a band is computed on.
	ConfirmAttempts = 5
)

// Phase of a cell within a round.
type Phase string

const (
	Screening  Phase = "screening"
	Confirming Phase = "confirming"
	Settled    Phase = "settled"
)

// Cell is one prompt on one engine for the current round.
type Cell struct {
	Phase         Phase
	Answers       int // answers collected (attempts that returned)
	Present       int // answers where the engine produced an AI answer
	Mentions      int // present answers that mention the brand
	Displacements int // present answers classified as displacement
}

// Advance moves a cell to the phase its counts call for.
//
//   - 2 screening answers that all mention the brand settle as visible: no need to pay
//     for three more.
//   - 2 screening answers where the engine never produced an AI answer settle as
//     "no answer" (typical for AI Overview on some queries).
//   - Anything else escalates to confirmation, and settles at 5 answers.
func Advance(c Cell) Cell {
	switch {
	case c.Phase == Settled:
	case c.Answers >= ConfirmAttempts:
		c.Phase = Settled
	case c.Answers >= ScreenAttempts && c.Phase == Screening:
		if c.Present == 0 || (c.Present == c.Answers && c.Mentions == c.Present) {
			c.Phase = Settled
		} else {
			c.Phase = Confirming
		}
	}
	return c
}

// Target is the number of answers the cell's phase needs in total.
func Target(p Phase) int {
	if p == Screening {
		return ScreenAttempts
	}
	return ConfirmAttempts
}

// NoAnswer marks a settled cell where the engine showed no AI answer at all.
const NoAnswer gap.Band = "no_answer"

// Band returns the cell's visibility band.
func Band(c Cell) gap.Band {
	if c.Phase == Settled && c.Present == 0 {
		return NoAnswer
	}
	if c.Phase == Settled && c.Present == c.Answers && c.Answers == ScreenAttempts && c.Mentions == c.Present {
		return gap.Visible // screened visible: 2 of 2
	}
	return gap.MentionBand(c.Mentions, c.Present)
}

// Finding is how sure we are about a blindspot.
type Finding string

const (
	None        Finding = ""
	Provisional Finding = "provisional" // seen while screening or confirming
	Confirmed   Finding = "confirmed"   // holds at full depth
)

// Findings derives the visibility and displacement blindspots a cell implies.
func Findings(c Cell) (visibility, displacement Finding) {
	switch {
	case c.Phase == Settled && Band(c) == gap.Blindspot:
		visibility = Confirmed
	case c.Phase == Confirming && c.Present > 0 && c.Mentions == 0:
		visibility = Provisional
	}
	switch {
	case c.Phase == Settled && c.Present >= gap.MinSuccessful && 2*c.Displacements > c.Present:
		displacement = Confirmed
	case c.Phase == Confirming && c.Present >= 2 && c.Displacements == c.Present:
		displacement = Provisional
	}
	return visibility, displacement
}
