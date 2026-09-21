package site

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// JSONLD is one structured-data block.
type JSONLD struct {
	Types []string      `json:"types"`
	Valid bool          `json:"valid"`
	Error string        `json:"error,omitempty"`
	Org   *Organization `json:"org,omitempty"` // the first organisation the block describes
}

// Organization is what a site's own structured data says about who runs it. It is the
// source of brand suggestions: names the site already calls itself.
type Organization struct {
	Name       string   `json:"name,omitempty"`
	LegalName  string   `json:"legal_name,omitempty"`
	Alternates []string `json:"alternates,omitempty"`
	SameAs     []string `json:"same_as,omitempty"`
}

var orgTypes = map[string]bool{
	"Organization": true, "Corporation": true, "LocalBusiness": true, "OnlineStore": true, "OnlineBusiness": true,
	"Store": true, "Brand": true, "NGO": true, "EducationalOrganization": true, "MedicalOrganization": true,
	"ProfessionalService": true, "FinancialService": true, "Restaurant": true, "HealthAndBeautyBusiness": true,
}

func isOrgType(t string) bool {
	return orgTypes[t] || strings.HasSuffix(t, "Business") || strings.HasSuffix(t, "Store")
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case string:
		if s := clean(x); s != "" {
			return []string{s}
		}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, stringsOf(e)...)
		}
		return out
	}
	return nil
}

// Page is what the audit knows about one fetched URL.
type Page struct {
	URL             string            `json:"url"`
	FinalURL        string            `json:"final_url"`
	Status          int               `json:"status"`
	Redirects       int               `json:"redirects"`
	ContentType     string            `json:"content_type"`
	Title           string            `json:"title"`
	TitleCount      int               `json:"title_count"`
	MetaDescription string            `json:"meta_description"`
	MetaDescCount   int               `json:"meta_desc_count"`
	H1              []string          `json:"h1"`
	Headings        []int             `json:"headings"` // levels in document order
	Canonical       []string          `json:"canonical"`
	Lang            string            `json:"lang"`
	Viewport        bool              `json:"viewport"`
	Noindex         bool              `json:"noindex"`
	Images          int               `json:"images"`
	ImagesNoAlt     []string          `json:"images_no_alt"` // src of up to 20 images without an alt attribute
	ImagesNoAltN    int               `json:"images_no_alt_n"`
	JSONLD          []JSONLD          `json:"json_ld"`
	Nav             []string          `json:"nav,omitempty"` // link text inside <nav> and <header>: the site's own sections
	OpenGraph       map[string]string `json:"open_graph"`
	Links           []string          `json:"-"` // absolute same-site links, for crawling
	fetchErr        error             // why a status-0 page could not be fetched
	Scripts         int               `json:"scripts"`
	WordCount       int               `json:"word_count"` // server-rendered visible text
	ContentHash     string            `json:"content_hash"`
}

// ParseHTML fills the on-page fields of p from an HTML document served at base.
func ParseHTML(p *Page, base *url.URL, body []byte) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return
	}
	p.OpenGraph = map[string]string{}
	var text strings.Builder
	seenLinks := map[string]bool{}

	seenNav := map[string]bool{}
	var walk func(n *html.Node, skipText, inNav bool)
	walk = func(n *html.Node, skipText, inNav bool) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Nav, atom.Header:
				inNav = true
			case atom.Html:
				p.Lang = attr(n, "lang")
			case atom.Title:
				p.TitleCount++
				if p.TitleCount == 1 {
					p.Title = clean(textOf(n))
				}
			case atom.Meta:
				name := strings.ToLower(attr(n, "name"))
				prop := strings.ToLower(attr(n, "property"))
				content := attr(n, "content")
				switch {
				case name == "description":
					p.MetaDescCount++
					if p.MetaDescCount == 1 {
						p.MetaDescription = clean(content)
					}
				case name == "viewport":
					p.Viewport = true
				case name == "robots" || name == "googlebot":
					if strings.Contains(strings.ToLower(content), "noindex") {
						p.Noindex = true
					}
				case strings.HasPrefix(prop, "og:"):
					p.OpenGraph[prop] = content
				}
			case atom.Link:
				if strings.EqualFold(attr(n, "rel"), "canonical") {
					if u := resolve(base, attr(n, "href")); u != "" {
						p.Canonical = append(p.Canonical, u)
					}
				}
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				level := int(n.Data[1] - '0')
				p.Headings = append(p.Headings, level)
				if level == 1 {
					p.H1 = append(p.H1, clean(textOf(n)))
				}
			case atom.Img:
				p.Images++
				if _, ok := attrOK(n, "alt"); !ok {
					p.ImagesNoAltN++
					if len(p.ImagesNoAlt) < 20 {
						p.ImagesNoAlt = append(p.ImagesNoAlt, resolve(base, attr(n, "src")))
					}
				}
			case atom.A:
				if u := resolve(base, attr(n, "href")); u != "" && sameSite(base, u) && !seenLinks[u] {
					seenLinks[u] = true
					p.Links = append(p.Links, u)
				}
				if inNav && len(p.Nav) < 60 {
					if label := clean(textOf(n)); len(label) >= 2 && len(label) <= 40 && !seenNav[strings.ToLower(label)] {
						seenNav[strings.ToLower(label)] = true
						p.Nav = append(p.Nav, label)
					}
				}
			case atom.Script:
				if strings.EqualFold(attr(n, "type"), "application/ld+json") {
					p.JSONLD = append(p.JSONLD, parseJSONLD(textOf(n)))
					return
				}
				p.Scripts++
				skipText = true
			case atom.Style, atom.Noscript, atom.Template, atom.Head:
				skipText = true
			}
		}
		if n.Type == html.TextNode && !skipText {
			text.WriteString(n.Data)
			text.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, skipText, inNav)
		}
	}
	walk(doc, false, false)

	visible := strings.Join(strings.Fields(text.String()), " ")
	p.WordCount = countWords(visible)
	sum := sha256.Sum256([]byte(p.Title + "\x00" + p.MetaDescription + "\x00" + strings.Join(p.H1, "|") + "\x00" + visible))
	p.ContentHash = hex.EncodeToString(sum[:8])
}

func parseJSONLD(raw string) JSONLD {
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &v); err != nil {
		return JSONLD{Valid: false, Error: err.Error()}
	}
	out := JSONLD{Valid: true}
	var collect func(v any)
	collect = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			var types []string
			switch t := x["@type"].(type) {
			case string:
				types = append(types, t)
			case []any:
				for _, s := range t {
					if s, ok := s.(string); ok {
						types = append(types, s)
					}
				}
			}
			out.Types = append(out.Types, types...)
			for _, t := range types {
				if out.Org == nil && isOrgType(t) {
					o := &Organization{Alternates: stringsOf(x["alternateName"]), SameAs: stringsOf(x["sameAs"])}
					if names := stringsOf(x["name"]); len(names) > 0 {
						o.Name = names[0]
					}
					if legal := stringsOf(x["legalName"]); len(legal) > 0 {
						o.LegalName = legal[0]
					}
					if o.Name != "" || o.LegalName != "" || len(o.Alternates) > 0 {
						out.Org = o
					}
				}
			}
			if g, ok := x["@graph"]; ok {
				collect(g)
			}
		case []any:
			for _, e := range x {
				collect(e)
			}
		}
	}
	collect(v)
	return out
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func attr(n *html.Node, key string) string {
	v, _ := attrOK(n, key)
	return v
}

func attrOK(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func countWords(s string) int {
	n, in := 0, false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if !in {
				n++
			}
			in = true
		} else {
			in = false
		}
	}
	return n
}

// resolve makes href absolute against base, dropping fragments and non-http links.
func resolve(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := base.Parse(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// SameSite treats www and the bare domain as the same site.
func sameSite(base *url.URL, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") == strings.TrimPrefix(strings.ToLower(base.Hostname()), "www.")
}
