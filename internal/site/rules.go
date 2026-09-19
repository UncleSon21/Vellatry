package site

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Severity of a finding.
const (
	Critical = "critical"
	Warning  = "warning"
	Info     = "info"
)

// Finding is one problem the audit found. Its fingerprint is stable across crawls, so
// the next crawl can tell "still broken" from "fixed".
type Finding struct {
	Fingerprint string         `json:"fingerprint"`
	Rule        string         `json:"rule"`
	Severity    string         `json:"severity"`
	URL         string         `json:"url,omitempty"`
	Message     string         `json:"message"`
	Detail      map[string]any `json:"detail,omitempty"`
}

func finding(rule, severity, pageURL, key, message string, detail map[string]any) Finding {
	sum := sha1.Sum([]byte(rule + "|" + pageURL + "|" + key))
	return Finding{Fingerprint: hex.EncodeToString(sum[:10]), Rule: rule, Severity: severity, URL: pageURL, Message: message, Detail: detail}
}

// SiteFacts are the site-level inputs to the audit.
type SiteFacts struct {
	Home        string
	Robots      *Robots
	RobotsFound bool
	LLMSTxt     bool
	Sitemaps    []string
}

// Audit applies every rule to the crawl. Deterministic: same input, same findings.
func Audit(site SiteFacts, pages []Page) []Finding {
	var out []Finding

	// AI and search crawler access.
	for _, a := range AIAccess(site.Robots) {
		if !a.Blocked {
			continue
		}
		if a.Purpose == "search" {
			out = append(out, finding("robots_blocks_search_bot", Critical, "", a.Agent,
				fmt.Sprintf("robots.txt blocks %s (%s), so this site cannot appear in its answers.", a.Agent, a.Product),
				map[string]any{"agent": a.Agent, "product": a.Product}))
		} else {
			out = append(out, finding("robots_blocks_training_bot", Info, "", a.Agent,
				fmt.Sprintf("robots.txt blocks %s (%s). This is a valid choice; it can reduce how well models know the brand.", a.Agent, a.Product),
				map[string]any{"agent": a.Agent, "product": a.Product}))
		}
	}
	if !site.RobotsFound {
		out = append(out, finding("robots_missing", Info, "", "", "No robots.txt found. Crawlers may fetch everything, and there is no place to list sitemaps.", nil))
	}
	if !site.LLMSTxt {
		out = append(out, finding("llms_txt_missing", Info, "", "", "No llms.txt. It gives AI assistants a curated map of the site's most important pages.", nil))
	}
	if len(site.Sitemaps) == 0 {
		out = append(out, finding("sitemap_missing", Warning, "", "", "No XML sitemap found in robots.txt or at /sitemap.xml.", nil))
	}

	titles, descs := map[string][]string{}, map[string][]string{}
	for _, p := range pages {
		if p.Status >= 400 || p.Status == 0 {
			out = append(out, finding("status_error", Critical, p.URL, "", fmt.Sprintf("The page returns HTTP %d.", p.Status), map[string]any{"status": p.Status}))
			continue
		}
		if p.Redirects >= 2 {
			out = append(out, finding("redirect_chain", Warning, p.URL, "", fmt.Sprintf("Reaching this page takes %d redirects.", p.Redirects),
				map[string]any{"redirects": p.Redirects, "final_url": p.FinalURL}))
		}
		if !strings.HasPrefix(strings.ToLower(p.ContentType), "text/html") {
			continue
		}
		home := isHome(p.URL, site.Home)
		if p.Noindex {
			sev := Warning
			if home {
				sev = Critical
			}
			out = append(out, finding("noindex", sev, p.URL, "", "The page tells search engines not to index it.", nil))
			continue // the remaining on-page rules matter little on a page kept out of search
		}
		switch {
		case p.Title == "":
			out = append(out, finding("title_missing", Critical, p.URL, "", "The page has no title.", nil))
		case len([]rune(p.Title)) < 30 || len([]rune(p.Title)) > 60:
			out = append(out, finding("title_length", Warning, p.URL, "", fmt.Sprintf("The title is %d characters; 30 to 60 displays best.", len([]rune(p.Title))),
				map[string]any{"title": p.Title, "length": len([]rune(p.Title))}))
		}
		if p.Title != "" {
			titles[p.Title] = append(titles[p.Title], p.URL)
		}
		switch {
		case p.MetaDescription == "":
			out = append(out, finding("meta_description_missing", Warning, p.URL, "", "The page has no meta description.", nil))
		case len([]rune(p.MetaDescription)) < 70 || len([]rune(p.MetaDescription)) > 160:
			out = append(out, finding("meta_description_length", Info, p.URL, "", fmt.Sprintf("The meta description is %d characters; 70 to 160 displays best.", len([]rune(p.MetaDescription))),
				map[string]any{"meta_description": p.MetaDescription, "length": len([]rune(p.MetaDescription))}))
		}
		if p.MetaDescription != "" {
			descs[p.MetaDescription] = append(descs[p.MetaDescription], p.URL)
		}
		switch len(p.H1) {
		case 0:
			out = append(out, finding("h1_missing", Warning, p.URL, "", "The page has no H1 heading.", nil))
		case 1:
		default:
			out = append(out, finding("h1_multiple", Info, p.URL, "", fmt.Sprintf("The page has %d H1 headings.", len(p.H1)), map[string]any{"h1": p.H1}))
		}
		if skip := headingSkip(p.Headings); skip != "" {
			out = append(out, finding("heading_skip", Info, p.URL, "", "Headings skip a level ("+skip+").", nil))
		}
		if p.ImagesNoAltN > 0 {
			out = append(out, finding("img_alt_missing", Warning, p.URL, "", fmt.Sprintf("%d of %d images have no alt text.", p.ImagesNoAltN, p.Images),
				map[string]any{"images": p.ImagesNoAlt, "count": p.ImagesNoAltN}))
		}
		switch {
		case len(p.Canonical) == 0:
			out = append(out, finding("canonical_missing", Info, p.URL, "", "The page has no canonical link.", nil))
		case len(p.Canonical) > 1:
			out = append(out, finding("canonical_multiple", Warning, p.URL, "", "The page declares more than one canonical URL.", map[string]any{"canonical": p.Canonical}))
		case !sameHost(p.Canonical[0], p.URL):
			out = append(out, finding("canonical_other_host", Warning, p.URL, "", "The canonical URL points to another host.", map[string]any{"canonical": p.Canonical[0]}))
		}
		if p.Lang == "" {
			out = append(out, finding("lang_missing", Info, p.URL, "", "The html element has no lang attribute.", nil))
		}
		if !p.Viewport {
			out = append(out, finding("viewport_missing", Warning, p.URL, "", "The page has no viewport meta tag, so it may not render well on phones.", nil))
		}
		for _, j := range p.JSONLD {
			if !j.Valid {
				out = append(out, finding("jsonld_invalid", Warning, p.URL, "", "A JSON-LD block does not parse, so search engines ignore it.", map[string]any{"error": j.Error}))
				break
			}
		}
		if home && !hasType(p.JSONLD, "Organization", "Corporation", "LocalBusiness", "OnlineStore", "Store") {
			out = append(out, finding("organization_schema_missing", Info, p.URL, "", "The homepage has no Organization structured data describing the brand.", nil))
		}
		if p.OpenGraph["og:title"] == "" || p.OpenGraph["og:description"] == "" {
			out = append(out, finding("open_graph_missing", Info, p.URL, "", "Open Graph title or description is missing.", nil))
		}
		if p.WordCount < 150 && p.Scripts > 0 {
			out = append(out, finding("thin_server_content", Warning, p.URL, "",
				fmt.Sprintf("Only %d words are in the HTML the server sends. AI crawlers that do not run JavaScript see little else.", p.WordCount),
				map[string]any{"words": p.WordCount}))
		}
	}
	for title, urls := range titles {
		if len(urls) > 1 {
			sort.Strings(urls)
			out = append(out, finding("title_duplicate", Warning, urls[0], title, fmt.Sprintf("%d pages share the title %q.", len(urls), title), map[string]any{"urls": urls}))
		}
	}
	for desc, urls := range descs {
		if len(urls) > 1 {
			sort.Strings(urls)
			out = append(out, finding("meta_description_duplicate", Info, urls[0], desc, fmt.Sprintf("%d pages share the same meta description.", len(urls)), map[string]any{"urls": urls}))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].Severity) < rank(out[j].Severity) })
	return out
}

func rank(sev string) int {
	switch sev {
	case Critical:
		return 0
	case Warning:
		return 1
	}
	return 2
}

func headingSkip(levels []int) string {
	prev := 0
	for _, l := range levels {
		if prev > 0 && l > prev+1 {
			return fmt.Sprintf("H%d to H%d", prev, l)
		}
		prev = l
	}
	return ""
}

func hasType(blocks []JSONLD, types ...string) bool {
	for _, b := range blocks {
		for _, t := range b.Types {
			for _, want := range types {
				if strings.EqualFold(t, want) {
					return true
				}
			}
		}
	}
	return false
}

func isHome(pageURL, home string) bool {
	a, err1 := url.Parse(pageURL)
	b, err2 := url.Parse(home)
	if err1 != nil || err2 != nil {
		return false
	}
	return sameHost(pageURL, home) && strings.TrimRight(a.Path, "/") == strings.TrimRight(b.Path, "/")
}

func sameHost(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil &&
		strings.TrimPrefix(strings.ToLower(ua.Hostname()), "www.") == strings.TrimPrefix(strings.ToLower(ub.Hostname()), "www.")
}
