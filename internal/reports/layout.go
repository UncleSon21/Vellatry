package reports

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Block is one section as the reader sees it. The HTML (web and PDF) and the plain
// text (the figure check and the summary drafter's input) are both rendered from
// blocks, so they always show the same numbers.
type Block struct {
	Key     string
	Title   string
	Intro   string
	Metrics []Metric
	Chart   *Chart
	Tables  []Table
	Bullets []string
	Note    string // the team's own words for this section
}

// Metric is one headline number.
type Metric struct {
	Label  string
	Value  string
	Change string
	Good   *bool // colour of the change; nil when neutral
}

// Table is a small table.
type Table struct {
	Title string
	Head  []string
	Rows  [][]string
	Bold  []bool // rows to emphasise (the brand among competitors)
}

// Chart is a daily line.
type Chart struct {
	Caption string
	Points  []DayValue
}

var engineNames = map[string]string{"chatgpt": "ChatGPT", "gemini": "Gemini", "ai_overview": "Google AI Overview"}

func engineName(e string) string {
	if n := engineNames[e]; n != "" {
		return n
	}
	return e
}

func pct(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'f', 1, 64) + "%"
}

func num(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func dec(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'f', 1, 64)
}

func boolp(b bool) *bool { return &b }

// pointsChange describes a change in percentage points.
func pointsChange(p Pair) (string, *bool) {
	if p.Current == nil || p.Previous == nil {
		return "", nil
	}
	d := math.Round((*p.Current-*p.Previous)*10) / 10
	switch {
	case d > 0:
		return fmt.Sprintf("up %.1f pts", d), boolp(true)
	case d < 0:
		return fmt.Sprintf("down %.1f pts", -d), boolp(false)
	}
	return "no change", nil
}

// rankChange describes a change in a position where lower is better.
func rankChange(p Pair) (string, *bool) {
	if p.Current == nil || p.Previous == nil {
		return "", nil
	}
	d := math.Round((*p.Current-*p.Previous)*10) / 10
	switch {
	case d < 0:
		return fmt.Sprintf("improved %.1f", -d), boolp(true)
	case d > 0:
		return fmt.Sprintf("slipped %.1f", d), boolp(false)
	}
	return "no change", nil
}

// countChange describes a change in a count as a percentage.
func countChange(p IntPair) (string, *bool) {
	if p.Previous == nil {
		return "", nil
	}
	prev := *p.Previous
	if prev == 0 {
		if p.Current == 0 {
			return "no change", nil
		}
		return "up from 0", boolp(true)
	}
	d := math.Round(1000*float64(p.Current-prev)/float64(prev)) / 10
	switch {
	case d > 0:
		return fmt.Sprintf("up %.1f%%", d), boolp(true)
	case d < 0:
		return fmt.Sprintf("down %.1f%%", -d), boolp(false)
	}
	return "no change", nil
}

func shortDate(s string) string {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return s
	}
	return t.Format("2 Jan 2006")
}

func chartOf(points []DayValue, what string) *Chart {
	n := 0
	for _, p := range points {
		if p.Value != nil {
			n++
		}
	}
	if n < 2 {
		return nil
	}
	return &Chart{Caption: what + ", daily, " + shortDate(points[0].Day) + " to " + shortDate(points[len(points)-1].Day), Points: points}
}

// Layout turns a snapshot and the team's notes into blocks, in the series' order.
func Layout(s Snapshot, notes map[string]string) []Block {
	var out []Block
	for _, key := range s.Order {
		var b *Block
		switch key {
		case SecVisibility:
			b = visibilityBlock(s)
		case SecBlindspots:
			b = blindspotBlock(s)
		case SecSearch:
			b = searchBlock(s)
		case SecAIReferrals:
			b = channelBlock(s.AIReferrals, key, "Visits from AI assistants",
				"Sessions on your site that came from an AI assistant, from Google Analytics 4.", "Assistant")
		case SecTraffic:
			b = channelBlock(s.Traffic, key, "Website traffic", "Sessions by channel, from Google Analytics 4.", "Channel")
		case SecSite:
			b = siteBlock(s)
		case SecFixes:
			b = fixesBlock(s)
		}
		if b == nil {
			continue
		}
		b.Key = key
		b.Note = strings.TrimSpace(notes[key])
		out = append(out, *b)
	}
	return out
}

func visibilityBlock(s Snapshot) *Block {
	v := s.Visibility
	if v == nil {
		return nil
	}
	var engines []string
	for _, e := range v.ByEngine {
		engines = append(engines, engineName(e.Engine))
	}
	b := &Block{Title: "AI visibility",
		Intro: fmt.Sprintf("How often %s mentioned %s when asked about your topics, across %s answers.", joinAnd(engines), s.Brand, num(int64(v.Answers)))}
	c, good := pointsChange(v.Visibility)
	b.Metrics = append(b.Metrics, Metric{Label: "Visibility", Value: pct(v.Visibility.Current), Change: c, Good: good})
	c, good = pointsChange(v.ShareOfVoice)
	b.Metrics = append(b.Metrics, Metric{Label: "Share of voice", Value: pct(v.ShareOfVoice.Current), Change: c, Good: good})
	if v.Position.Current != nil {
		c, good = rankChange(v.Position)
		b.Metrics = append(b.Metrics, Metric{Label: "Average position when mentioned", Value: dec(v.Position.Current), Change: c, Good: good})
	}
	b.Chart = chartOf(v.Series, "Visibility")
	if len(v.ByEngine) > 1 {
		t := Table{Title: "By engine", Head: []string{"Engine", "Visibility", "Change"}}
		for _, e := range v.ByEngine {
			c, _ := pointsChange(e.Visibility)
			t.Rows = append(t.Rows, []string{engineName(e.Engine), pct(e.Visibility.Current), c})
		}
		b.Tables = append(b.Tables, t)
	}
	if len(v.Entities) > 1 {
		t := Table{Title: "Against competitors", Head: []string{"Brand", "Visibility", "Share of voice"}}
		for _, e := range v.Entities {
			t.Rows = append(t.Rows, []string{e.Name, pct(e.Visibility), pct(e.ShareOfVoice)})
			t.Bold = append(t.Bold, e.IsBrand)
		}
		b.Tables = append(b.Tables, t)
	}
	if len(v.Sources) > 0 {
		t := Table{Title: "Sources the AI engines cited most", Head: []string{"Website", "Type", "Citations"}}
		for _, r := range v.Sources {
			t.Rows = append(t.Rows, []string{r.Domain, sourceType(r.Type), num(int64(r.Citations))})
		}
		b.Tables = append(b.Tables, t)
	}
	return b
}

func sourceType(t string) string {
	switch t {
	case "owned":
		return "Your site"
	case "competitor":
		return "Competitor"
	case "ugc":
		return "Forum or social"
	case "review":
		return "Reviews"
	case "reference":
		return "Reference"
	}
	return "Editorial"
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return "AI engines"
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func blindspotBlock(s Snapshot) *Block {
	bs := s.Blindspots
	if bs == nil {
		return nil
	}
	b := &Block{Title: "Blindspots",
		Intro: "Questions where an AI engine consistently leaves " + s.Brand + " out or recommends a competitor first, confirmed over five answers."}
	b.Metrics = []Metric{
		{Label: "Confirmed this period", Value: num(int64(bs.Confirmed))},
		{Label: "Resolved this period", Value: num(int64(bs.Resolved)), Good: boolp(bs.Resolved > 0)},
		{Label: "Open now", Value: num(int64(bs.OpenNow))},
	}
	if bs.Resolved == 0 {
		b.Metrics[1].Good = nil
	}
	if len(bs.Top) > 0 {
		t := Table{Title: "Highest-priority open blindspots", Head: []string{"Engine", "Question", "What happens"}}
		for _, r := range bs.Top {
			what := "Leaves " + s.Brand + " out"
			if r.Kind == "displacement" && r.Competitor != nil {
				what = "Recommends " + *r.Competitor + " first"
			} else if r.Kind != "visibility" && r.Kind != "displacement" {
				what = strings.ToUpper(r.Kind[:1]) + r.Kind[1:]
			}
			t.Rows = append(t.Rows, []string{engineName(r.Engine), r.Prompt, what})
		}
		b.Tables = append(b.Tables, t)
	}
	return b
}

func searchBlock(s Snapshot) *Block {
	sc := s.Search
	if sc == nil {
		return nil
	}
	b := &Block{Title: "Google Search", Intro: "Performance in Google Search, from Search Console."}
	c, good := countChange(sc.Clicks)
	b.Metrics = append(b.Metrics, Metric{Label: "Clicks", Value: num(sc.Clicks.Current), Change: c, Good: good})
	c, good = countChange(sc.Impressions)
	b.Metrics = append(b.Metrics, Metric{Label: "Impressions", Value: num(sc.Impressions.Current), Change: c, Good: good})
	c, good = pointsChange(sc.CTR)
	b.Metrics = append(b.Metrics, Metric{Label: "Click-through rate", Value: pct(sc.CTR.Current), Change: c, Good: good})
	c, good = rankChange(sc.Position)
	b.Metrics = append(b.Metrics, Metric{Label: "Average position", Value: dec(sc.Position.Current), Change: c, Good: good})
	b.Chart = chartOf(sc.Series, "Clicks")
	for _, part := range []struct {
		title, head string
		rows        []TopRow
	}{{"Top searches", "Search", sc.Queries}, {"Top pages", "Page", sc.Pages}} {
		if len(part.rows) == 0 {
			continue
		}
		t := Table{Title: part.title, Head: []string{part.head, "Clicks", "Impressions", "Position"}}
		for _, r := range part.rows {
			t.Rows = append(t.Rows, []string{r.Key, num(r.Clicks), num(r.Impressions), dec(r.Position)})
		}
		b.Tables = append(b.Tables, t)
	}
	return b
}

func channelBlock(c *ChannelSection, key, title, intro, col string) *Block {
	if c == nil {
		return nil
	}
	b := &Block{Title: title, Intro: intro}
	ch, good := countChange(c.Sessions)
	b.Metrics = append(b.Metrics, Metric{Label: "Sessions", Value: num(c.Sessions.Current), Change: ch, Good: good})
	if c.KeyEvents.Current != nil {
		ch, good = pointsChangeCount(c.KeyEvents)
		b.Metrics = append(b.Metrics, Metric{Label: "Key events", Value: num(int64(*c.KeyEvents.Current)), Change: ch, Good: good})
	}
	if len(c.Rows) > 0 {
		t := Table{Head: []string{col, "Sessions", "Change"}}
		for _, r := range c.Rows {
			ch, _ := countChange(IntPair{Current: r.Sessions, Previous: r.Previous})
			t.Rows = append(t.Rows, []string{r.Name, num(r.Sessions), ch})
		}
		b.Tables = append(b.Tables, t)
	}
	return b
}

// pointsChangeCount describes a change in a whole-number total held as a float.
func pointsChangeCount(p Pair) (string, *bool) {
	if p.Current == nil || p.Previous == nil {
		return "", nil
	}
	prev := int64(*p.Previous)
	return countChange(IntPair{Current: int64(*p.Current), Previous: &prev})
}

func siteBlock(s Snapshot) *Block {
	st := s.Site
	if st == nil {
		return nil
	}
	b := &Block{Title: "Site health", Intro: fmt.Sprintf("As of the crawl on %s (%s pages).", shortDate(st.CrawledOn), num(int64(st.Pages)))}
	llms := "Not published"
	if st.LLMSTxt {
		llms = "Published"
	}
	b.Metrics = []Metric{
		{Label: "Critical issues", Value: num(int64(st.Critical)), Good: boolp(st.Critical == 0)},
		{Label: "Warnings", Value: num(int64(st.Warning))},
		{Label: "llms.txt", Value: llms},
	}
	if len(st.AIAccess) > 0 {
		t := Table{Title: "AI search crawlers", Head: []string{"Crawler", "Used by", "Access"}}
		for _, a := range st.AIAccess {
			access := "Allowed"
			if a.Blocked {
				access = "Blocked"
			}
			t.Rows = append(t.Rows, []string{a.Agent, a.Product, access})
		}
		b.Tables = append(b.Tables, t)
	}
	return b
}

func fixesBlock(s Snapshot) *Block {
	f := s.Fixes
	if f == nil {
		return nil
	}
	b := &Block{Title: "What changed on the site", Intro: "Changes the team made that Vellatry confirmed on the live site."}
	b.Metrics = []Metric{
		{Label: "Confirmed live", Value: num(int64(len(f.Live))), Good: boolp(len(f.Live) > 0)},
		{Label: "In progress", Value: num(int64(f.InProgress))},
	}
	if len(f.Live) == 0 {
		b.Metrics[0].Good = nil
	}
	for _, x := range f.Live {
		line := x.Title
		if x.Page != nil && *x.Page != "" {
			line += " (" + *x.Page + ")"
		}
		b.Bullets = append(b.Bullets, line+", live "+shortDate(x.LiveOn))
	}
	return b
}

// Sources names the data behind the sections present, for the report's footer. Only
// sources the report actually uses are named.
func Sources(s Snapshot) string {
	var out []string
	seen := map[string]bool{}
	add := func(x string) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	for _, k := range s.Order {
		switch k {
		case SecVisibility, SecBlindspots:
			add("AI answers collected by Vellatry")
		case SecSearch:
			add("Google Search Console")
		case SecAIReferrals, SecTraffic:
			add("Google Analytics 4")
		case SecSite, SecFixes:
			add("Vellatry's crawl of your site")
		}
	}
	return joinAnd(out)
}
