package automation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNormalise(t *testing.T) {
	engines := []string{"chatgpt", "gemini"}
	w, err := Normalise(Watcher{Kind: VisibilityDrop, Params: Params{Competitor: "dropped", Engine: "gemini"}}, engines)
	if err != nil {
		t.Fatal(err)
	}
	if w.Params.Points != 10 || w.Params.Engine != "gemini" || w.Params.Competitor != "" || w.Delivery != "immediate" {
		t.Errorf("normalised = %+v", w)
	}
	for _, bad := range []Watcher{
		{Kind: "made_up"},
		{Kind: VisibilityDrop, Params: Params{Points: 500}},
		{Kind: VisibilityDrop, Params: Params{Engine: "perplexity"}},
		{Kind: CriticalFinding, Delivery: "sometimes"},
	} {
		if _, err := Normalise(bad, engines); !errors.Is(err, ErrInvalid) {
			t.Errorf("Normalise(%+v) err = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestDrop(t *testing.T) {
	if _, ok := Drop(Window{19, 5}, Window{40, 20}); ok {
		t.Error("a week with fewer than 20 answers must not count")
	}
	d, ok := Drop(Window{40, 8}, Window{40, 20})
	if !ok || d != 30 {
		t.Errorf("drop = %v %v, want 30 points", d, ok)
	}
	if d, _ := Drop(Window{40, 30}, Window{40, 20}); d >= 0 {
		t.Errorf("a rise reported as a drop of %v", d)
	}
}

func answers(prompt string, current bool, n int, brand int, rivals map[string]int) []PromptAnswer {
	var out []PromptAnswer
	for i := 0; i < n; i++ {
		a := PromptAnswer{PromptID: prompt, Prompt: "best " + prompt, Current: current, Brand: i < brand}
		for name, k := range rivals {
			if i < k {
				a.Competitors = append(a.Competitors, name, name) // repeats in one answer count once
			}
		}
		out = append(out, a)
	}
	return out
}

func TestOvertakes(t *testing.T) {
	var all []PromptAnswer
	// p1: Rival overtakes (last week 1 vs brand 3; this week 4 vs brand 2).
	all = append(all, answers("p1", false, 5, 3, map[string]int{"Rival": 1})...)
	all = append(all, answers("p1", true, 5, 2, map[string]int{"Rival": 4, "Other": 1})...)
	// p2: Rival was already ahead last week: not news.
	all = append(all, answers("p2", false, 5, 1, map[string]int{"Rival": 3})...)
	all = append(all, answers("p2", true, 5, 1, map[string]int{"Rival": 4})...)
	// p3: too few answers this week.
	all = append(all, answers("p3", false, 5, 3, nil)...)
	all = append(all, answers("p3", true, 2, 0, map[string]int{"Rival": 2})...)

	got := Overtakes(all, "")
	if len(got) != 1 || got[0].PromptID != "p1" || got[0].Competitor != "Rival" {
		t.Fatalf("overtakes = %+v, want Rival on p1", got)
	}
	if got[0].Rival[1].Mentioned != 4 || got[0].Brand[1].Mentioned != 2 {
		t.Errorf("counts = %+v", got[0])
	}
	if len(Overtakes(all, "other")) != 0 {
		t.Error("filtering to Other should find nothing")
	}
	if len(Overtakes(all, "rival")) != 1 {
		t.Error("the competitor filter should ignore case")
	}
}

func TestValidSlackWebhook(t *testing.T) {
	ok := "https://hooks.slack.com/services/T000/B000/XXXXXXXX"
	if err := ValidSlackWebhook(ok); err != nil {
		t.Errorf("valid webhook refused: %v", err)
	}
	for _, bad := range []string{
		"http://hooks.slack.com/services/T000/B000/XXXX",
		"https://hooks.slack.com.evil.example/services/T000/B000/XXXX",
		"https://evil.example/services/T000/B000/XXXX",
		"https://user@hooks.slack.com/services/T000/B000/XXXX",
		"https://hooks.slack.com/workflows/T000/B000/XXXX",
		"https://hooks.slack.com/services/T000",
		"https://169.254.169.254/services/a/b/c",
	} {
		if err := ValidSlackWebhook(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestValidRecipients(t *testing.T) {
	got, err := ValidRecipients([]string{"CMO@Koala.com.au", "cmo@koala.com.au", "ana@mail.koala.com.au", "contractor@gmail.com"},
		[]string{"koala.com.au"}, []string{"Contractor@gmail.com"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "cmo@koala.com.au,ana@mail.koala.com.au,contractor@gmail.com" {
		t.Errorf("recipients = %v", got)
	}
	for _, bad := range [][]string{
		{"stranger@gmail.com"},
		{"notkoala.com.au@evil.com"},
		{"x@evilkoala.com.au"},
		{"not an address"},
		{},
	} {
		if _, err := ValidRecipients(bad, []string{"koala.com.au"}, nil); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestRenderEscapesSlackAndHTML(t *testing.T) {
	link := "/site/findings/7"
	r, err := Render(Stored{Kind: CriticalFinding, Severity: "critical", Title: "New <critical> issue",
		Body: "robots.txt blocks <OAI-SearchBot> & more", Link: &link, Occurrences: 2}, "https://app.vellatry.com")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Text   string `json:"text"`
		Blocks []struct {
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(r.Slack, &payload); err != nil {
		t.Fatal(err)
	}
	main := payload.Blocks[0].Text.Text
	if !strings.Contains(main, "&lt;OAI-SearchBot&gt; &amp; more") || strings.Contains(main, "<OAI") {
		t.Errorf("slack text not escaped: %q", main)
	}
	if !strings.Contains(main, "<https://app.vellatry.com/site/findings/7|New &lt;critical&gt; issue>") {
		t.Errorf("slack link = %q", main)
	}
	if strings.Contains(r.HTML, "<OAI-SearchBot>") || !strings.Contains(r.HTML, "&lt;OAI-SearchBot&gt;") {
		t.Error("html not escaped")
	}
	if !strings.Contains(r.Text, "Seen 2 times") || !strings.Contains(r.Text, "https://app.vellatry.com/site/findings/7") {
		t.Errorf("text = %q", r.Text)
	}
}

func f(v float64) *float64 { return &v }

func TestRenderDigestLeavesOutMissingSections(t *testing.T) {
	d := Digest{Brand: "Koala", From: "2026-09-07", To: "2026-09-13",
		Visibility: &DigestVisibility{Answers: 120, Visibility: f(42.5), Previous: f(40), ShareOfVoice: f(30),
			ByEngine: []EngineLine{{Engine: "chatgpt", Visibility: f(50), Previous: f(45)}}},
		Site: &DigestSite{LastCrawl: "2026-09-12", Critical: 1, Warning: 4, FixesLive: 2, FixesProposed: 3},
	}
	r, err := RenderDigest(d, "https://app")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Koala weekly", "42.5%", "up 2.5 pts", "ChatGPT: 50.0% (up 5.0 pts)", "1 critical and 4 warning", "7 Sep to 13 Sep"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("text missing %q:\n%s", want, r.Text)
		}
	}
	for _, absent := range []string{"SEARCH CONSOLE", "AI REFERRALS", "BLINDSPOTS", "ALERTS", "RECONNECTING"} {
		if strings.Contains(r.Text, absent) {
			t.Errorf("text has a %s section with no data", absent)
		}
		if strings.Contains(strings.ToUpper(r.HTML), ">"+absent) {
			t.Errorf("html has a %s section with no data", absent)
		}
	}
	var payload struct {
		Blocks []json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(r.Slack, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Blocks) != 4 { // header, visibility, site, footer link
		t.Errorf("slack blocks = %d, want 4: %s", len(payload.Blocks), r.Slack)
	}
	if !(Digest{}).Empty() || d.Empty() {
		t.Error("Empty is wrong")
	}
}

func TestFormatting(t *testing.T) {
	cases := map[string]string{
		fmtInt(0): "0", fmtInt(999): "999", fmtInt(1000): "1,000", fmtInt(1234567): "1,234,567", fmtInt(-4500): "-4,500",
		fmtChange(110, 100): "up 10.0%", fmtChange(90, 100): "down 10.0%", fmtChange(5, 0): "new", fmtChange(0, 0): "no change",
		fmtPoints(f(40), f(42.25)): "down 2.3 pts", fmtPoints(nil, f(1)): "",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
