// Package promptgen turns topics and engine fan-out queries into candidate prompts
// without a model: fixed intent templates for a topic, and the searches the AI engines
// themselves ran (fan-out queries), which are how real answers get grounded.
package promptgen

import (
	"strings"
	"unicode"
)

// Candidate is a prompt to screen.
type Candidate struct {
	Text   string
	Source string // "template" or "fan_out"
}

// FromTopic returns template prompts for a topic, phrased the way buyers ask AI
// assistants. location is a place name such as "Australia".
func FromTopic(topic, location string) []Candidate {
	topic = clean(topic)
	if topic == "" {
		return nil
	}
	in := ""
	if location = clean(location); location != "" {
		in = " in " + location
	}
	templates := []string{
		"What is the best " + topic + in + "?",
		"Which " + topic + " would you recommend for a small business" + in + "?",
		"How do I choose a " + topic + "?",
		"What are the top " + topic + " options" + in + " and how do they compare?",
	}
	out := make([]Candidate, len(templates))
	for i, t := range templates {
		out[i] = Candidate{Text: t, Source: "template"}
	}
	return out
}

// FromFanOut turns fan-out queries into prompts, skipping ones too short to be a real
// question, ones already known (case- and punctuation-insensitive), and duplicates.
// At most limit are returned.
func FromFanOut(queries []string, known []string, limit int) []Candidate {
	seen := map[string]bool{}
	for _, k := range known {
		seen[Key(k)] = true
	}
	var out []Candidate
	for _, q := range queries {
		q = clean(q)
		if len(strings.Fields(q)) < 3 {
			continue
		}
		k := Key(q)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, Candidate{Text: asQuestion(q), Source: "fan_out"})
		if len(out) == limit {
			break
		}
	}
	return out
}

// Key normalises a prompt for duplicate detection: lower case, letters and digits only,
// single spaces.
func Key(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func asQuestion(q string) string {
	q = strings.TrimRight(q, " .?!")
	if q == "" {
		return q
	}
	r := []rune(q)
	r[0] = unicode.ToUpper(r[0])
	return string(r) + "?"
}
