package notebook

import (
	"strings"
	"unicode"
)

// Chunking is deterministic and does not call a model. A chunk is what an answer cites,
// so the team has to be able to read one and recognise where it came from: splitting on
// the document's own paragraphs does that, and a model deciding where a document breaks
// would be both paid and unrepeatable.

const (
	// TargetChunk is the size a chunk aims for. Small enough that a citation points at a
	// passage somebody can read, large enough to hold a whole argument.
	TargetChunk = 900
	// MaxChunk is the hard limit. The embedding model stops reading somewhere past here,
	// so a longer chunk would be retrieved on its first half alone.
	MaxChunk = 1600
	// Overlap is how much of the previous chunk starts the next one, so a sentence that
	// answers the question is not split in half by a paragraph break.
	Overlap = 200
)

// Chunk is one passage of a source.
type Chunk struct {
	Seq  int
	Text string
}

// Split breaks a document into chunks on its own paragraphs: paragraphs are packed
// together up to TargetChunk, one too long for MaxChunk is split on sentences, and each
// chunk after the first starts with the tail of the one before.
func Split(text string) []Chunk {
	var out []Chunk
	var cur strings.Builder
	var tail string

	flush := func() {
		body := strings.TrimSpace(cur.String())
		cur.Reset()
		if body == "" {
			return
		}
		out = append(out, Chunk{Seq: len(out), Text: body})
		tail = lastSentence(body)
	}
	add := func(p string) {
		if cur.Len() == 0 && tail != "" && len(out) > 0 {
			cur.WriteString(tail + "\n\n")
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(p)
		if cur.Len() >= TargetChunk {
			flush()
		}
	}

	for _, p := range paragraphs(text) {
		if len(p) <= MaxChunk {
			add(p)
			continue
		}
		flush() // an oversize paragraph starts its own chunk rather than ending someone else's
		for _, piece := range splitLong(p) {
			add(piece)
		}
	}
	flush()
	return out
}

// paragraphs splits on blank lines and squeezes the whitespace inside each, so a
// document that arrived with hard-wrapped lines, tabs or Windows line endings chunks the
// same way as one that did not.
func paragraphs(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var out []string
	for _, block := range strings.Split(text, "\n\n") {
		var lines []string
		for _, line := range strings.Split(block, "\n") {
			if line = strings.Join(strings.Fields(line), " "); line != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) > 0 {
			out = append(out, strings.Join(lines, "\n"))
		}
	}
	return out
}

// splitLong cuts a paragraph longer than MaxChunk at sentence ends, and mid-sentence
// only when one sentence is itself too long (a table pasted as prose, a minified line).
func splitLong(p string) []string {
	var out []string
	var cur string
	for _, s := range sentences(p) {
		for len(s) > MaxChunk {
			if cur != "" {
				out, cur = append(out, cur), ""
			}
			out = append(out, strings.TrimSpace(s[:MaxChunk]))
			s = s[MaxChunk:]
		}
		if cur != "" && len(cur)+1+len(s) > TargetChunk {
			out, cur = append(out, cur), ""
		}
		if cur == "" {
			cur = s
		} else {
			cur += " " + s
		}
	}
	if cur = strings.TrimSpace(cur); cur != "" {
		out = append(out, cur)
	}
	return out
}

// sentences splits on a full stop, question mark or exclamation followed by a space and
// a capital. It is a heuristic and only decides where a long paragraph breaks, so an
// abbreviation it gets wrong costs a slightly odd boundary and nothing else.
func sentences(p string) []string {
	var out []string
	start := 0
	runes := []rune(p)
	for i := 0; i < len(runes)-1; i++ {
		if runes[i] != '.' && runes[i] != '?' && runes[i] != '!' {
			continue
		}
		j := i + 1
		for j < len(runes) && runes[j] == ' ' {
			j++
		}
		if j == i+1 || j >= len(runes) || !unicode.IsUpper(runes[j]) {
			continue
		}
		if s := strings.TrimSpace(string(runes[start:j])); s != "" {
			out = append(out, s)
		}
		start, i = j, j-1
	}
	if s := strings.TrimSpace(string(runes[start:])); s != "" {
		out = append(out, s)
	}
	return out
}

// lastSentence returns the tail of a chunk to repeat at the start of the next, up to
// Overlap characters.
func lastSentence(body string) string {
	ss := sentences(body)
	if len(ss) == 0 {
		return ""
	}
	last := ss[len(ss)-1]
	if len(last) > Overlap {
		last = strings.TrimSpace(last[len(last)-Overlap:])
	}
	return last
}
