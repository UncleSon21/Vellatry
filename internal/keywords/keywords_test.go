package keywords

import (
	"strings"
	"testing"
)

func kw(name string, volume int, urls ...string) Keyword {
	return Keyword{Keyword: name, Volume: volume, URLs: urls}
}

func TestNormaliseURL(t *testing.T) {
	same := []string{"https://www.koala.com/mattress/", "http://koala.com/mattress", "https://KOALA.com/mattress?utm=1#top"}
	for _, u := range same {
		if got := NormaliseURL(u); got != "koala.com/mattress" {
			t.Errorf("NormaliseURL(%q) = %q", u, got)
		}
	}
}

func TestGroupsBySharedResults(t *testing.T) {
	a := []string{"a.com/1", "b.com/2", "c.com/3", "d.com/4", "e.com/5"}
	// Same four results: one topic.
	twin := []string{"a.com/1", "b.com/2", "c.com/3", "d.com/4", "z.com/9"}
	// Only three shared with the hub: its own topic at the strict setting.
	cousin := []string{"a.com/1", "b.com/2", "c.com/3", "y.com/8", "x.com/7"}
	ks := []Keyword{
		kw("mattress in a box", 5400, a...),
		kw("bed in a box", 2900, twin...),
		kw("mattress topper", 1600, cousin...),
	}
	got := Group(ks, 4)
	if len(got) != 2 {
		t.Fatalf("clusters = %d, want 2: %+v", len(got), got)
	}
	if got[0].Name != "mattress in a box" || strings.Join(got[0].Keywords, "|") != "bed in a box|mattress in a box" {
		t.Errorf("first cluster = %+v; the highest-volume keyword names it", got[0])
	}
	if got[0].Volume != 8300 {
		t.Errorf("volume = %d, want the sum", got[0].Volume)
	}
	if got[1].Name != "mattress topper" {
		t.Errorf("second cluster = %+v", got[1])
	}
	// At the looser setting the cousin joins.
	if loose := Group(ks, 3); len(loose) != 1 {
		t.Errorf("at three shared results, clusters = %d, want 1", len(loose))
	}
}

func TestGroupDoesNotChain(t *testing.T) {
	// A shares four with B, B shares four with C, but A and C share only two. Chaining
	// would put all three together and swallow the site.
	ks := []Keyword{
		kw("a", 1000, "1", "2", "3", "4", "9"),
		kw("b", 900, "1", "2", "3", "4", "5", "6", "7", "8"),
		kw("c", 800, "5", "6", "7", "8", "9"),
	}
	got := Group(ks, 4)
	if len(got) != 2 {
		t.Fatalf("clusters = %+v, want a and b together and c apart", got)
	}
	if strings.Join(got[0].Keywords, "|") != "a|b" || got[1].Name != "c" {
		t.Errorf("clusters = %+v", got)
	}
}

func TestClusterIntentAndDifficulty(t *testing.T) {
	d := func(v int) *int { return &v }
	urls := []string{"1", "2", "3", "4"}
	ks := []Keyword{
		{Keyword: "buy mattress", Volume: 1000, Intent: "transactional", Difficulty: d(40), URLs: urls, Features: []string{"shopping"}},
		{Keyword: "mattress deals", Volume: 500, Intent: "commercial", Difficulty: d(60), URLs: urls, Features: []string{"shopping"}},
	}
	c := Group(ks, 4)[0]
	if c.Intent != "transactional" || *c.Difficulty != 60 || strings.Join(c.Features, ",") != "shopping" {
		t.Errorf("cluster = %+v", c)
	}
	// With no stated intent, shopping results still mean people are buying.
	plain := []Keyword{
		{Keyword: "mattress sale", Volume: 800, URLs: urls, Features: []string{"shopping"}},
		{Keyword: "cheap mattress", Volume: 600, URLs: urls, Features: []string{"shopping"}},
	}
	if got := Group(plain, 4)[0].Intent; got != "transactional" {
		t.Errorf("intent from results = %q", got)
	}
}

func TestScore(t *testing.T) {
	d := func(v int) *int { return &v }
	c := Cluster{Volume: 10000, Intent: "transactional", Difficulty: d(50)}
	o := Score(c, 0, 25) // ranking at 25: almost no clicks today
	if o.Potential != 1000 {
		t.Errorf("potential = %v, want volume x the target's click share", o.Potential)
	}
	if o.Current != 50 || o.Gap != 950 {
		t.Errorf("current = %v, gap = %v", o.Current, o.Gap)
	}
	if o.Ease != 0.7 || o.IntentWeight != 1 || o.Score != 665 {
		t.Errorf("score = %+v", o)
	}
	// Already at the target: nothing left to win.
	if got := Score(c, 1000, 3); got.Gap != 0 || got.Score != 0 {
		t.Errorf("no gap expected: %+v", got)
	}
	// Informational demand is worth less than someone ready to buy.
	info := Score(Cluster{Volume: 10000, Intent: "informational"}, 0, 0)
	if info.IntentWeight != 0.7 || info.Score <= 0 {
		t.Errorf("informational = %+v", info)
	}
}

func TestPageTypeAndMatch(t *testing.T) {
	cases := map[string]string{
		"https://koala.com/":                     PageHome,
		"https://koala.com/products/mattress":    PageProduct,
		"https://koala.com/collections/mattress": PageCategory,
		"https://koala.com/blog/how-to-choose":   PageArticle,
		"https://koala.com/about-us":             PageOther,
	}
	for u, want := range cases {
		if got := PageType(u); got != want {
			t.Errorf("PageType(%q) = %q, want %q", u, got, want)
		}
	}
	if Match("mattress in a box", "https://koala.com/products/mattress-in-a-box", "", "") < 1 {
		t.Error("a slug carrying every word should match fully")
	}
	if Match("mattresses", "https://koala.com/x", "Our mattress range", "") < 1 {
		t.Error("plurals should match their singular")
	}
	if Match("sofa bed", "https://koala.com/products/mattress", "Mattress", "") > 0 {
		t.Error("an unrelated page should not match")
	}
}

func TestMapPageAndIssues(t *testing.T) {
	c := Cluster{Name: "mattress in a box", Keywords: []string{"mattress in a box", "bed in a box"}, Intent: "transactional",
		URLs: []string{"ecosa.com.au/mattress", "koala.com/products/mattress-in-a-box"}}
	pages := []CrawledPage{
		{URL: "https://koala.com/blog/mattress-in-a-box-guide", Title: "Mattress in a box guide", Status: 200},
		{URL: "https://koala.com/products/mattress-in-a-box", Title: "Mattress in a box", Status: 200},
	}

	// What Google already ranks wins.
	sc := map[string][]PageStat{
		"mattress in a box": {{Page: "https://koala.com/products/mattress-in-a-box", Clicks: 40, Impressions: 900}},
		"bed in a box":      {{Page: "https://koala.com/products/mattress-in-a-box", Clicks: 10, Impressions: 300}},
	}
	m := MapPage(c, sc, "koala.com", pages)
	if m.URL != "https://koala.com/products/mattress-in-a-box" || m.Source != FromSearchConsole || m.Kind != PageProduct {
		t.Fatalf("mapping = %+v", m)
	}
	if len(Issues(c, m, sc)) != 0 {
		t.Errorf("a product page for buying searches is right: %+v", Issues(c, m, sc))
	}

	// Without Search Console, the results we fetched show where the brand ranks.
	m = MapPage(c, nil, "koala.com", pages)
	if m.URL != "https://koala.com/products/mattress-in-a-box" || m.Source != FromRanking {
		t.Errorf("mapping from results = %+v", m)
	}

	// With neither, the closest page by its words, and an intent mismatch is flagged.
	blogOnly := []CrawledPage{{URL: "https://koala.com/blog/mattress-in-a-box-guide", Title: "Mattress in a box guide", Status: 200}}
	m = MapPage(Cluster{Keywords: c.Keywords, Intent: "transactional"}, nil, "koala.com", blogOnly)
	if m.Source != FromMatch || m.Kind != PageArticle {
		t.Fatalf("mapping by words = %+v", m)
	}
	issues := Issues(Cluster{Keywords: c.Keywords, Intent: "transactional"}, m, nil)
	if len(issues) != 1 || issues[0].Kind != IssueIntentMismatch {
		t.Errorf("issues = %+v", issues)
	}

	// Nothing ranks, nothing matches: the topic needs a page that does not exist.
	unknown := Cluster{Keywords: []string{"cot mattress protector"}, Intent: "transactional", URLs: []string{"ecosa.com.au/mattress"}}
	if m := MapPage(unknown, nil, "koala.com", pages); m.URL != "" {
		t.Errorf("mapping = %+v, want none", m)
	}
	if got := Issues(c, Mapping{}, nil); len(got) != 1 || got[0].Kind != IssueNoPage {
		t.Errorf("issues = %+v", got)
	}

	// Two of our own pages competing for the same searches.
	split := map[string][]PageStat{"mattress in a box": {
		{Page: "https://koala.com/products/mattress-in-a-box", Clicks: 10, Impressions: 500},
		{Page: "https://koala.com/blog/mattress-in-a-box-guide", Clicks: 2, Impressions: 500},
	}}
	got := Issues(c, MapPage(c, split, "koala.com", pages), split)
	if len(got) != 1 || got[0].Kind != IssueCannibalised || len(got[0].Pages) != 2 {
		t.Errorf("cannibalisation = %+v", got)
	}
}

func TestReject(t *testing.T) {
	comps := []string{"Ecosa", "Sleeping Duck"}
	for _, k := range []string{"ecosa", "ecosa mattress review", "best sleeping duck", "is ecosa good"} {
		if reject, reason := Reject(k, comps); !reject || reason != RejectCompetitorBrand {
			t.Errorf("%q should be rejected as a competitor's own search", k)
		}
	}
	for _, k := range []string{"mattress in a box", "best mattress australia", "koala mattress"} {
		if reject, _ := Reject(k, comps); reject {
			t.Errorf("%q should be kept", k)
		}
	}
	if reject, reason := Reject(strings.Repeat("a b ", 20), comps); !reject || reason != RejectTooLong {
		t.Error("a very long phrase is not a real search")
	}
}
