package site

import (
	"strings"
	"testing"
)

func rules(fs []Finding) map[string]Finding {
	m := map[string]Finding{}
	for _, f := range fs {
		m[f.Rule+"|"+f.URL] = f
	}
	return m
}

func TestAudit(t *testing.T) {
	facts := SiteFacts{Home: "https://koala.com/", Robots: ParseRobots("User-agent: OAI-SearchBot\nDisallow: /\nUser-agent: GPTBot\nDisallow: /"), RobotsFound: true}
	good := Page{URL: "https://koala.com/", Status: 200, ContentType: "text/html", Title: "Koala: mattresses delivered in 4 hours",
		MetaDescription: "Australian-made mattresses with free delivery in four hours and a 120-night trial on every order.",
		H1:              []string{"Mattresses"}, Headings: []int{1, 2}, Canonical: []string{"https://koala.com/"}, Lang: "en", Viewport: true,
		OpenGraph: map[string]string{"og:title": "x", "og:description": "y"}, WordCount: 400}
	dupA := Page{URL: "https://koala.com/a", Status: 200, ContentType: "text/html; charset=utf-8", Title: "Short", H1: []string{"A", "B"},
		Headings: []int{1, 3}, Images: 2, ImagesNoAltN: 1, ImagesNoAlt: []string{"https://koala.com/x.jpg"}, WordCount: 50, Scripts: 4,
		JSONLD: []JSONLD{{Valid: false, Error: "bad"}}}
	dupB := Page{URL: "https://koala.com/b", Status: 200, ContentType: "text/html", Title: "Short", Noindex: true}
	broken := Page{URL: "https://koala.com/gone", Status: 404}

	fs := Audit(facts, []Page{good, dupA, dupB, broken})
	got := rules(fs)
	want := []string{
		"robots_blocks_search_bot|", "robots_blocks_training_bot|", "llms_txt_missing|", "sitemap_missing|",
		"status_error|https://koala.com/gone", "noindex|https://koala.com/b",
		"title_length|https://koala.com/a", "meta_description_missing|https://koala.com/a", "h1_multiple|https://koala.com/a",
		"heading_skip|https://koala.com/a", "img_alt_missing|https://koala.com/a", "jsonld_invalid|https://koala.com/a",
		"thin_server_content|https://koala.com/a", "viewport_missing|https://koala.com/a", "organization_schema_missing|https://koala.com/",
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("missing finding %s", w)
		}
	}
	// A noindexed page is not also flagged for its title, and the duplicate-title rule
	// only counts indexable pages.
	if _, ok := got["title_duplicate|https://koala.com/a"]; ok {
		t.Error("noindexed page counted in duplicate titles")
	}
	if _, ok := got["title_length|https://koala.com/b"]; ok {
		t.Error("noindexed page audited for its title")
	}
	if fs[0].Severity != Critical {
		t.Errorf("findings not sorted by severity: first is %s", fs[0].Severity)
	}
	// Fingerprints are stable across runs and distinct per page.
	again := rules(Audit(facts, []Page{good, dupA, dupB, broken}))
	for k, f := range got {
		if again[k].Fingerprint != f.Fingerprint {
			t.Errorf("unstable fingerprint for %s", k)
		}
	}
}

func TestFixes(t *testing.T) {
	b := BrandFacts{Name: "Koala", Domain: "koala.com", Summary: "Mattresses delivered fast.",
		Pages: []Page{{URL: "https://koala.com/", Title: "Koala", MetaDescription: "Home", Status: 200}, {URL: "https://koala.com/x", Title: "Gone", Status: 404}}}
	f, ok := FixFor(Finding{Rule: "robots_blocks_search_bot", Detail: map[string]any{"agent": "OAI-SearchBot", "product": "ChatGPT search"}}, b)
	if !ok || f.Snippet != "User-agent: OAI-SearchBot\nAllow: /" {
		t.Errorf("robots fix = %+v", f)
	}
	llms, _ := FixFor(Finding{Rule: "llms_txt_missing"}, b)
	if !strings.HasPrefix(llms.Snippet, "# Koala\n\n> Mattresses delivered fast.") || strings.Contains(llms.Snippet, "Gone") {
		t.Errorf("llms.txt = %q", llms.Snippet)
	}
	org, _ := FixFor(Finding{Rule: "organization_schema_missing"}, b)
	if !strings.Contains(org.Snippet, `"@type": "Organization"`) || !strings.Contains(org.Snippet, "https://koala.com/") {
		t.Errorf("organization = %s", org.Snippet)
	}
	if _, ok := FixFor(Finding{Rule: "heading_skip"}, b); ok {
		t.Error("heading_skip needs judgement; no generated fix expected")
	}
}
