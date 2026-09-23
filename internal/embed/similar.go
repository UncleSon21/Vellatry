package embed

import "math"

// Cosine is the similarity of two vectors, from -1 to 1. The service returns unit
// vectors, but it normalises anyway: a vector read back from a database may have been
// written by an older version, and a silently wrong score is worse than a slow one.
func Cosine(a, b Vector) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// SameTopic is the similarity at or above which two short topic names are worth putting
// in front of a person as "these look like the same topic". It is a suggestion, never an
// automatic merge, so a false positive costs a glance.
//
// Measured on BAAI/bge-small-en-v1.5 (ml/embed), the model this number belongs to:
//
//	the same thing said twice   mattresses / mattress            0.878
//	                            sofa beds / sofabeds             0.915
//	                            best mattress for back pain /
//	                            mattress for back pain           0.952
//	                            hybrid mattresses / hybrid
//	                            mattress                         0.973
//	related but not the same    mattresses / hybrid mattresses   0.833
//	                            sofa beds / bed bases            0.793
//	                            mattresses / pillows             0.750
//	unrelated                   mattresses / business energy     0.554
//
// The gap between 0.833 and 0.878 is where the line goes. Re-measure it if the model
// changes: these numbers do not transfer.
const SameTopic float32 = 0.85

// Candidate is something already embedded, to compare against.
type Candidate struct {
	ID     string
	Model  string
	Vector Vector
}

// Match is one candidate that was close enough, with its score.
type Match struct {
	ID    string
	Score float32
}

// Similar returns the candidates closest to v, best first, keeping only those at or
// above min and at most limit of them. Candidates from another model are skipped: their
// numbers would look like scores and mean nothing.
func Similar(model string, v Vector, candidates []Candidate, min float32, limit int) []Match {
	var out []Match
	for _, c := range candidates {
		if c.Model != model {
			continue
		}
		if score := Cosine(v, c.Vector); score >= min {
			out = append(out, Match{ID: c.ID, Score: score})
		}
	}
	// Small n (topics in one organisation), so an insertion sort keeps it obvious.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Score > out[j-1].Score; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
