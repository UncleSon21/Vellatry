package judge

import "encoding/json"

// Score10 maps a 1-5 label onto the 1-10 display scale: 1, 3.25, 5.5, 7.75, 10.
func Score10(label int) float64 { return 1 + float64(label-1)*2.25 }

// SentimentScore is the Sentiment family on the 1-10 scale: the mean of tone,
// comparative framing, and unprompted criticism (present scores 1, absent 10).
func SentimentScore(r Result) float64 {
	crit := 10.0
	if r.UnpromptedCriticism.Present {
		crit = 1
	}
	return (Score10(r.Tone.Label) + Score10(r.ComparativeFraming.Label) + crit) / 3
}

// ConfidenceScore is the Confidence family's judged dimensions on the 1-10 scale.
func ConfidenceScore(r Result) float64 {
	return mean(r.Hedging.Label, r.Specificity.Label, r.Consistency.Label, r.Directness.Label)
}

// CompletenessScore is the Completeness family on the 1-10 scale.
func CompletenessScore(r Result) float64 {
	return mean(r.QuestionCoverage.Label, r.Depth.Label, r.EvidenceQuality.Label, r.Recency.Label)
}

func mean(labels ...int) float64 {
	s := 0.0
	for _, l := range labels {
		s += Score10(l)
	}
	return s / float64(len(labels))
}

// FramingNegative is the sentiment signal the gap matrix uses: the brand is present
// but framed unfavourably.
func FramingNegative(r Result) bool {
	return r.Tone.Label <= 2 || r.ComparativeFraming.Label <= 2 || r.UnpromptedCriticism.Present ||
		r.Role.Value == "compared_unfavourably" || r.Role.Value == "warned_against"
}

// DifferentiatorGap is true when the brand declared differentiators and the answer
// supports none of them.
func DifferentiatorGap(r Result, declared int) bool {
	if declared == 0 {
		return false
	}
	for _, c := range r.Differentiators {
		if c.Supported {
			return false
		}
	}
	return true
}

// Row is one stored judgment.
type Row struct {
	Dimension string
	Label     *int
	Score     *float64
	Value     json.RawMessage
	Evidence  string
}

// Rows flattens r into one row per dimension for the judgments table.
func Rows(r Result) []Row {
	var rows []Row
	for name, s := range r.scaled() {
		label, score := s.Label, Score10(s.Label)
		rows = append(rows, Row{Dimension: name, Label: &label, Score: &score, Evidence: s.Evidence})
	}
	crit, _ := json.Marshal(r.UnpromptedCriticism.Present)
	role, _ := json.Marshal(r.Role.Value)
	flags, _ := json.Marshal(map[string]bool{"uncertainty": r.Uncertainty, "contradiction": r.Contradiction})
	rows = append(rows,
		Row{Dimension: "unprompted_criticism", Value: crit, Evidence: r.UnpromptedCriticism.Evidence},
		Row{Dimension: "role", Value: role, Evidence: r.Role.Evidence},
		Row{Dimension: "flags", Value: flags},
	)
	if len(r.Differentiators) > 0 {
		claims, _ := json.Marshal(r.Differentiators)
		rows = append(rows, Row{Dimension: "differentiators", Value: claims})
	}
	for _, fam := range []struct {
		name  string
		score float64
	}{{"sentiment", SentimentScore(r)}, {"confidence", ConfidenceScore(r)}, {"completeness", CompletenessScore(r)}} {
		s := fam.score
		rows = append(rows, Row{Dimension: fam.name, Score: &s})
	}
	return rows
}
