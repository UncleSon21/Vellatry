// Package agent answers questions about a tenant's own data. Code computes every number
// and picks the headline; a model, when one is used at all, only chooses which analysis
// to run and puts the numbers into sentences, and anything it writes is checked against
// the evidence before it is shown.
package agent

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Bundle is what an analysis produces: the facts behind an answer, each with an id, so
// every sentence can be traced back to a number the code computed.
type Bundle struct {
	Analysis string   `json:"analysis"`
	Question string   `json:"question"`
	Headline string   `json:"headline"` // the one thing that matters, chosen by code
	From     string   `json:"from,omitempty"`
	To       string   `json:"to,omitempty"`
	Facts    []Fact   `json:"facts"`
	Tables   []Table  `json:"tables,omitempty"`
	Series   []Series `json:"series,omitempty"`
	Links    []Link   `json:"links,omitempty"`
	// Actions the answer offers. Nothing runs until the person confirms it.
	Actions []Action `json:"actions,omitempty"`
	// Empty says why there is nothing to answer with, when that is the case.
	Empty string `json:"empty,omitempty"`
}

// Fact is one number or value the answer may use.
type Fact struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Value string  `json:"value"`           // as it should be written
	Raw   float64 `json:"raw,omitempty"`   // the number behind it
	Unit  string  `json:"unit,omitempty"`  // %, clicks, points...
	Note  string  `json:"note,omitempty"`  // what it means
	Trend string  `json:"trend,omitempty"` // up | down | flat
}

// Table is a small table of rows.
type Table struct {
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	Head    []string   `json:"head"`
	Rows    [][]string `json:"rows"`
	Caption string     `json:"caption,omitempty"`
}

// Series is a line for a chart the dashboard draws.
type Series struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Unit   string   `json:"unit,omitempty"`
	Points []Point  `json:"points"`
	Labels []string `json:"labels,omitempty"`
}

// Point is one day of a series.
type Point struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

// Action is something the agent can do next. It is a proposal: the dashboard shows it
// as a button and the person confirms before anything happens.
type Action struct {
	Kind    string            `json:"kind"`
	Label   string            `json:"label"`
	Confirm string            `json:"confirm"` // what the person is agreeing to
	Params  map[string]string `json:"params,omitempty"`
}

// The actions the agent may propose. Each maps to something the dashboard can already
// do, run with the person's own permissions.
const (
	ActionCrawlSite    = "crawl_site"
	ActionResearch     = "research_keywords"
	ActionWatcher      = "create_watcher"
	ActionCheckNow     = "check_now"
	ActionSendToAsana  = "send_to_asana"
	ActionApproveTopic = "approve_topic"
)

// Link points at the page in Vellatry that shows the working.
type Link struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// Add appends a fact and returns its id.
func (b *Bundle) Add(f Fact) string {
	if f.ID == "" {
		f.ID = fmt.Sprintf("f%d", len(b.Facts)+1)
	}
	b.Facts = append(b.Facts, f)
	return f.ID
}

// Fact looks a fact up by id.
func (b *Bundle) Fact(id string) (Fact, bool) {
	for _, f := range b.Facts {
		if f.ID == id {
			return f, true
		}
	}
	return Fact{}, false
}

// ---- formatting ------------------------------------------------------------------------

// Pct writes a percentage the way the dashboard does.
func Pct(v float64) string { return strconv.FormatFloat(round1(v), 'f', 1, 64) + "%" }

// Points writes a change in percentage points.
func Points(v float64) string {
	v = round1(v)
	switch {
	case v > 0:
		return fmt.Sprintf("up %.1f points", v)
	case v < 0:
		return fmt.Sprintf("down %.1f points", -v)
	}
	return "unchanged"
}

// Num writes a count with thousands separators.
func Num(n int64) string {
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

// Change writes a change in a count as a percentage.
func Change(cur, prev int64) string {
	if prev == 0 {
		if cur == 0 {
			return "unchanged"
		}
		return "up from none"
	}
	d := round1(100 * float64(cur-prev) / float64(prev))
	switch {
	case d > 0:
		return fmt.Sprintf("up %.1f%%", d)
	case d < 0:
		return fmt.Sprintf("down %.1f%%", -d)
	}
	return "unchanged"
}

// Trend says which way a change went.
func Trend(delta float64) string {
	switch {
	case delta > 0:
		return "up"
	case delta < 0:
		return "down"
	}
	return "flat"
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Period is the range an analysis covers.
type Period struct {
	From time.Time
	To   time.Time
}

// Days is the length of the period.
func (p Period) Days() int { return int(p.To.Sub(p.From).Hours()/24) + 1 }

// Previous is the period of the same length immediately before.
func (p Period) Previous() Period {
	n := p.Days()
	return Period{From: p.From.AddDate(0, 0, -n), To: p.From.AddDate(0, 0, -1)}
}

// Label writes the period for people.
func (p Period) Label() string {
	if p.From.Year() == p.To.Year() {
		return p.From.Format("2 Jan") + " to " + p.To.Format("2 Jan 2006")
	}
	return p.From.Format("2 Jan 2006") + " to " + p.To.Format("2 Jan 2006")
}

// EngineName is how an engine is written for people.
func EngineName(e string) string {
	switch e {
	case "chatgpt":
		return "ChatGPT"
	case "gemini":
		return "Gemini"
	case "ai_overview":
		return "Google AI Overview"
	}
	return e
}
