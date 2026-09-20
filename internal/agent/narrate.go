package agent

import (
	"regexp"
	"strconv"
	"strings"
)

// Narrate writes the answer from the evidence, with no model involved: the headline the
// code chose, then the numbers behind it. This is what most questions get.
func Narrate(b Bundle) string {
	if b.Empty != "" {
		return "There is nothing to answer with: " + b.Empty
	}
	var sb strings.Builder
	sb.WriteString(b.Headline)
	shown := 0
	for _, f := range b.Facts {
		if shown == 5 {
			break
		}
		if f.Label == "" || f.Value == "" {
			continue
		}
		if shown == 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("\n" + f.Label + ": " + f.Value)
		if f.Note != "" {
			sb.WriteString(" (" + f.Note + ")")
		}
		sb.WriteString(".")
		shown++
	}
	for _, t := range b.Tables {
		if len(t.Rows) > 0 && t.Title != "" {
			sb.WriteString("\n\n" + t.Title + " is below.")
			break
		}
	}
	return sb.String()
}

var numberRE = regexp.MustCompile(`\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?`)

func canonical(n string) string {
	f, err := strconv.ParseFloat(strings.ReplaceAll(n, ",", ""), 64)
	if err != nil {
		return n
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Figures is every number the evidence contains, in canonical form. A sentence may use
// these and nothing else.
func Figures(b Bundle) map[string]bool {
	out := map[string]bool{}
	add := func(s string) {
		for _, n := range numberRE.FindAllString(s, -1) {
			out[canonical(n)] = true
		}
	}
	add(b.Headline)
	add(b.From)
	add(b.To)
	for _, f := range b.Facts {
		add(f.Value)
		add(f.Note)
		add(f.Label)
		out[canonical(strconv.FormatFloat(f.Raw, 'f', -1, 64))] = true
		out[canonical(strconv.FormatFloat(round1(f.Raw), 'f', -1, 64))] = true
	}
	for _, t := range b.Tables {
		add(t.Title)
		add(t.Caption)
		for _, row := range t.Rows {
			for _, cell := range row {
				add(cell)
			}
		}
	}
	return out
}

// Unverified returns the numbers in text that the evidence does not contain. The prior
// system's agent was wrong about a quarter of its factual claims, and the errors were
// arithmetic: a model may repeat a number here, never work one out.
func Unverified(text string, allowed map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range numberRE.FindAllString(text, -1) {
		c := canonical(n)
		if !allowed[c] && !seen[c] {
			seen[c] = true
			out = append(out, n)
		}
	}
	return out
}

// Checked returns narration a model wrote if every figure in it is in the evidence, and
// the code's own narration if not, with what was wrong.
func Checked(written string, b Bundle) (text string, problems []string) {
	written = strings.TrimSpace(written)
	if written == "" {
		return Narrate(b), nil
	}
	if bad := Unverified(written, Figures(b)); len(bad) > 0 {
		return Narrate(b), bad
	}
	return written, nil
}

// NarrationPrompt is the instruction for the model when one is used to write the answer.
// It is handed the evidence and nothing else.
const NarrationPrompt = `You answer a marketing team's question about their own data, in two or three short sentences.

Rules:
- Use only the figures in the evidence below, written exactly as they appear there. Do not calculate anything, including differences, totals or percentages.
- Lead with the answer. No preamble, no headings, no bullet points, no emoji.
- Plain Australian English. Say "AI answers", not "LLM responses".
- If the evidence does not answer the question, say so in one sentence.`

// Evidence writes the bundle as the text a model is allowed to read.
func Evidence(b Bundle) string {
	var sb strings.Builder
	sb.WriteString("Question: " + b.Question + "\n")
	if b.From != "" {
		sb.WriteString("Period: " + b.From + " to " + b.To + "\n")
	}
	if b.Empty != "" {
		sb.WriteString("Nothing to report: " + b.Empty + "\n")
		return sb.String()
	}
	sb.WriteString("Headline (already decided by code): " + b.Headline + "\n\nFigures:\n")
	for _, f := range b.Facts {
		sb.WriteString("- " + f.Label + ": " + f.Value)
		if f.Note != "" {
			sb.WriteString(" (" + f.Note + ")")
		}
		sb.WriteString("\n")
	}
	for _, t := range b.Tables {
		sb.WriteString("\n" + t.Title + ":\n| " + strings.Join(t.Head, " | ") + " |\n")
		for i, row := range t.Rows {
			if i == 8 {
				break
			}
			sb.WriteString("| " + strings.Join(row, " | ") + " |\n")
		}
	}
	return sb.String()
}
