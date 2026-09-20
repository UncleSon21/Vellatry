// Package keywords turns search demand into topics: clustering by shared search
// results, naming, intent, page mapping and the opportunity score. Every step is code,
// with no LLM call anywhere: the clustering signal is Google's own results, which is
// both cheaper and steadier than asking a model whether two phrases mean the same thing.
package keywords

import (
	"math"
	"net/url"
	"sort"
	"strings"
)

// MinSharedURLs is how many of the top ten results two keywords must share to belong to
// the same topic. Three is the usual floor; four is stricter and splits big clusters.
const MinSharedURLs = 4

// Keyword is one keyword with everything clustering and scoring need.
type Keyword struct {
	Keyword       string
	Volume        int
	Difficulty    *int
	Intent        string
	URLs          []string // the top organic results, in order
	Features      []string // other result types on the page: shopping, local_pack...
	SCClicks      int64    // Search Console, the period we hold
	SCImpressions int64
	SCPosition    float64
}

// Cluster is a group of keywords that share search results: one topic.
type Cluster struct {
	Name       string   `json:"name"` // the highest-volume keyword in the group
	Keywords   []string `json:"keywords"`
	Volume     int      `json:"volume"`     // monthly searches, summed
	Difficulty *int     `json:"difficulty"` // median of the keywords that have one
	Intent     string   `json:"intent"`
	Features   []string `json:"features"` // result types present on most of the keywords
	URLs       []string `json:"urls"`     // the results the group shares, most common first
}

// NormaliseURL strips the parts that do not identify a page, so two results only differ
// when they really are different pages.
func NormaliseURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	host := strings.ToLower(strings.TrimPrefix(u.Host, "www."))
	path := strings.TrimSuffix(u.Path, "/")
	return host + path
}

// Group clusters keywords that share at least minShared of their top-ten results.
//
// Keywords are taken highest volume first and each becomes the hub of a group; the rest
// join a hub only by their overlap with that hub, never through a chain of neighbours,
// which is what stops one big cluster swallowing a whole site.
func Group(ks []Keyword, minShared int) []Cluster {
	if minShared <= 0 {
		minShared = MinSharedURLs
	}
	type item struct {
		k    Keyword
		urls map[string]bool
	}
	items := make([]item, 0, len(ks))
	for _, k := range ks {
		if len(k.URLs) == 0 {
			continue // no search results: nothing to cluster on
		}
		set := make(map[string]bool, len(k.URLs))
		for _, u := range k.URLs {
			set[NormaliseURL(u)] = true
		}
		items = append(items, item{k, set})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].k.Volume != items[j].k.Volume {
			return items[i].k.Volume > items[j].k.Volume
		}
		return items[i].k.Keyword < items[j].k.Keyword
	})

	byURL := map[string][]int{}
	for i, it := range items {
		for u := range it.urls {
			byURL[u] = append(byURL[u], i)
		}
	}
	taken := make([]bool, len(items))
	var out []Cluster
	for i := range items {
		if taken[i] {
			continue
		}
		taken[i] = true
		members := []int{i}
		shared := map[int]int{}
		for u := range items[i].urls {
			for _, j := range byURL[u] {
				if j != i && !taken[j] {
					shared[j]++
				}
			}
		}
		candidates := make([]int, 0, len(shared))
		for j, n := range shared {
			if n >= minShared {
				candidates = append(candidates, j)
			}
		}
		sort.Ints(candidates) // highest volume first, since items are sorted
		for _, j := range candidates {
			taken[j] = true
			members = append(members, j)
		}
		group := make([]Keyword, len(members))
		for n, m := range members {
			group[n] = items[m].k
		}
		out = append(out, summarise(group))
	}
	return out
}

func summarise(group []Keyword) Cluster {
	c := Cluster{Name: group[0].Keyword}
	var difficulties []int
	intent := map[string]int{}
	features := map[string]int{}
	urls := map[string]int{}
	for _, k := range group {
		c.Keywords = append(c.Keywords, k.Keyword)
		c.Volume += k.Volume
		if k.Difficulty != nil {
			difficulties = append(difficulties, *k.Difficulty)
		}
		if k.Intent != "" {
			intent[k.Intent] += k.Volume + 1 // volume-weighted, but every keyword counts
		}
		for _, f := range k.Features {
			features[f]++
		}
		for _, u := range k.URLs {
			urls[NormaliseURL(u)]++
		}
	}
	sort.Strings(c.Keywords)
	if len(difficulties) > 0 {
		sort.Ints(difficulties)
		d := difficulties[len(difficulties)/2]
		c.Difficulty = &d
	}
	c.Intent = pickIntent(intent, features, len(group))
	for f, n := range features {
		if n*2 >= len(group) { // on at least half the keywords
			c.Features = append(c.Features, f)
		}
	}
	sort.Strings(c.Features)
	c.URLs = topKeys(urls, 10)
	return c
}

// pickIntent takes the volume-weighted vote, then lets the search results correct it:
// a shopping carousel or product results mean people are ready to buy.
func pickIntent(votes map[string]int, features map[string]int, n int) string {
	best, bestN := "", 0
	for i, v := range votes {
		if v > bestN || (v == bestN && i < best) {
			best, bestN = i, v
		}
	}
	if features["shopping"]*2 >= n && (best == "commercial" || best == "") {
		return "transactional"
	}
	if best == "" {
		if features["people_also_ask"]*2 >= n {
			return "informational"
		}
		return "commercial"
	}
	return best
}

func topKeys(m map[string]int, limit int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > limit {
		keys = keys[:limit]
	}
	return keys
}

// ---- opportunity ----------------------------------------------------------------------

// ctrCurve is the share of clicks a result at each position takes, position 1 first.
// It is a published-average curve, not a promise: the report shows the components.
var ctrCurve = []float64{0.28, 0.15, 0.10, 0.07, 0.05, 0.04, 0.03, 0.025, 0.02, 0.018}

// TargetPosition is the position the potential is measured against.
const TargetPosition = 3

// CTRAt returns the expected click-through rate at a position.
func CTRAt(pos float64) float64 {
	switch {
	case pos < 1:
		return 0
	case pos <= float64(len(ctrCurve)):
		return ctrCurve[int(math.Round(pos))-1]
	case pos <= 20:
		return 0.01
	case pos <= 30:
		return 0.005
	}
	return 0
}

var intentWeight = map[string]float64{"transactional": 1, "commercial": 0.9, "informational": 0.7, "navigational": 0.4}

// Opportunity is the score with every component visible, so nobody has to trust a
// single number.
type Opportunity struct {
	Score        float64 `json:"score"`     // monthly clicks worth chasing, adjusted
	Volume       int     `json:"volume"`    // monthly searches
	Potential    float64 `json:"potential"` // clicks a month at the target position
	Current      float64 `json:"current"`   // clicks a month now
	Gap          float64 `json:"gap"`       // potential minus current
	Difficulty   *int    `json:"difficulty"`
	Ease         float64 `json:"ease"`          // 1 at difficulty 0, 0.4 at 100
	IntentWeight float64 `json:"intent_weight"` // by intent
	Target       int     `json:"target"`        // the position the potential assumes
}

// Score rates a cluster. currentClicks is what the brand gets from these keywords a
// month now (Search Console); when there is none, its current position estimates it.
func Score(c Cluster, currentClicks float64, currentPosition float64) Opportunity {
	o := Opportunity{Volume: c.Volume, Difficulty: c.Difficulty, Target: TargetPosition, IntentWeight: intentWeight[c.Intent]}
	if o.IntentWeight == 0 {
		o.IntentWeight = 0.7
	}
	o.Potential = float64(c.Volume) * CTRAt(TargetPosition)
	switch {
	case currentClicks > 0:
		o.Current = currentClicks
	case currentPosition > 0:
		o.Current = float64(c.Volume) * CTRAt(currentPosition)
	}
	o.Gap = math.Max(0, o.Potential-o.Current)
	o.Ease = 1
	if c.Difficulty != nil {
		o.Ease = 1 - 0.6*float64(*c.Difficulty)/100
	}
	o.Score = math.Round(o.Gap*o.Ease*o.IntentWeight*10) / 10
	o.Potential = math.Round(o.Potential*10) / 10
	o.Current = math.Round(o.Current*10) / 10
	o.Gap = math.Round(o.Gap*10) / 10
	return o
}

// ---- pages -----------------------------------------------------------------------------

// Page types, decided by URL shape (a small classifier replaces this later).
const (
	PageHome     = "home"
	PageProduct  = "product"
	PageCategory = "category"
	PageArticle  = "article"
	PageOther    = "other"
)

var pagePatterns = []struct {
	kind  string
	parts []string
}{
	{PageProduct, []string{"/product/", "/products/", "/p/", "/item/", "/buy/"}},
	{PageCategory, []string{"/collections/", "/category/", "/categories/", "/shop/", "/range/", "/c/"}},
	{PageArticle, []string{"/blog/", "/news/", "/guide/", "/guides/", "/article/", "/articles/", "/learn/", "/resources/", "/advice/", "/how-to/"}},
}

// PageType guesses what a page is for from its URL.
func PageType(raw string) string {
	u, err := url.Parse(raw)
	path := raw
	if err == nil && u.Host != "" {
		path = u.Path
	}
	path = strings.ToLower(path)
	if path == "" || path == "/" {
		return PageHome
	}
	padded := path
	if !strings.HasSuffix(padded, "/") {
		padded += "/"
	}
	for _, p := range pagePatterns {
		for _, part := range p.parts {
			if strings.Contains(padded, part) {
				return p.kind
			}
		}
	}
	return PageOther
}

// Match scores how well a page's words answer a keyword, for the case where Search
// Console has no ranking page yet. A trained ranker replaces this later.
func Match(keyword string, pageURL, title, h1 string) float64 {
	want := tokens(keyword)
	if len(want) == 0 {
		return 0
	}
	have := map[string]bool{}
	for _, s := range []string{title, h1, slugWords(pageURL)} {
		for _, t := range tokens(s) {
			have[t] = true
		}
	}
	hit := 0
	for _, t := range want {
		if have[t] {
			hit++
		}
	}
	return float64(hit) / float64(len(want))
}

var stopWords = map[string]bool{"the": true, "a": true, "an": true, "and": true, "or": true, "for": true, "of": true,
	"to": true, "in": true, "on": true, "with": true, "best": true, "top": true, "vs": true, "near": true, "me": true, "au": true}

func tokens(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(f) > 2 && !stopWords[f] {
			out = append(out, singular(f))
		}
	}
	return out
}

// singular trims a trailing plural s, so "mattresses" and "mattress" match.
func singular(w string) string {
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "ses") && len(w) > 4:
		return w[:len(w)-2]
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 3:
		return w[:len(w)-1]
	}
	return w
}

func slugWords(raw string) string {
	u, err := url.Parse(raw)
	path := raw
	if err == nil && u.Host != "" {
		path = u.Path
	}
	return strings.NewReplacer("/", " ", "-", " ", "_", " ", ".html", " ").Replace(path)
}

// IntentSuitsPage reports whether a mapped page matches what the searcher wants, and
// says what is wrong when it does not.
func IntentSuitsPage(intent, pageKind string) (bool, string) {
	switch {
	case intent == "informational" && (pageKind == PageProduct || pageKind == PageCategory):
		return false, "these searches want an explanation, but the mapped page sells"
	case (intent == "transactional" || intent == "commercial") && pageKind == PageArticle:
		return false, "these searches are ready to buy, but the mapped page is an article"
	}
	return true, ""
}
