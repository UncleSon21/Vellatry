// Package site audits a customer's website: robots.txt and AI-crawler access, llms.txt,
// sitemaps, and on-page SEO rules. Everything here is deterministic; no model runs.
package site

import (
	"bufio"
	"regexp"
	"sort"
	"strings"
)

// Robots is a parsed robots.txt.
type Robots struct {
	groups   []robotsGroup
	Sitemaps []string
	Missing  bool // no robots.txt (everything allowed)
}

type robotsGroup struct {
	agents []string // lower case
	rules  []robotsRule
}

type robotsRule struct {
	allow   bool
	pattern string
	re      *regexp.Regexp
}

// ParseRobots parses robots.txt following Google's rules: groups start with one or more
// user-agent lines; the most specific agent group applies; within it the longest
// matching rule wins and allow wins ties; * and $ are wildcards.
func ParseRobots(body string) *Robots {
	r := &Robots{}
	var cur *robotsGroup
	lastWasAgent := false
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "user-agent":
			if !lastWasAgent || cur == nil {
				r.groups = append(r.groups, robotsGroup{})
				cur = &r.groups[len(r.groups)-1]
			}
			cur.agents = append(cur.agents, strings.ToLower(val))
			lastWasAgent = true
		case "allow", "disallow":
			lastWasAgent = false
			if cur == nil {
				continue
			}
			if val == "" {
				continue // "Disallow:" with no path allows everything
			}
			cur.rules = append(cur.rules, robotsRule{allow: key == "allow", pattern: val, re: compileRobots(val)})
		case "sitemap":
			lastWasAgent = false
			if val != "" {
				r.Sitemaps = append(r.Sitemaps, val)
			}
		default:
			lastWasAgent = false
		}
	}
	return r
}

func compileRobots(p string) *regexp.Regexp {
	anchored := strings.HasSuffix(p, "$")
	p = strings.TrimSuffix(p, "$")
	var b strings.Builder
	b.WriteString("^")
	for _, part := range strings.Split(p, "*") {
		b.WriteString(regexp.QuoteMeta(part))
		b.WriteString(".*")
	}
	s := strings.TrimSuffix(b.String(), ".*")
	if anchored {
		s += "$"
	}
	return regexp.MustCompile(s)
}

// group returns the group for agent: the one whose user-agent is the longest prefix of
// the agent's product token, else "*".
func (r *Robots) group(agent string) *robotsGroup {
	agent = strings.ToLower(agent)
	var best *robotsGroup
	bestLen := -1
	for i := range r.groups {
		for _, a := range r.groups[i].agents {
			if a != "*" && strings.HasPrefix(agent, a) && len(a) > bestLen {
				best, bestLen = &r.groups[i], len(a)
			}
		}
	}
	if best != nil {
		return best
	}
	for i := range r.groups {
		for _, a := range r.groups[i].agents {
			if a == "*" {
				return &r.groups[i]
			}
		}
	}
	return nil
}

// Allowed reports whether agent may fetch path (path and query, starting with "/").
func (r *Robots) Allowed(agent, path string) bool {
	if r == nil || r.Missing {
		return true
	}
	g := r.group(agent)
	if g == nil {
		return true
	}
	if path == "" {
		path = "/"
	}
	bestLen, allowed := -1, true
	for _, rule := range g.rules {
		if !rule.re.MatchString(path) {
			continue
		}
		l := len(rule.pattern)
		if l > bestLen || (l == bestLen && rule.allow) {
			bestLen, allowed = l, rule.allow
		}
	}
	return allowed
}

// AIBot is a crawler that feeds an AI product.
type AIBot struct {
	Agent   string `json:"agent"`
	Product string `json:"product"`
	Purpose string `json:"purpose"` // "search" answers cite pages live; "training" builds models
}

// AIBots are the AI crawlers checked on every audit. Blocking the search agents keeps a
// site out of AI answers; blocking training agents is a legitimate choice we report but
// do not flag as an error.
var AIBots = []AIBot{
	{"OAI-SearchBot", "ChatGPT search", "search"},
	{"ChatGPT-User", "ChatGPT browsing", "search"},
	{"GPTBot", "OpenAI training", "training"},
	{"PerplexityBot", "Perplexity", "search"},
	{"Perplexity-User", "Perplexity browsing", "search"},
	{"Claude-SearchBot", "Claude search", "search"},
	{"Claude-User", "Claude browsing", "search"},
	{"ClaudeBot", "Anthropic training", "training"},
	{"Google-Extended", "Gemini training and grounding", "training"},
	{"Googlebot", "Google Search and AI Overviews", "search"},
	{"Bingbot", "Bing and Copilot", "search"},
	{"Applebot-Extended", "Apple Intelligence training", "training"},
	{"CCBot", "Common Crawl", "training"},
}

// BotAccess is one AI crawler's access to the site.
type BotAccess struct {
	AIBot
	Home    bool `json:"home"`    // may fetch the homepage
	Blocked bool `json:"blocked"` // blocked from the whole site
}

// AIAccess checks every AI bot against the homepage and the whole site.
func AIAccess(r *Robots) []BotAccess {
	out := make([]BotAccess, len(AIBots))
	for i, b := range AIBots {
		out[i] = BotAccess{AIBot: b, Home: r.Allowed(b.Agent, "/"), Blocked: !r.Allowed(b.Agent, "/")}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Purpose == "search" && out[j].Purpose != "search" })
	return out
}
