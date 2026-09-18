// Package gap turns per-answer signals into gap verdicts and mention-rate bands.
package gap

// Signals are the deterministic facts about one answer.
type Signals struct {
	BrandMentioned      bool
	BrandCited          bool
	SourcesExist        bool
	CompetitorMentioned bool
	CompetitorCited     bool
	FramingNegative     bool // from the judge; always false when the brand is absent
}

// Verdict is the gap classification of one answer.
type Verdict struct {
	Visibility   bool
	Displacement bool
	Sentiment    bool
}

// Classify applies the gap matrix to one answer.
//
//   - Brand named: never a visibility or displacement gap; a sentiment gap when framed negatively.
//   - Brand absent, no competitor named: a visibility gap only when a competitor's site is
//     cited and the brand's is not (nobody is winning the slot otherwise).
//   - Brand absent, competitor named: a displacement gap, plus a visibility gap unless the
//     brand is at least cited as a source.
func Classify(s Signals) Verdict {
	if s.BrandMentioned {
		return Verdict{Sentiment: s.FramingNegative}
	}
	if !s.CompetitorMentioned {
		return Verdict{Visibility: s.CompetitorCited && !s.BrandCited}
	}
	return Verdict{
		Visibility:   !s.SourcesExist || !s.BrandCited,
		Displacement: true,
	}
}

// Band is the visibility band of one prompt on one engine across attempts.
type Band string

const (
	Insufficient Band = "insufficient" // too few successful attempts for a verdict
	Blindspot    Band = "blindspot"
	Weak         Band = "weak"
	Visible      Band = "visible"
)

// MinSuccessful is the minimum number of successful attempts before a band is given.
const MinSuccessful = 3

// MentionBand bands the absolute number of attempts that mentioned the brand, out of
// the 5 attempts a confirmed cell runs: 0-1 blindspot, 2-3 weak, 4-5 visible.
func MentionBand(mentions, successful int) Band {
	switch {
	case successful < MinSuccessful:
		return Insufficient
	case mentions <= 1:
		return Blindspot
	case mentions <= 3:
		return Weak
	default:
		return Visible
	}
}
