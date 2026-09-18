// Package detect finds brand and competitor mentions in an AI answer without a model.
//
// Mentions are matched case-insensitively on whole words against each entity's name
// and aliases. Exclusions (lookalikes such as a sister company or a common word) are
// masked out first, so "Koala bear" never counts as the brand "Koala". Citations are
// matched by domain, including subdomains.
package detect

import (
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Entity is a brand or competitor as the customer configured it.
type Entity struct {
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases,omitempty"`
	Exclusions []string `json:"exclusions,omitempty"`
	Domains    []string `json:"domains,omitempty"`
}

// Mention is how often an entity appears in the answer text.
type Mention struct {
	Entity string `json:"entity"`
	Count  int    `json:"count"`
	First  int    `json:"first"` // offset of the first mention in the lowered text, -1 if absent
}

// Result holds every signal derived from one answer.
type Result struct {
	Brand            Mention   `json:"brand"`
	Competitors      []Mention `json:"competitors"`
	BrandPosition    int       `json:"brand_position"` // 1-based rank by first mention among tracked entities, 0 if absent
	BrandCited       bool      `json:"brand_cited"`
	CitedCompetitors []string  `json:"cited_competitors,omitempty"`
}

// Mentioned reports whether the brand appears in the answer text.
func (r Result) Mentioned() bool { return r.Brand.Count > 0 }

// CompetitorMentioned reports whether any competitor appears in the answer text.
func (r Result) CompetitorMentioned() bool {
	for _, c := range r.Competitors {
		if c.Count > 0 {
			return true
		}
	}
	return false
}

// Analyze derives mention and citation signals for one answer.
func Analyze(text string, citations []string, brand Entity, competitors []Entity) Result {
	lowered := strings.ToLower(text)
	res := Result{Brand: count(lowered, brand)}
	all := []Mention{res.Brand}
	for _, c := range competitors {
		m := count(lowered, c)
		res.Competitors = append(res.Competitors, m)
		all = append(all, m)
	}
	res.BrandPosition = position(all)
	res.BrandCited = anyCited(citations, brand.Domains)
	for _, c := range competitors {
		if anyCited(citations, c.Domains) {
			res.CitedCompetitors = append(res.CitedCompetitors, c.Name)
		}
	}
	return res
}

func count(lowered string, e Entity) Mention {
	m := Mention{Entity: e.Name, First: -1}
	masked := lowered
	for _, ex := range e.Exclusions {
		masked = mask(masked, strings.ToLower(ex))
	}
	terms := normalizedTerms(e)
	covered := make([]bool, len(masked))
	for _, term := range terms {
		for from := 0; ; {
			i := strings.Index(masked[from:], term)
			if i < 0 {
				break
			}
			start, end := from+i, from+i+len(term)
			from = start + 1
			if !wholeWord(masked, start, end) || anyCovered(covered, start, end) {
				continue
			}
			for k := start; k < end; k++ {
				covered[k] = true
			}
			m.Count++
			if m.First < 0 || start < m.First {
				m.First = start
			}
		}
	}
	return m
}

// normalizedTerms returns name and aliases, lowered, deduplicated, longest first so
// "xero payroll" claims its span before "xero" can count it twice.
func normalizedTerms(e Entity) []string {
	seen := map[string]bool{}
	var terms []string
	for _, t := range append([]string{e.Name}, e.Aliases...) {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
	}
	sort.SliceStable(terms, func(i, j int) bool { return len(terms[i]) > len(terms[j]) })
	return terms
}

// mask blanks every whole-word occurrence of phrase, preserving byte offsets.
func mask(s, phrase string) string {
	if phrase == "" {
		return s
	}
	var b strings.Builder
	for from := 0; ; {
		i := strings.Index(s[from:], phrase)
		if i < 0 {
			b.WriteString(s[from:])
			return b.String()
		}
		start, end := from+i, from+i+len(phrase)
		if wholeWord(s, start, end) {
			b.WriteString(s[from:start])
			b.WriteString(strings.Repeat(" ", end-start))
		} else {
			b.WriteString(s[from:end])
		}
		from = end
	}
}

func wholeWord(s string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:start])
		if isWordRune(r) {
			return false
		}
	}
	if end < len(s) {
		r, _ := utf8.DecodeRuneInString(s[end:])
		if isWordRune(r) {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func anyCovered(covered []bool, start, end int) bool {
	for k := start; k < end; k++ {
		if covered[k] {
			return true
		}
	}
	return false
}

func position(all []Mention) int {
	if all[0].Count == 0 {
		return 0
	}
	rank := 1
	for _, m := range all[1:] {
		if m.Count > 0 && m.First < all[0].First {
			rank++
		}
	}
	return rank
}

func anyCited(citations, domains []string) bool {
	for _, c := range citations {
		host := Host(c)
		for _, d := range domains {
			d = strings.ToLower(strings.TrimPrefix(d, "www."))
			if host == d || strings.HasSuffix(host, "."+d) {
				return true
			}
		}
	}
	return false
}

// Host returns the lowered hostname of a URL without a leading "www.", or "" if unparseable.
func Host(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}
