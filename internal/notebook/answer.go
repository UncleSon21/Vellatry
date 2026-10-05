package notebook

import (
	"regexp"
	"strconv"
	"strings"
)

// What the notebook promises is not that the answer is right, which no one can promise.
// It is that every sentence of it came from a passage of a document the team put there,
// and that the passage is one click away. That promise is kept here, by code, after the
// model has written: a sentence with no citation, a citation to a passage that was never
// retrieved, or a figure that is not in the passage it cites, is dropped before anyone
// reads it.
//
// Dropping rather than repairing, for the reason the report bot does it: a repaired
// sentence is one nobody wrote.

// WhenTheSourcesCannotSay is the answer when nothing survives. It says whose limit it
// is, and does not pretend the question was a bad one.
const WhenTheSourcesCannotSay = "The sources in this notebook do not answer that. Add a document that covers it, or ask in the chat on another page where Vellatry's own measurements can answer."

// WhenThereAreNoSources is the answer before anything has been added.
const WhenThereAreNoSources = "This notebook has no sources yet. Add a page or paste some text, and questions will be answered from it."

// AnswerPrompt is the instruction. It is handed the passages and the question, and no
// other data at all: there is nothing else for the model to reach for.
const AnswerPrompt = `You answer a marketing team's question using only the numbered passages below, which come from documents they chose.

Rules:
- Use only what the passages say. If they do not answer the question, say so in one sentence and stop.
- End every sentence with the passage it came from, in square brackets before the full stop, like this [2]. A sentence may cite more than one: [1][3].
- Never write a sentence you cannot cite. Never cite a passage that does not say it.
- Quote figures exactly as the passage writes them. Do not calculate anything, including totals, differences or percentages.
- Four sentences at most. Plain Australian English, no preamble, no headings, no bullet points, no emoji.`

// Context writes the passages as the model reads them.
func Context(ps []Passage) string {
	var b strings.Builder
	for i, p := range ps {
		b.WriteString("[" + strconv.Itoa(i+1) + "] from \"" + p.Title + "\"")
		if p.URL != "" {
			b.WriteString(" (" + p.URL + ")")
		}
		b.WriteString("\n" + p.Text + "\n\n")
	}
	return strings.TrimSpace(b.String())
}

var (
	markerRE = regexp.MustCompile(`\[(\d{1,2})\]`)
	numberRE = regexp.MustCompile(`\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?`)
)

func canonical(n string) string {
	f, err := strconv.ParseFloat(strings.ReplaceAll(n, ",", ""), 64)
	if err != nil {
		return n
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func figuresIn(text string) map[string]bool {
	out := map[string]bool{}
	for _, n := range numberRE.FindAllString(text, -1) {
		out[canonical(n)] = true
	}
	return out
}

// Ground keeps the sentences the passages can back, renumbers their citations from one,
// and returns the passages those citations point at.
//
// The renumbering matters: after a sentence is dropped its passage may be unused, and an
// answer whose markers are [1] and [4] with only two citations under it reads like the
// other two were hidden. What the reader sees is [1] and [2], and both are there.
func Ground(written string, ps []Passage) (answer string, cites []Citation, dropped int) {
	var kept []string
	marker := map[int]int{} // retrieved index (1-based) -> the number the reader sees

	for _, s := range sentences(strings.TrimSpace(written)) {
		used, ok := citedBy(s, len(ps))
		if !ok {
			dropped++
			continue
		}
		if !figuresBacked(s, used, ps) {
			dropped++
			continue
		}
		for _, i := range used {
			if _, seen := marker[i]; !seen {
				marker[i] = len(marker) + 1
				cites = append(cites, Citation{
					Marker: marker[i], ChunkID: ps[i-1].ID, Source: ps[i-1].Source,
					Title: ps[i-1].Title, URL: ps[i-1].URL, Seq: ps[i-1].Seq, Text: ps[i-1].Text,
				})
			}
		}
		kept = append(kept, markerRE.ReplaceAllStringFunc(s, func(m string) string {
			i, _ := strconv.Atoi(m[1 : len(m)-1])
			return "[" + strconv.Itoa(marker[i]) + "]"
		}))
	}
	if len(kept) == 0 {
		return "", nil, dropped
	}
	return strings.Join(kept, " "), cites, dropped
}

// citedBy returns the passages a sentence cites, and whether it may be kept at all. A
// sentence with no citation is one the model wrote from somewhere else; a citation past
// the end of the list is one it invented.
func citedBy(sentence string, n int) ([]int, bool) {
	ms := markerRE.FindAllStringSubmatch(sentence, -1)
	if len(ms) == 0 {
		return nil, false
	}
	seen := map[int]bool{}
	var out []int
	for _, m := range ms {
		i, err := strconv.Atoi(m[1])
		if err != nil || i < 1 || i > n {
			return nil, false
		}
		if !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	return out, true
}

// figuresBacked reports whether every number in the sentence appears in a passage it
// cites. The markers themselves are numbers, so they come out first.
func figuresBacked(sentence string, used []int, ps []Passage) bool {
	claimed := figuresIn(markerRE.ReplaceAllString(sentence, " "))
	if len(claimed) == 0 {
		return true
	}
	allowed := map[string]bool{}
	for _, i := range used {
		for n := range figuresIn(ps[i-1].Text) {
			allowed[n] = true
		}
	}
	for n := range claimed {
		if !allowed[n] {
			return false
		}
	}
	return true
}
