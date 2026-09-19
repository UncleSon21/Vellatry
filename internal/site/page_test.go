package site

import (
	"net/url"
	"testing"
)

const sample = `<!doctype html>
<html lang="en-AU"><head>
<title>  Koala Mattress | Australia  </title>
<meta name="description" content="Free delivery in 4 hours.">
<meta name="viewport" content="width=device-width">
<meta name="robots" content="index, follow">
<meta property="og:title" content="Koala">
<link rel="canonical" href="/mattress">
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"Organization","name":"Koala"},{"@type":["Product","Thing"]}]}</script>
<script type="application/ld+json">{ broken </script>
<script>var hidden = "not visible text";</script>
<style>.x{}</style>
</head><body>
<h1>Mattress</h1><h3>Skipped a level</h3><h2>Delivery</h2>
<p>Our mattress ships fast across Australia.</p>
<img src="/a.jpg" alt="A mattress"><img src="/b.jpg"><img src="/c.jpg" alt="">
<a href="/sofa#reviews">Sofa</a><a href="https://www.koala.com/bed">Bed</a>
<a href="https://other.com/x">Other</a><a href="mailto:hi@koala.com">Mail</a>
</body></html>`

func TestParseHTML(t *testing.T) {
	base, _ := url.Parse("https://koala.com/mattress")
	p := &Page{URL: base.String()}
	ParseHTML(p, base, []byte(sample))

	if p.Title != "Koala Mattress | Australia" || p.TitleCount != 1 || p.Lang != "en-AU" || !p.Viewport || p.Noindex {
		t.Errorf("head = %+v", p)
	}
	if p.MetaDescription != "Free delivery in 4 hours." {
		t.Errorf("meta = %q", p.MetaDescription)
	}
	if len(p.Canonical) != 1 || p.Canonical[0] != "https://koala.com/mattress" {
		t.Errorf("canonical = %v", p.Canonical)
	}
	if len(p.H1) != 1 || p.H1[0] != "Mattress" || len(p.Headings) != 3 || p.Headings[1] != 3 {
		t.Errorf("headings = %v %v", p.H1, p.Headings)
	}
	// alt="" is a valid decorative image; only a missing alt attribute is a finding.
	if p.Images != 3 || p.ImagesNoAltN != 1 || p.ImagesNoAlt[0] != "https://koala.com/b.jpg" {
		t.Errorf("images = %d, no alt %v", p.Images, p.ImagesNoAlt)
	}
	if len(p.JSONLD) != 2 || !p.JSONLD[0].Valid || len(p.JSONLD[0].Types) != 3 || p.JSONLD[1].Valid {
		t.Errorf("json-ld = %+v", p.JSONLD)
	}
	if len(p.Links) != 2 || p.Links[0] != "https://koala.com/sofa" || p.Links[1] != "https://www.koala.com/bed" {
		t.Errorf("links = %v", p.Links)
	}
	if p.WordCount == 0 || p.WordCount > 20 {
		t.Errorf("word count = %d (script and style text must not count)", p.WordCount)
	}
	if p.OpenGraph["og:title"] != "Koala" || p.Scripts != 1 || p.ContentHash == "" {
		t.Errorf("og/scripts/hash = %v %d %q", p.OpenGraph, p.Scripts, p.ContentHash)
	}
}
