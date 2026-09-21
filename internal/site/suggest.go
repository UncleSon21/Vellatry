package site

import (
	"strings"
	"unicode"
)

// Suggestions are what a site says about itself that onboarding can pre-fill. They
// are only ever suggestions: the brand package never lets one overwrite a value a
// person set, and topics arrive proposed, not tracked.
type Suggestions struct {
	Aliases []string `json:"aliases"` // other names the site calls itself
	Topics  []string `json:"topics"`  // its own sections, from the navigation
}

// MaxSuggestedTopics keeps the starter list short enough to review.
const MaxSuggestedTopics = 15

// Links that every site has and none is about.
var genericNav = map[string]bool{
	"home": true, "about": true, "about us": true, "our story": true, "contact": true, "contact us": true,
	"blog": true, "news": true, "journal": true, "press": true, "login": true, "log in": true, "sign in": true,
	"sign up": true, "register": true, "account": true, "my account": true, "cart": true, "basket": true,
	"bag": true, "checkout": true, "search": true, "faq": true, "faqs": true, "help": true, "support": true,
	"careers": true, "jobs": true, "privacy": true, "privacy policy": true, "terms": true, "terms of service": true,
	"terms and conditions": true, "shop all": true, "all products": true, "menu": true, "close": true, "more": true,
	"sale": true, "new": true, "new arrivals": true, "gift cards": true, "stores": true, "store locator": true,
	"find a store": true, "reviews": true, "returns": true, "shipping": true, "delivery": true, "track order": true,
	"pricing": true, "plans": true, "demo": true, "book a demo": true, "get started": true, "start free trial": true,
	"free trial": true, "resources": true, "partners": true, "customers": true, "company": true, "team": true,
	"skip to content": true, "skip to main content": true, "view all": true, "shop": true, "shop now": true,
	"learn more": true, "english": true, "australia": true,
}

// Suggest reads a home page for names the brand goes by and for its sections.
func Suggest(home Page, brandName string) Suggestions {
	s := Suggestions{Aliases: []string{}, Topics: []string{}}
	name := strings.ToLower(strings.TrimSpace(brandName))
	seen := map[string]bool{name: true}
	addAlias := func(a string) {
		a = strings.TrimSpace(a)
		key := strings.ToLower(a)
		if a == "" || len(a) > 60 || seen[key] {
			return
		}
		seen[key] = true
		s.Aliases = append(s.Aliases, a)
	}
	for _, j := range home.JSONLD {
		if j.Org == nil {
			continue
		}
		addAlias(j.Org.Name)
		addAlias(j.Org.LegalName)
		for _, a := range j.Org.Alternates {
			addAlias(a)
		}
	}

	seenTopic := map[string]bool{}
	for _, label := range home.Nav {
		t := strings.Join(strings.Fields(label), " ")
		key := strings.ToLower(t)
		if seenTopic[key] || genericNav[key] || key == name || !hasLetters(t) || len(strings.Fields(t)) > 5 {
			continue
		}
		seenTopic[key] = true
		s.Topics = append(s.Topics, t)
		if len(s.Topics) == MaxSuggestedTopics {
			break
		}
	}
	return s
}

func hasLetters(s string) bool {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n >= 3
}
