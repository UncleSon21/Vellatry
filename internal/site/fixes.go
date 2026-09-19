package site

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Fix is a copy-ready change for one finding: what to do, why, and the exact snippet
// where one can be generated deterministically. No model writes these.
type Fix struct {
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
	Snippet      string `json:"snippet,omitempty"`
	SnippetLang  string `json:"snippet_lang,omitempty"` // text | html | json | markdown
}

// BrandFacts are what fixes may quote about the brand.
type BrandFacts struct {
	Name    string
	Domain  string
	Summary string   // one sentence, from the brand setup
	Pages   []Page   // top pages, for llms.txt
	SameAs  []string // official profiles, when known
}

// FixFor returns the fix for a finding, or false when the finding needs judgement the
// audit cannot supply (it is still listed, without a generated snippet).
func FixFor(f Finding, b BrandFacts) (Fix, bool) {
	switch f.Rule {
	case "robots_blocks_search_bot":
		agent, _ := f.Detail["agent"].(string)
		return Fix{
			Title: "Let " + agent + " crawl the site",
			Instructions: fmt.Sprintf("In robots.txt, remove the `Disallow: /` that applies to %s (either in its own group or in `User-agent: *`) and add an explicit group allowing it. "+
				"Blocking %s keeps the site out of %s answers.", agent, agent, f.Detail["product"]),
			Snippet:     "User-agent: " + agent + "\nAllow: /",
			SnippetLang: "text",
		}, true
	case "llms_txt_missing":
		return Fix{
			Title:        "Publish llms.txt",
			Instructions: "Save this as /llms.txt at the site root. It follows the llms.txt proposal: a title, a one-line summary, and links to the pages AI assistants should read first. Review the page list before publishing.",
			Snippet:      LLMSTxt(b),
			SnippetLang:  "markdown",
		}, true
	case "organization_schema_missing":
		return Fix{
			Title:        "Describe the brand with Organization structured data",
			Instructions: "Add this block inside the homepage's <head>. Add a logo URL and your official social profiles to sameAs.",
			Snippet:      OrganizationJSONLD(b),
			SnippetLang:  "html",
		}, true
	case "canonical_missing":
		return Fix{
			Title:        "Add a canonical link",
			Instructions: "Add this inside <head> so search engines know which URL is the original.",
			Snippet:      fmt.Sprintf(`<link rel="canonical" href="%s">`, f.URL),
			SnippetLang:  "html",
		}, true
	case "lang_missing":
		return Fix{Title: "Declare the page language", Instructions: "Set the language on the html element.", Snippet: `<html lang="en-AU">`, SnippetLang: "html"}, true
	case "viewport_missing":
		return Fix{Title: "Add a viewport meta tag", Instructions: "Add this inside <head> so the page scales on phones.",
			Snippet: `<meta name="viewport" content="width=device-width, initial-scale=1">`, SnippetLang: "html"}, true
	case "img_alt_missing":
		imgs, _ := f.Detail["images"].([]string)
		return Fix{Title: "Add alt text to images", Instructions: "Give each image an alt attribute describing it. Use alt=\"\" for purely decorative images.",
			Snippet: strings.Join(imgs, "\n"), SnippetLang: "text"}, true
	case "title_missing":
		return Fix{Title: "Add a page title", Instructions: "Add a <title> of 30 to 60 characters that states what the page offers, with the main topic first."}, true
	case "title_length":
		return Fix{Title: "Resize the page title", Instructions: fmt.Sprintf("Rewrite the title to 30 to 60 characters, keeping the main topic first. Current: %q.", f.Detail["title"])}, true
	case "meta_description_missing":
		return Fix{Title: "Add a meta description", Instructions: "Add a meta description of 70 to 160 characters that summarises the page and gives a reason to click."}, true
	case "noindex":
		return Fix{Title: "Check the noindex tag", Instructions: "The page asks search engines not to index it. If that is unintended, remove noindex from the robots meta tag or the X-Robots-Tag header."}, true
	case "status_error":
		return Fix{Title: "Fix or redirect the broken page", Instructions: fmt.Sprintf("The page returns HTTP %v. Restore it, or 301-redirect it to the closest live page and update links pointing to it.", f.Detail["status"])}, true
	case "redirect_chain":
		return Fix{Title: "Shorten the redirect chain", Instructions: fmt.Sprintf("Point links and the first redirect straight at %v.", f.Detail["final_url"])}, true
	case "thin_server_content":
		return Fix{Title: "Render the main content on the server", Instructions: "Most of this page's text is added by JavaScript. Crawlers that do not run JavaScript, including several AI crawlers, see almost none of it. Render the main content in the HTML the server sends."}, true
	case "sitemap_missing":
		return Fix{Title: "Publish an XML sitemap", Instructions: "Generate /sitemap.xml listing the pages you want found, and reference it from robots.txt.",
			Snippet: "Sitemap: https://" + b.Domain + "/sitemap.xml", SnippetLang: "text"}, true
	}
	return Fix{}, false
}

// LLMSTxt builds an llms.txt from the brand and its top pages.
func LLMSTxt(b BrandFacts) string {
	var s strings.Builder
	fmt.Fprintf(&s, "# %s\n\n", b.Name)
	if b.Summary != "" {
		fmt.Fprintf(&s, "> %s\n\n", b.Summary)
	}
	s.WriteString("## Key pages\n\n")
	n := 0
	for _, p := range b.Pages {
		if p.Title == "" || p.Status >= 400 || p.Noindex {
			continue
		}
		line := fmt.Sprintf("- [%s](%s)", p.Title, p.URL)
		if p.MetaDescription != "" {
			line += ": " + p.MetaDescription
		}
		s.WriteString(line + "\n")
		if n++; n == 20 {
			break
		}
	}
	return s.String()
}

// OrganizationJSONLD builds the brand's Organization block.
func OrganizationJSONLD(b BrandFacts) string {
	org := map[string]any{"@context": "https://schema.org", "@type": "Organization", "name": b.Name, "url": "https://" + b.Domain + "/"}
	if b.Summary != "" {
		org["description"] = b.Summary
	}
	if len(b.SameAs) > 0 {
		org["sameAs"] = b.SameAs
	}
	raw, _ := json.MarshalIndent(org, "", "  ")
	return "<script type=\"application/ld+json\">\n" + string(raw) + "\n</script>"
}
