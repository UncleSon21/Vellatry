package agent

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

func known() Known {
	return Known{
		Engines:     []string{"chatgpt", "gemini", "ai_overview"},
		Competitors: []string{"Ecosa", "Sleeping Duck"},
		Topics:      []TopicRef{{ID: "t1", Name: "mattress in a box"}},
	}
}

func TestRouterPicksTheAnalysis(t *testing.T) {
	cases := map[string]string{
		"Why did our AI visibility drop this month?":        VisibilityChange,
		"Where are competitors beating us in AI answers?":   CompetitorGaps,
		"What should we fix first?":                         FixPriority,
		"Why did organic traffic change?":                   TrafficChange,
		"Which sources do AI engines trust for our topics?": Sources,
		"What does AI say about us that is wrong?":          NegativeClaims,
		"Did the fixes we made work?":                       FixOutcomes,
		"What changed since the last report?":               SinceLastReport,
		"Are AI assistants sending us traffic?":             AIReferrals,
		"Which topics are worth the work?":                  TopicOpportunity,
	}
	for q, want := range cases {
		r := Pick(q, Context{}, known(), now)
		if r.Intent != want {
			t.Errorf("Pick(%q) = %q (%.2f, matched %q), want %q", q, r.Intent, r.Confidence, r.Matched, want)
		}
		if r.Confidence < Confident {
			t.Errorf("Pick(%q) confidence %.2f is below the bar, so a known question would go to the model", q, r.Confidence)
		}
	}
	if r := Pick("How do we compare with Ecosa?", Context{}, known(), now); r.Intent != CompetitorCompare || r.Slots.Competitor != "Ecosa" {
		t.Errorf("comparison = %+v", r)
	}
	// Nothing in the catalogue fits: the planner decides.
	for _, q := range []string{"Write me a poem about mattresses", "Should we hire an agency?"} {
		if r := Pick(q, Context{}, known(), now); r.Intent != "" && r.Confidence >= Confident {
			t.Errorf("Pick(%q) = %q, want the planner to handle it", q, r.Intent)
		}
	}
}

func TestRouterFillsSlots(t *testing.T) {
	r := Pick("Why did visibility drop on Gemini last week?", Context{}, known(), now)
	if r.Slots.Engine != "gemini" {
		t.Errorf("engine = %q", r.Slots.Engine)
	}
	if r.Slots.From.Format(time.DateOnly) != "2026-09-13" || r.Slots.To.Format(time.DateOnly) != "2026-09-19" {
		t.Errorf("period = %s to %s, want the last seven days", r.Slots.From.Format(time.DateOnly), r.Slots.To.Format(time.DateOnly))
	}
	r = Pick("Why did visibility drop last month?", Context{}, known(), now)
	if r.Slots.From.Format(time.DateOnly) != "2026-08-01" || r.Slots.To.Format(time.DateOnly) != "2026-08-31" {
		t.Errorf("last month = %s to %s", r.Slots.From.Format(time.DateOnly), r.Slots.To.Format(time.DateOnly))
	}
	// The page the question was asked from fills what the words leave out.
	r = Pick("What should we fix first?", Context{From: "2026-01-01", To: "2026-01-31", Engine: "chatgpt"}, known(), now)
	if r.Slots.Engine != "chatgpt" || r.Slots.From.Format(time.DateOnly) != "2026-01-01" {
		t.Errorf("context slots = %+v", r.Slots)
	}
	// With neither, four weeks ending yesterday.
	r = Pick("What should we fix first?", Context{}, known(), now)
	if r.Slots.To.Format(time.DateOnly) != "2026-09-19" || r.Slots.From.Format(time.DateOnly) != "2026-08-23" {
		t.Errorf("default period = %s to %s", r.Slots.From.Format(time.DateOnly), r.Slots.To.Format(time.DateOnly))
	}
	if got := Pick("How do we compare with sleeping duck?", Context{}, known(), now).Slots.Competitor; got != "Sleeping Duck" {
		t.Errorf("competitor = %q; matching should ignore case", got)
	}
}

func sample() Bundle {
	b := Bundle{Analysis: VisibilityChange, Question: "Why did our AI visibility drop?", From: "2026-08-23", To: "2026-09-19",
		Headline: "AI visibility is 42.5%, down 4.4 points, mostly on ChatGPT (down 8.0 points)."}
	b.Add(Fact{ID: "visibility", Label: "AI visibility", Value: "42.5%", Raw: 42.5, Unit: "%"})
	b.Add(Fact{ID: "previous", Label: "Visibility before", Value: "46.9%", Raw: 46.9, Unit: "%"})
	b.Add(Fact{ID: "answers", Label: "Answers collected", Value: "1,240", Raw: 1240})
	b.Tables = append(b.Tables, Table{ID: "by_engine", Title: "By engine", Head: []string{"Engine", "Visibility"},
		Rows: [][]string{{"ChatGPT", "38.0%"}, {"Gemini", "47.0%"}}})
	return b
}

func TestNarrateIsDeterministic(t *testing.T) {
	b := sample()
	text := Narrate(b)
	if !strings.HasPrefix(text, b.Headline) {
		t.Errorf("narration should lead with the headline:\n%s", text)
	}
	for _, want := range []string{"AI visibility: 42.5%", "Answers collected: 1,240", "By engine is below."} {
		if !strings.Contains(text, want) {
			t.Errorf("narration is missing %q:\n%s", want, text)
		}
	}
	empty := Narrate(Bundle{Empty: "no AI answers were collected in this period."})
	if !strings.Contains(empty, "nothing to answer with") {
		t.Errorf("empty narration = %q", empty)
	}
}

func TestCheckedRefusesInventedFigures(t *testing.T) {
	b := sample()
	good := "AI visibility fell to 42.5% from 46.9%, across 1,240 answers. ChatGPT is the weakest at 38.0%."
	if text, bad := Checked(good, b); text != good || len(bad) != 0 {
		t.Errorf("a grounded answer was rejected: %v", bad)
	}
	// The failure that mattered in the prior system: the model does the arithmetic.
	invented := "AI visibility fell 9.4% to 42.5%, which is 520 fewer mentions."
	text, bad := Checked(invented, b)
	if strings.Join(bad, ",") != "9.4,520" {
		t.Errorf("unverified figures = %v, want 9.4 and 520", bad)
	}
	if text != Narrate(b) {
		t.Error("a rejected narration must fall back to the code's own answer")
	}
	if text, bad := Checked("   ", b); text != Narrate(b) || bad != nil {
		t.Error("empty narration falls back quietly")
	}
}

func TestEvidenceCarriesOnlyTheNumbers(t *testing.T) {
	e := Evidence(sample())
	for _, want := range []string{"Question: Why did our AI visibility drop?", "Headline (already decided by code)", "- AI visibility: 42.5%", "| ChatGPT | 38.0% |"} {
		if !strings.Contains(e, want) {
			t.Errorf("evidence is missing %q:\n%s", want, e)
		}
	}
}

func TestFormatting(t *testing.T) {
	cases := map[string]string{
		Pct(42.46): "42.5%", Num(1234567): "1,234,567", Num(-450): "-450",
		Points(2.25): "up 2.3 points", Points(-1): "down 1.0 points", Points(0): "unchanged",
		Change(110, 100): "up 10.0%", Change(90, 100): "down 10.0%", Change(5, 0): "up from none", Change(0, 0): "unchanged",
		Trend(-1): "down", EngineName("ai_overview"): "Google AI Overview",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestPeriod(t *testing.T) {
	p := Period{From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)}
	if p.Days() != 28 {
		t.Errorf("days = %d", p.Days())
	}
	prev := p.Previous()
	if prev.From.Format(time.DateOnly) != "2026-08-04" || prev.To.Format(time.DateOnly) != "2026-08-31" {
		t.Errorf("previous = %s to %s", prev.From.Format(time.DateOnly), prev.To.Format(time.DateOnly))
	}
	if p.Label() != "1 Sep to 28 Sep 2026" {
		t.Errorf("label = %q", p.Label())
	}
}

func TestCatalogueMatchesRegistry(t *testing.T) {
	for _, c := range Catalogue {
		if _, ok := registry[c.Name]; !ok {
			t.Errorf("%s is offered but has no analysis", c.Name)
		}
	}
	for name := range registry {
		found := false
		for _, c := range Catalogue {
			found = found || c.Name == name
		}
		if !found {
			t.Errorf("%s exists but is not offered", name)
		}
	}
}
