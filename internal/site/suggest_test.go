package site

import (
	"net/url"
	"strings"
	"testing"
)

func TestSuggestFromHomePage(t *testing.T) {
	base, _ := url.Parse("https://koala.com/")
	body := []byte(`<html><head><title>Koala</title>
	<script type="application/ld+json">{"@context":"https://schema.org","@graph":[
	  {"@type":"WebSite","name":"Koala"},
	  {"@type":"Organization","name":"Koala","legalName":"Koala Sleep Pty Ltd","alternateName":["Koala Mattress","Koala"],
	   "sameAs":["https://www.instagram.com/koala"]}]}</script></head>
	<body><header><a href="/">Home</a><a href="/mattresses">Mattresses</a><a href="/sofas">Sofa Beds</a>
	  <a href="/cart">Cart</a><a href="/about">About us</a></header>
	<nav><a href="/bedroom">Bedroom</a><a href="/mattresses">Mattresses</a><a href="/sale">Sale</a><a href="/x">12</a></nav>
	<main><a href="/story">Read our story in full detail today</a></main></body></html>`)
	var p Page
	ParseHTML(&p, base, body)

	if got := strings.Join(p.Nav, "|"); got != "Home|Mattresses|Sofa Beds|Cart|About us|Bedroom|Sale|12" {
		t.Errorf("nav = %q; links outside <nav> and <header> must not count", got)
	}
	var org *Organization
	for _, j := range p.JSONLD {
		if j.Org != nil {
			org = j.Org
		}
	}
	if org == nil || org.LegalName != "Koala Sleep Pty Ltd" || len(org.Alternates) != 2 || org.SameAs[0] != "https://www.instagram.com/koala" {
		t.Fatalf("organization = %+v", org)
	}

	s := Suggest(p, "Koala")
	if strings.Join(s.Aliases, "|") != "Koala Sleep Pty Ltd|Koala Mattress" {
		t.Errorf("aliases = %v; the brand's own name is not an alias", s.Aliases)
	}
	if strings.Join(s.Topics, "|") != "Mattresses|Sofa Beds|Bedroom" {
		t.Errorf("topics = %v; generic links, numbers and repeats are left out", s.Topics)
	}
}

func TestSuggestWithNothingToGo(t *testing.T) {
	s := Suggest(Page{}, "Koala")
	if len(s.Aliases) != 0 || len(s.Topics) != 0 {
		t.Errorf("suggestions from an empty page = %+v", s)
	}
}
