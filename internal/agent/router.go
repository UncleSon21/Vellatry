package agent

import (
	"sort"
	"strings"
	"time"
)

// The analyses the router can pick. Each is deterministic, tested, and answers one of
// the questions marketing teams and CMOs actually ask.
const (
	VisibilityChange  = "visibility_change"  // why did our AI visibility change?
	CompetitorGaps    = "competitor_gaps"    // where are competitors beating us?
	FixPriority       = "fix_priority"       // what should we fix first?
	TrafficChange     = "traffic_change"     // why did organic traffic change?
	CompetitorCompare = "competitor_compare" // how do we compare with X?
	Sources           = "sources"            // which sources do AI engines trust?
	NegativeClaims    = "negative_claims"    // what does AI say that is wrong or negative?
	FixOutcomes       = "fix_outcomes"       // did the fixes we made work?
	SinceLastReport   = "since_last_report"  // what changed since the last report?
	AIReferrals       = "ai_referrals"       // are AI assistants sending us traffic?
	TopicOpportunity  = "topic_opportunity"  // which topics are worth the work?
)

// Analysis describes one analysis for the dashboard and for the planner.
type Analysis struct {
	Name     string `json:"name"`
	Question string `json:"question"`
	Needs    string `json:"needs"` // the data it reads, in plain words
}

// Catalogue is every analysis, in the order they are offered.
var Catalogue = []Analysis{
	{VisibilityChange, "Why did our AI visibility change?", "AI answers collected by Vellatry"},
	{CompetitorGaps, "Where are competitors beating us in AI answers?", "blindspots and answer signals"},
	{FixPriority, "What should we fix first?", "blindspots, site findings and fixes"},
	{TrafficChange, "Why did organic traffic change?", "Search Console"},
	{CompetitorCompare, "How do we compare with a competitor?", "AI answers and share of voice"},
	{Sources, "Which sources do AI engines trust for our topics?", "citations in AI answers"},
	{NegativeClaims, "What does AI say about us that is wrong or negative?", "AI answers and judgments"},
	{FixOutcomes, "Did the fixes we made work?", "fixes, Search Console and AI answers"},
	{SinceLastReport, "What changed since the last report?", "events, blindspots and rollups"},
	{AIReferrals, "Are AI assistants sending us traffic?", "Google Analytics 4"},
	{TopicOpportunity, "Which topics are worth the work?", "keyword research"},
}

// Known is what the router can fill slots from: the tenant's own names.
type Known struct {
	Engines     []string
	Competitors []string
	Topics      []TopicRef
}

// TopicRef is a topic the router can match a question to.
type TopicRef struct {
	ID   string
	Name string
}

// Context is where the question was asked from: the page, its date range and whatever
// the user had selected. It fills slots the words leave out.
type Context struct {
	Page       string `json:"page,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Engine     string `json:"engine,omitempty"`
	TopicID    string `json:"topic_id,omitempty"`
	Competitor string `json:"competitor,omitempty"`
	PromptID   string `json:"prompt_id,omitempty"`
	ReportID   string `json:"report_id,omitempty"`
}

// Slots are the parameters an analysis runs with.
type Slots struct {
	Engine     string    `json:"engine,omitempty"`
	Competitor string    `json:"competitor,omitempty"`
	TopicID    string    `json:"topic_id,omitempty"`
	Topic      string    `json:"topic,omitempty"`
	PromptID   string    `json:"prompt_id,omitempty"`
	From       time.Time `json:"-"`
	To         time.Time `json:"-"`
}

// Period is the range the slots cover.
func (s Slots) Period() Period { return Period{From: s.From, To: s.To} }

// Route is the router's decision.
type Route struct {
	Intent     string  `json:"intent"`
	Confidence float64 `json:"confidence"`
	Slots      Slots   `json:"slots"`
	Matched    string  `json:"matched,omitempty"` // the words that decided it
}

// Confident is the score a route needs before it answers without a model.
const Confident = 0.6

// phrases score a question against an intent. Longer phrases are worth more because
// they are less likely to match by accident.
var phrases = map[string][]string{
	VisibilityChange:  {"visibility", "mentioned", "mentions", "share of voice", "ai answers", "appear in ai", "visibility drop", "visibility fell"},
	CompetitorGaps:    {"beating us", "ahead of us", "losing to", "competitors win", "competitor wins", "where are competitors", "displac", "recommend a competitor", "competitors beat"},
	FixPriority:       {"fix first", "what should we fix", "what should we do", "priorit", "next step", "biggest opportunit", "where to start", "what to work on"},
	TrafficChange:     {"organic traffic", "search traffic", "clicks", "impressions", "traffic change", "traffic drop", "traffic fell", "search console", "google traffic", "rankings"},
	CompetitorCompare: {"compare", "versus", " vs ", "how do we stack", "side by side", "against "},
	Sources:           {"sources", "cited", "citation", "which sites", "which domains", "where do they get", "trusted sites", "referenced"},
	NegativeClaims:    {"negative", "wrong", "incorrect", "inaccurate", "bad things", "say about us", "sentiment", "misleading", "out of date"},
	FixOutcomes:       {"did the fix", "did our fixes", "did it work", "since we fixed", "impact of the fix", "fixes work", "worked"},
	SinceLastReport:   {"since the last report", "since last report", "what changed", "what's new", "what is new", "this week", "latest changes"},
	AIReferrals:       {"referral", "sending us traffic", "traffic from chatgpt", "traffic from ai", "assistants sending", "ai traffic", "visits from ai"},
	TopicOpportunity:  {"which topics", "topic opportunit", "keyword opportunit", "what topics", "worth the work", "biggest topics", "keyword research"},
}

// DefaultDays is the period an analysis covers when the question and the page say
// nothing: four weeks, long enough for AI answers to add up.
const DefaultDays = 28

// Pick chooses the analysis a question asks for and fills its parameters. An empty
// intent means nobody is confident enough, and the question goes to the planner.
func Pick(question string, ctx Context, known Known, now time.Time) Route {
	q := " " + strings.ToLower(strings.TrimSpace(question)) + " "
	type scored struct {
		intent  string
		score   float64
		matched string
	}
	var all []scored
	for intent, list := range phrases {
		best, matched := 0.0, ""
		for _, p := range list {
			if strings.Contains(q, p) {
				// Longer phrases are stronger evidence than a single common word.
				s := 0.4 + float64(len(strings.Fields(p)))*0.2
				if s > best {
					best, matched = s, p
				}
			}
		}
		if best > 0 {
			all = append(all, scored{intent, best, matched})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].intent < all[j].intent
	})

	r := Route{Slots: fillSlots(q, ctx, known, now)}
	if len(all) == 0 {
		return r
	}
	r.Intent, r.Confidence, r.Matched = all[0].intent, all[0].score, all[0].matched
	// A question naming a competitor and asking to compare is a comparison, not a gap.
	if r.Slots.Competitor != "" && r.Intent == CompetitorGaps && strings.Contains(q, "compare") {
		r.Intent = CompetitorCompare
	}
	if len(all) > 1 && all[1].score == all[0].score {
		r.Confidence -= 0.2 // two analyses fit equally well: let the planner choose
	}
	if r.Confidence > 1 {
		r.Confidence = 1
	}
	return r
}

func fillSlots(q string, ctx Context, known Known, now time.Time) Slots {
	s := Slots{Engine: ctx.Engine, TopicID: ctx.TopicID, Competitor: ctx.Competitor, PromptID: ctx.PromptID}
	for _, e := range known.Engines {
		if strings.Contains(q, strings.ToLower(EngineName(e))) || strings.Contains(q, e) {
			s.Engine = e
		}
	}
	if strings.Contains(q, "ai overview") || strings.Contains(q, "ai mode") {
		s.Engine = "ai_overview"
	}
	for _, c := range known.Competitors {
		if c != "" && strings.Contains(q, " "+strings.ToLower(c)) {
			s.Competitor = c
		}
	}
	for _, t := range known.Topics {
		if t.Name != "" && strings.Contains(q, strings.ToLower(t.Name)) {
			s.TopicID, s.Topic = t.ID, t.Name
		}
	}
	s.From, s.To = period(q, ctx, now)
	return s
}

// period reads a date range from the question, else from the page the user is on, else
// the last four weeks.
func period(q string, ctx Context, now time.Time) (time.Time, time.Time) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	switch {
	case strings.Contains(q, "last month"):
		first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
		return first, first.AddDate(0, 1, -1)
	case strings.Contains(q, "this month"):
		return time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC), yesterday
	case strings.Contains(q, "last week"), strings.Contains(q, "this week"), strings.Contains(q, "7 days"), strings.Contains(q, "seven days"):
		return today.AddDate(0, 0, -7), yesterday
	case strings.Contains(q, "90 days"), strings.Contains(q, "quarter"), strings.Contains(q, "three months"):
		return today.AddDate(0, 0, -90), yesterday
	case strings.Contains(q, "30 days"), strings.Contains(q, "month"):
		return today.AddDate(0, 0, -30), yesterday
	case strings.Contains(q, "year"):
		return today.AddDate(0, 0, -365), yesterday
	}
	from, errF := time.Parse(time.DateOnly, ctx.From)
	to, errT := time.Parse(time.DateOnly, ctx.To)
	if errF == nil && errT == nil && !to.Before(from) {
		return from, to
	}
	return today.AddDate(0, 0, -DefaultDays), yesterday
}
