package keywords

import (
	"fmt"
	"sort"
	"strings"
)

// PageStat is one page's Search Console performance for one keyword.
type PageStat struct {
	Page        string
	Clicks      int64
	Impressions int64
}

// CrawledPage is a page of the brand's own site, from the crawl.
type CrawledPage struct {
	URL     string
	Title   string
	H1      string
	Status  int
	Noindex bool
}

// Mapping is the page that should own a topic.
type Mapping struct {
	URL    string `json:"url"`
	Source string `json:"source"` // search_console | ranking | match | ""
	Kind   string `json:"kind"`
}

// Page-mapping sources.
const (
	FromSearchConsole = "search_console" // Google already ranks this page for these searches
	FromRanking       = "ranking"        // the brand appears in the results we fetched
	FromMatch         = "match"          // the closest page on the site by its words
)

// MinMatch is how much of a keyword's words a page must carry to be mapped by words
// alone.
const MinMatch = 0.5

// MapPage picks the page that should own a cluster: what Google already ranks first,
// then what shows in the results we fetched, then the closest page on the site. An
// empty URL means the topic needs a page that does not exist yet.
func MapPage(c Cluster, sc map[string][]PageStat, brandDomain string, pages []CrawledPage) Mapping {
	if page := bestSearchConsolePage(c, sc); page != "" {
		return Mapping{URL: page, Source: FromSearchConsole, Kind: PageType(page)}
	}
	if brandDomain != "" {
		host := strings.ToLower(strings.TrimPrefix(brandDomain, "www."))
		for _, u := range c.URLs { // already normalised, most common first
			if u == host || strings.HasPrefix(u, host+"/") {
				return Mapping{URL: "https://" + u, Source: FromRanking, Kind: PageType(u)}
			}
		}
	}
	best, bestScore := "", MinMatch
	for _, p := range pages {
		if p.Status != 200 || p.Noindex {
			continue
		}
		score := 0.0
		for _, k := range c.Keywords {
			score += Match(k, p.URL, p.Title, p.H1)
		}
		score /= float64(len(c.Keywords))
		if score > bestScore || (score == bestScore && p.URL < best) {
			best, bestScore = p.URL, score
		}
	}
	if best != "" {
		return Mapping{URL: best, Source: FromMatch, Kind: PageType(best)}
	}
	return Mapping{}
}

func bestSearchConsolePage(c Cluster, sc map[string][]PageStat) string {
	totals := map[string]*PageStat{}
	for _, k := range c.Keywords {
		for _, s := range sc[k] {
			t := totals[s.Page]
			if t == nil {
				t = &PageStat{Page: s.Page}
				totals[s.Page] = t
			}
			t.Clicks += s.Clicks
			t.Impressions += s.Impressions
		}
	}
	best := ""
	var bestStat PageStat
	for page, t := range totals {
		if t.Clicks > bestStat.Clicks || (t.Clicks == bestStat.Clicks && t.Impressions > bestStat.Impressions) ||
			(t.Clicks == bestStat.Clicks && t.Impressions == bestStat.Impressions && best != "" && page < best) {
			best, bestStat = page, *t
		}
	}
	if bestStat.Impressions == 0 {
		return ""
	}
	return best
}

// Issue is something wrong with a topic that the team can act on.
type Issue struct {
	Kind   string   `json:"kind"`
	Detail string   `json:"detail"`
	Pages  []string `json:"pages,omitempty"`
}

// Issue kinds.
const (
	IssueNoPage         = "no_page"
	IssueIntentMismatch = "intent_mismatch"
	IssueCannibalised   = "cannibalised"
)

// CannibalShare is the share of a topic's impressions a second page needs before the
// two count as competing with each other.
const CannibalShare = 0.2

// Issues lists what is wrong with a mapped topic: no page to rank, a page that answers
// a different need, or two of the brand's own pages competing for the same searches.
func Issues(c Cluster, m Mapping, sc map[string][]PageStat) []Issue {
	var out []Issue
	if m.URL == "" {
		out = append(out, Issue{Kind: IssueNoPage, Detail: "no page on the site covers these searches yet"})
	} else if ok, why := IntentSuitsPage(c.Intent, m.Kind); !ok {
		out = append(out, Issue{Kind: IssueIntentMismatch, Detail: why, Pages: []string{m.URL}})
	}
	totals := map[string]int64{}
	var all int64
	for _, k := range c.Keywords {
		for _, s := range sc[k] {
			totals[s.Page] += s.Impressions
			all += s.Impressions
		}
	}
	if all == 0 {
		return out
	}
	var competing []string
	for page, imp := range totals {
		if float64(imp)/float64(all) >= CannibalShare {
			competing = append(competing, page)
		}
	}
	if len(competing) > 1 {
		sort.Strings(competing)
		out = append(out, Issue{Kind: IssueCannibalised,
			Detail: fmt.Sprintf("%d of your pages compete for these searches; Google has to choose", len(competing)), Pages: competing})
	}
	return out
}

// Relevance rules. A trained classifier replaces these once there are enough decisions
// to learn from; until then the rules only reject what is obviously not ours, and the
// team's own rejections are kept as the training data.
const (
	RejectCompetitorBrand = "a competitor's own brand search"
	RejectTooLong         = "too long to be a real search"
)

// Reject decides whether a keyword is worth measuring. competitors are the names and
// domains of the tracked competitors.
func Reject(keyword string, competitorNames []string) (bool, string) {
	k := strings.ToLower(strings.TrimSpace(keyword))
	if len(k) > 80 || len(strings.Fields(k)) > 12 {
		return true, RejectTooLong
	}
	for _, name := range competitorNames {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || len(name) < 3 {
			continue
		}
		if k == name || strings.HasPrefix(k, name+" ") || strings.HasSuffix(k, " "+name) || strings.Contains(k, " "+name+" ") {
			return true, RejectCompetitorBrand
		}
	}
	return false, ""
}
