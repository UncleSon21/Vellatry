package notebook

import (
	"strings"
	"testing"
)

func TestSplitFollowsTheDocument(t *testing.T) {
	doc := "Brand voice\n\n" +
		strings.Repeat("We write plainly and we never oversell. ", 20) + "\n\n" +
		strings.Repeat("Pricing starts at $49 a month. ", 20) + "\n\n" +
		"The end."
	chunks := Split(doc)
	if len(chunks) < 2 {
		t.Fatalf("a long document became %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if c.Seq != i {
			t.Errorf("chunk %d is numbered %d", i, c.Seq)
		}
		if len(c.Text) > MaxChunk+Overlap {
			t.Errorf("chunk %d is %d characters", i, len(c.Text))
		}
		if strings.TrimSpace(c.Text) != c.Text {
			t.Errorf("chunk %d has loose whitespace: %q", i, c.Text)
		}
	}
	// Every chunk after the first repeats the tail of the one before, so a sentence is
	// never only half-retrievable.
	for i := 1; i < len(chunks); i++ {
		tail := lastSentence(chunks[i-1].Text)
		if tail != "" && !strings.HasPrefix(chunks[i].Text, tail) {
			t.Errorf("chunk %d does not start with the previous tail %q", i, tail)
		}
	}
	// Splitting is deterministic: the same document always chunks the same way, or a
	// citation would point somewhere else after a re-read.
	again := Split(doc)
	if len(again) != len(chunks) {
		t.Fatal("the same document chunked differently the second time")
	}
	for i := range chunks {
		if again[i].Text != chunks[i].Text {
			t.Fatalf("chunk %d differs between runs", i)
		}
	}

	// One paragraph longer than the limit is split rather than stored whole.
	long := Split(strings.Repeat("This sentence is here to make the paragraph long. ", 120))
	if len(long) < 2 {
		t.Errorf("an oversize paragraph became %d chunk(s)", len(long))
	}
	for _, c := range long {
		if len(c.Text) > MaxChunk+Overlap {
			t.Errorf("a split chunk is still %d characters", len(c.Text))
		}
	}
	if got := Split("   \n\n  \n"); len(got) != 0 {
		t.Errorf("empty text became %d chunks", len(got))
	}
}

func TestReadDocuments(t *testing.T) {
	html := []byte(`<!doctype html><html><head><title>Pricing — Acme</title><style>body{color:red}</style></head>
		<body><nav><a href="/">Home</a></nav><h1>Plans</h1><p>Starter is $49 a month.</p>
		<p>Growth is $149 a month.</p><script>track()</script><footer>Acme Pty Ltd</footer></body></html>`)
	text, err := Read("text/html; charset=utf-8", html)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Plans", "Starter is $49 a month.", "Growth is $149 a month."} {
		if !strings.Contains(text, want) {
			t.Errorf("the page text is missing %q:\n%s", want, text)
		}
	}
	for _, absent := range []string{"track()", "color:red", "Home", "Acme Pty Ltd"} {
		if strings.Contains(text, absent) {
			t.Errorf("the page text contains %q, which repeats on every page of a site", absent)
		}
	}
	if got := TitleOf(html); got != "Pricing — Acme" {
		t.Errorf("title = %q", got)
	}

	// A server that mislabels an HTML page as plain text is still read as HTML.
	if text, err := Read("text/plain", html); err != nil || strings.Contains(text, "<h1>") {
		t.Errorf("a mislabelled page was not read as HTML: %v %q", err, text)
	}
	// Markdown and plain text arrive as they are.
	if text, err := Read("text/markdown", []byte("# Voice\n\nWe write plainly.")); err != nil || !strings.Contains(text, "# Voice") {
		t.Errorf("markdown = %q, %v", text, err)
	}
	// Anything that is not text is refused by name, not chunked into nonsense.
	if _, err := Read("application/pdf", []byte("%PDF-1.7\x00\x01binary")); err != ErrNotText {
		t.Errorf("a PDF was accepted: %v", err)
	}
	if _, err := Read("text/plain", []byte("   ")); err != ErrEmpty {
		t.Errorf("an empty document = %v", err)
	}
	if _, err := Read("text/plain", []byte(strings.Repeat("a", MaxSource+1))); err != ErrTooLarge {
		t.Error("an oversize document was accepted")
	}
}

func TestFuseRewardsAgreement(t *testing.T) {
	p := func(id int64) Passage { return Passage{ID: id, Text: "p"} }
	// Full text ranks 1 first; meaning ranks 2 first. 3 is second in both, so agreement
	// puts it above either list's own favourite.
	text := []Passage{p(1), p(3), p(4)}
	meaning := []Passage{p(2), p(3), p(5)}
	got := Fuse(3, text, meaning)
	if len(got) != 3 || got[0].ID != 3 {
		t.Fatalf("fused order = %v; the passage both halves liked should lead", ids(got))
	}
	if got[0].Score <= got[1].Score {
		t.Error("the fused score does not decrease down the list")
	}
	// One list on its own still answers.
	if only := Fuse(2, text, nil); len(only) != 2 || only[0].ID != 1 {
		t.Errorf("one list alone = %v", ids(only))
	}
	if none := Fuse(5); len(none) != 0 {
		t.Errorf("no lists = %v", ids(none))
	}
}

func ids(ps []Passage) []int64 {
	out := make([]int64, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

func TestGroundKeepsOnlyWhatThePassagesSay(t *testing.T) {
	ps := []Passage{
		{ID: 11, Source: "s1", Seq: 0, Title: "Brand voice", Text: "We write plainly and never oversell."},
		{ID: 12, Source: "s1", Seq: 1, Title: "Brand voice", Text: "Starter is $49 a month."},
		{ID: 13, Source: "s2", Seq: 0, Title: "Competitor pricing", URL: "https://rival.example/pricing", Text: "Rival charges $89 a month."},
	}

	written := "Your voice guide says to write plainly [1]. " +
		"Starter is $49 a month [2]. " +
		"Together that is $138 a month [2]. " + // a figure neither passage shows: arithmetic
		"Your competitors are growing fast. " + // no citation at all
		"Rival charges $89 [7]. " // a passage that was never retrieved
	answer, cites, dropped := Ground(written, ps)
	if dropped != 3 {
		t.Errorf("dropped %d sentences, want 3", dropped)
	}
	for _, gone := range []string{"$138", "growing fast", "[7]"} {
		if strings.Contains(answer, gone) {
			t.Errorf("the answer still carries %q:\n%s", gone, answer)
		}
	}
	if !strings.Contains(answer, "write plainly [1]") || !strings.Contains(answer, "$49 a month [2]") {
		t.Errorf("a grounded sentence was dropped:\n%s", answer)
	}
	if len(cites) != 2 || cites[0].Marker != 1 || cites[0].ChunkID != 11 || cites[1].ChunkID != 12 {
		t.Errorf("citations = %+v", cites)
	}

	// Markers are renumbered from one, so the reader never sees a gap where a dropped
	// sentence used to be.
	answer, cites, dropped = Ground("Rival charges $89 a month [3]. We oversell constantly [9].", ps)
	if dropped != 1 || answer != "Rival charges $89 a month [1]." {
		t.Errorf("renumbered answer = %q (dropped %d)", answer, dropped)
	}
	if len(cites) != 1 || cites[0].Marker != 1 || cites[0].ChunkID != 13 || cites[0].URL != "https://rival.example/pricing" {
		t.Errorf("citation after renumbering = %+v", cites)
	}

	// Nothing survivable at all is not an answer, and the caller says so in its own words.
	if answer, cites, _ := Ground("The sources do not cover that.", ps); answer != "" || cites != nil {
		t.Errorf("an uncited answer survived: %q %+v", answer, cites)
	}
	// A sentence citing two passages may use a figure from either.
	if answer, _, _ := Ground("Starter is $49 and Rival is $89 [2][3].", ps); !strings.Contains(answer, "[1][2]") {
		t.Errorf("a sentence citing two passages = %q", answer)
	}
}

func TestContextCarriesOnlyThePassages(t *testing.T) {
	got := Context([]Passage{
		{Title: "Brand voice", Text: "We write plainly."},
		{Title: "Pricing", URL: "https://acme.example/pricing", Text: "Starter is $49."},
	})
	for _, want := range []string{"[1] from \"Brand voice\"", "We write plainly.", "[2] from \"Pricing\"", "(https://acme.example/pricing)", "Starter is $49."} {
		if !strings.Contains(got, want) {
			t.Errorf("the model's context is missing %q:\n%s", want, got)
		}
	}
}
