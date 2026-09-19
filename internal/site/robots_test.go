package site

import "testing"

const robotsTxt = `
# comment
User-agent: *
Disallow: /admin
Allow: /admin/public
Disallow: /*.pdf$

User-agent: GPTBot
User-agent: CCBot
Disallow: /

User-agent: OAI-SearchBot
Disallow:

Sitemap: https://koala.com/sitemap.xml
`

func TestRobotsAllowed(t *testing.T) {
	r := ParseRobots(robotsTxt)
	cases := []struct {
		agent, path string
		want        bool
	}{
		{"Googlebot", "/", true},
		{"Googlebot", "/admin", false},
		{"Googlebot", "/admin/settings", false},
		{"Googlebot", "/admin/public/page", true}, // longer allow wins
		{"Googlebot", "/files/report.pdf", false},
		{"Googlebot", "/files/report.pdf?x=1", true}, // $ anchors the end
		{"GPTBot", "/", false},
		{"gptbot", "/anything", false}, // case-insensitive agents
		{"CCBot", "/", false},
		{"OAI-SearchBot", "/admin", true}, // its own group, which allows everything
	}
	for _, c := range cases {
		if got := r.Allowed(c.agent, c.path); got != c.want {
			t.Errorf("Allowed(%s, %s) = %v, want %v", c.agent, c.path, got, c.want)
		}
	}
	if len(r.Sitemaps) != 1 || r.Sitemaps[0] != "https://koala.com/sitemap.xml" {
		t.Errorf("sitemaps = %v", r.Sitemaps)
	}
	if !(&Robots{Missing: true}).Allowed("GPTBot", "/") {
		t.Error("no robots.txt allows everything")
	}
}

func TestAIAccess(t *testing.T) {
	access := AIAccess(ParseRobots(robotsTxt))
	byAgent := map[string]BotAccess{}
	for _, a := range access {
		byAgent[a.Agent] = a
	}
	if !byAgent["GPTBot"].Blocked || byAgent["OAI-SearchBot"].Blocked || byAgent["PerplexityBot"].Blocked {
		t.Errorf("access = %+v", access)
	}
	if access[0].Purpose != "search" {
		t.Error("search bots should be listed first")
	}
}
