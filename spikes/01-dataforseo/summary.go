package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/UncleSon21/vellatry/internal/visibility/gap"
)

// projection is a per-tenant monthly usage scenario.
type projection struct {
	topics          int
	promptsPerTopic int
	confirmShare    float64
	tracked         int
}

const weeksPerMonth = 4.33

// monthlyCalls returns answers per engine per month: discovery screens every prompt with
// 2 attempts and confirms a share with 5; tracking refreshes tracked prompts weekly with 5.
func (p projection) monthlyCalls() (discover, track float64) {
	discover = float64(p.topics*p.promptsPerTopic) * (2 + p.confirmShare*5)
	track = float64(p.tracked) * 5 * weeksPerMonth
	return discover, track
}

type groupKey struct{ engine, variant string }

type stats struct {
	answers, errors, present, withSources, mentioned, competitor, visGap, dispGap int
	cost                                                                          float64
	latencies                                                                     []int64
	sources, fanOut                                                               int
}

func summarize(recs []Record, o options, planned int, spent float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Spike 1 results\n\n")
	fmt.Fprintf(&b, "Mode: %s. Engines: %s. Attempts: %d. Answers: %d of %d planned. Spent: $%.4f of $%.2f budget.",
		o.mode, engineList(o), o.attempts, len(recs), planned, spent, o.budgetUSD)
	if o.dryRun {
		b.WriteString(" DRY RUN: fake API, numbers are not real.")
	}
	b.WriteString("\n\n")

	groups := map[groupKey]*stats{}
	for _, r := range recs {
		k := groupKey{r.Engine, r.Variant}
		s := groups[k]
		if s == nil {
			s = &stats{}
			groups[k] = s
		}
		if !r.OK {
			s.errors++
			continue
		}
		s.answers++
		s.cost += r.Cost
		s.latencies = append(s.latencies, r.LatencyMS)
		if !r.Present {
			continue
		}
		s.present++
		s.sources += len(r.Sources)
		s.fanOut += len(r.FanOut)
		if len(r.Sources) > 0 {
			s.withSources++
		}
		if r.BrandMentioned {
			s.mentioned++
		}
		if len(r.CompetitorsMentioned) > 0 {
			s.competitor++
		}
		if r.Verdict.Visibility {
			s.visGap++
		}
		if r.Verdict.Displacement {
			s.dispGap++
		}
	}
	keys := make([]groupKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].engine != keys[j].engine {
			return keys[i].engine < keys[j].engine
		}
		return keys[i].variant < keys[j].variant
	})

	b.WriteString("## By engine\n\nRates for mentions, sources and gaps are over answers where the engine produced an AI answer.\n\n")
	b.WriteString("| Engine | Variant | Answers | Errors | AI answer present | Cost per answer (USD) | Latency p50 / p95 (s) | With sources | Avg sources | Avg fan-out | Brand mentioned | Competitor mentioned | Visibility gap | Displacement gap |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, k := range keys {
		s := groups[k]
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %s | %s | %s / %s | %s | %s | %s | %s | %s | %s | %s |\n",
			k.engine, k.variant, s.answers, s.errors, pct(s.present, s.answers), money(s.cost, s.answers),
			secs(percentile(s.latencies, 0.5)), secs(percentile(s.latencies, 0.95)),
			pct(s.withSources, s.present), avg(s.sources, s.present), avg(s.fanOut, s.present),
			pct(s.mentioned, s.present), pct(s.competitor, s.present), pct(s.visGap, s.present), pct(s.dispGap, s.present))
	}

	writeMentionRates(&b, recs)
	writeCrossCheck(&b, recs)
	writeProjection(&b, groups, o.proj)
	writeErrors(&b, recs)
	return b.String()
}

func writeMentionRates(b *strings.Builder, recs []Record) {
	type cell struct{ set, query, engine, variant string }
	type tally struct{ mentioned, answered int }
	cells := map[cell]*tally{}
	var order []cell
	for _, r := range recs {
		if !r.OK || !r.Present {
			continue
		}
		c := cell{r.Set, r.Query, r.Engine, r.Variant}
		if cells[c] == nil {
			cells[c] = &tally{}
			order = append(order, c)
		}
		cells[c].answered++
		if r.BrandMentioned {
			cells[c].mentioned++
		}
	}
	sort.Slice(order, func(i, j int) bool {
		a, z := order[i], order[j]
		return a.set+a.query+a.engine+a.variant < z.set+z.query+z.engine+z.variant
	})
	b.WriteString("\n## Mention rate per query\n\nBands apply only to cells with 5 answers (the confirmation depth).\n\n")
	b.WriteString("| Set | Query | Engine | Variant | Mentioned / answered | Band |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, c := range order {
		t := cells[c]
		band := "-"
		if t.answered == 5 {
			band = string(gap.MentionBand(t.mentioned, t.answered))
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %d / %d | %s |\n", c.set, c.query, c.engine, c.variant, t.mentioned, t.answered, band)
	}
}

// writeCrossCheck lists answers where our detector and DataForSEO's brand entities
// disagree about the brand. These are the first cases to hand-check.
func writeCrossCheck(b *strings.Builder, recs []Record) {
	var lines []string
	checked := 0
	for _, r := range recs {
		if !r.OK || !r.Present || len(r.BrandEntities) == 0 {
			continue
		}
		checked++
		theirs := false
		brand := strings.ToLower(r.Brand)
		for _, e := range r.BrandEntities {
			if strings.Contains(strings.ToLower(e), brand) {
				theirs = true
				break
			}
		}
		if theirs != r.BrandMentioned {
			lines = append(lines, fmt.Sprintf("| %s | %v | %v | %s |", r.ID, r.BrandMentioned, theirs, strings.Join(r.BrandEntities, ", ")))
		}
	}
	fmt.Fprintf(b, "\n## Detection cross-check\n\n%d answers carried DataForSEO brand entities; %d disagree with our detector.\n\n", checked, len(lines))
	if len(lines) == 0 {
		return
	}
	b.WriteString("| Answer | Our detector | DataForSEO entities | Entities |\n| --- | --- | --- | --- |\n")
	for i, l := range lines {
		if i == 20 {
			fmt.Fprintf(b, "\n%d more in results.jsonl.\n", len(lines)-20)
			break
		}
		b.WriteString(l + "\n")
	}
}

func writeProjection(b *strings.Builder, groups map[groupKey]*stats, p projection) {
	discover, track := p.monthlyCalls()
	fmt.Fprintf(b, "\n## Monthly projection per tenant\n\nScenario: %d topics x %d prompts screened at 2 attempts, %.0f%% confirmed at 5, plus %d tracked prompts at 5 attempts weekly. Per engine: %.0f discovery + %.0f tracking answers a month. Uses the measured cost per answer of this run's mode.\n\n",
		p.topics, p.promptsPerTopic, p.confirmShare*100, p.tracked, discover, track)
	b.WriteString("| Engine | Variant | Cost per answer (USD) | Discovery (USD/month) | Tracking (USD/month) | Total (USD/month) |\n| --- | --- | --- | --- | --- | --- |\n")
	keys := make([]groupKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].engine+keys[i].variant < keys[j].engine+keys[j].variant })
	// The total counts one variant per engine: ChatGPT with web search off (the default,
	// matching real users) when it was measured, otherwise whichever variant ran.
	perEngine := map[string]float64{}
	for _, k := range keys {
		s := groups[k]
		if s.answers == 0 {
			continue
		}
		unit := s.cost / float64(s.answers)
		d, t := unit*discover, unit*track
		fmt.Fprintf(b, "| %s | %s | %.4f | %.2f | %.2f | %.2f |\n", k.engine, k.variant, unit, d, t, d+t)
		if _, seen := perEngine[k.engine]; !seen || k.variant == "web_search_off" {
			perEngine[k.engine] = d + t
		}
	}
	var total float64
	for _, v := range perEngine {
		total += v
	}
	fmt.Fprintf(b, "\nTotal across engines (one variant each): about $%.2f USD per tenant per month. No LLM cost on this path.\n", total)
}

func writeErrors(b *strings.Builder, recs []Record) {
	counts := map[string]int{}
	for _, r := range recs {
		if !r.OK {
			msg := r.Error
			if len(msg) > 120 {
				msg = msg[:120]
			}
			counts[r.Engine+": "+msg]++
		}
	}
	if len(counts) == 0 {
		return
	}
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range counts {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	b.WriteString("\n## Errors\n\n| Count | Error |\n| --- | --- |\n")
	for i, e := range list {
		if i == 10 {
			break
		}
		fmt.Fprintf(b, "| %d | %s |\n", e.v, strings.ReplaceAll(e.k, "|", "/"))
	}
}

func engineList(o options) string {
	var s []string
	for _, e := range o.engines {
		s = append(s, string(e))
	}
	return strings.Join(s, ", ")
}

func percentile(v []int64, p float64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(p*float64(len(s))+0.5) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

func pct(n, d int) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(d))
}

func avg(n, d int) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", float64(n)/float64(d))
}

func money(total float64, n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%.4f", total/float64(n))
}

func secs(ms int64) string { return fmt.Sprintf("%.1f", float64(ms)/1000) }
