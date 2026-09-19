package site

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCrawl(t *testing.T) {
	var privateHits atomic.Int32
	var srv *httptest.Server
	mux := http.NewServeMux()
	page := func(title, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<html lang=en><head><title>" + title + "</title></head><body>" + body + "</body></html>"))
		}
	}
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /private\nUser-agent: GPTBot\nDisallow: /\nSitemap: " + srv.URL + "/sitemap_index.xml\n"))
	})
	mux.HandleFunc("/sitemap_index.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?><sitemapindex><sitemap><loc>` + srv.URL + `/pages.xml</loc></sitemap></sitemapindex>`))
	})
	mux.HandleFunc("/pages.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<urlset><url><loc>` + srv.URL + `/about</loc></url><url><loc>https://elsewhere.example/x</loc></url></urlset>`))
	})
	mux.HandleFunc("/{$}", page("Home", `<a href="/about">About</a><a href="/old">Old</a><a href="/private/x">P</a>
		<a href="/guide.pdf">PDF</a><a href="https://elsewhere.example/">Out</a><a href="/missing">Missing</a>`))
	mux.HandleFunc("/about", page("About us", `<a href="/">Home</a>`))
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/older", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/older", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/about", http.StatusFound) })
	mux.HandleFunc("/private/", func(w http.ResponseWriter, r *http.Request) { privateHits.Add(1) })
	mux.HandleFunc("/guide.pdf", func(w http.ResponseWriter, r *http.Request) { t.Error("fetched a PDF") })
	srv = httptest.NewServer(mux)
	defer srv.Close()

	c := &Crawler{HTTP: srv.Client(), MaxPages: 50}
	var progressCalls atomic.Int32
	res, err := c.Crawl(context.Background(), srv.URL+"/", func(int) { progressCalls.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if !res.RobotsFound || res.LLMSTxt || len(res.Sitemaps) != 1 || !strings.HasSuffix(res.Sitemaps[0], "/pages.xml") {
		t.Errorf("site facts: robots %v, llms %v, sitemaps %v", res.RobotsFound, res.LLMSTxt, res.Sitemaps)
	}
	byPath := map[string]Page{}
	for _, p := range res.Pages {
		byPath[strings.TrimPrefix(p.URL, srv.URL)] = p
	}
	if len(res.Pages) != 4 { // /, /about, /old, /missing
		t.Errorf("pages = %v", keys(byPath))
	}
	if byPath["/"].Title != "Home" || byPath["/about"].Title != "About us" {
		t.Errorf("titles: %+v", byPath)
	}
	if old := byPath["/old"]; old.Redirects != 2 || !strings.HasSuffix(old.FinalURL, "/about") || old.Status != 200 {
		t.Errorf("redirect chain: %+v", old)
	}
	if byPath["/missing"].Status != 404 {
		t.Errorf("missing page status = %d", byPath["/missing"].Status)
	}
	if privateHits.Load() != 0 {
		t.Error("crawled a path robots.txt disallows")
	}
	if int(progressCalls.Load()) != len(res.Pages) {
		t.Errorf("progress called %d times for %d pages", progressCalls.Load(), len(res.Pages))
	}
	findings := Audit(res.Facts(), res.Pages)
	if len(findings) == 0 || findings[0].Severity != Critical {
		t.Errorf("audit of crawl: %+v", findings)
	}

	capped, _ := (&Crawler{HTTP: srv.Client(), MaxPages: 2}).Crawl(context.Background(), srv.URL+"/", nil)
	if len(capped.Pages) != 2 {
		t.Errorf("page cap ignored: %d pages", len(capped.Pages))
	}
}

func keys(m map[string]Page) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
