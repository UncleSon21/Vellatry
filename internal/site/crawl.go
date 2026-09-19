package site

import (
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// Crawler fetches a customer's site politely: robots.txt is obeyed for its own user
// agent, requests are paced, pages are capped, and it never leaves the site.
type Crawler struct {
	HTTP        *http.Client
	UserAgent   string
	MaxPages    int
	Concurrency int
	Delay       time.Duration // between requests, per worker
	MaxBytes    int64
}

// DefaultUserAgent identifies the crawler to site owners.
const DefaultUserAgent = "VellatryBot/1.0 (+https://vellatry.com/bot)"

func (c *Crawler) defaults() {
	if c.HTTP == nil {
		c.HTTP = PublicOnlyClient(20 * time.Second)
	}
	if c.UserAgent == "" {
		c.UserAgent = DefaultUserAgent
	}
	if c.MaxPages <= 0 {
		c.MaxPages = 300
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 3
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = 5 << 20
	}
}

// Result is everything one crawl found.
type Result struct {
	Home        string
	Robots      *Robots
	RobotsFound bool
	LLMSTxt     bool
	Sitemaps    []string // sitemaps that returned URLs
	Pages       []Page
}

// Facts converts a result into the audit's site-level inputs.
func (r Result) Facts() SiteFacts {
	return SiteFacts{Home: r.Home, Robots: r.Robots, RobotsFound: r.RobotsFound, LLMSTxt: r.LLMSTxt, Sitemaps: r.Sitemaps}
}

var skipExt = map[string]bool{
	".pdf": true, ".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".svg": true, ".ico": true,
	".css": true, ".js": true, ".zip": true, ".mp4": true, ".mp3": true, ".xml": true, ".json": true, ".txt": true,
	".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".woff": true, ".woff2": true,
}

// Crawl audits the site at home (for example https://koala.com/). progress, if set, is
// called after each fetched page.
func (c *Crawler) Crawl(ctx context.Context, home string, progress func(fetched int)) (Result, error) {
	c.defaults()
	base, err := url.Parse(home)
	if err != nil || base.Host == "" {
		return Result{}, errors.New("site: invalid home URL")
	}
	if base.Path == "" {
		base.Path = "/"
	}
	res := Result{Home: base.String(), Robots: &Robots{Missing: true}}

	if body, status, err := c.get(ctx, base.ResolveReference(&url.URL{Path: "/robots.txt"}).String()); err == nil && status == http.StatusOK {
		res.Robots, res.RobotsFound = ParseRobots(string(body)), true
	}
	if body, status, err := c.get(ctx, base.ResolveReference(&url.URL{Path: "/llms.txt"}).String()); err == nil && status == http.StatusOK {
		res.LLMSTxt = strings.HasPrefix(strings.TrimSpace(string(body)), "#")
	}

	sitemaps := res.Robots.Sitemaps
	if len(sitemaps) == 0 {
		sitemaps = []string{base.ResolveReference(&url.URL{Path: "/sitemap.xml"}).String()}
	}
	seeds := c.sitemapURLs(ctx, base, sitemaps, &res)

	queue := append([]string{base.String()}, seeds...)
	seen := map[string]bool{}
	var mu sync.Mutex
	for len(queue) > 0 && len(res.Pages) < c.MaxPages && ctx.Err() == nil {
		var batch []string
		for len(queue) > 0 && len(batch)+len(res.Pages) < c.MaxPages {
			u := normalise(queue[0])
			queue = queue[1:]
			if u == "" || seen[u] || !sameSite(base, u) || skipExt[strings.ToLower(path.Ext(mustPath(u)))] {
				continue
			}
			seen[u] = true
			if !res.Robots.Allowed(c.UserAgent, pathQuery(u)) {
				continue
			}
			batch = append(batch, u)
		}
		if len(batch) == 0 {
			break
		}
		pages := make([]Page, len(batch))
		sem := make(chan struct{}, c.Concurrency)
		var wg sync.WaitGroup
		for i, u := range batch {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, u string) {
				defer wg.Done()
				defer func() { <-sem }()
				pages[i] = c.fetchPage(ctx, u)
				mu.Lock()
				done := len(res.Pages) + i + 1
				mu.Unlock()
				if progress != nil {
					progress(done)
				}
				if c.Delay > 0 {
					select {
					case <-time.After(c.Delay):
					case <-ctx.Done():
					}
				}
			}(i, u)
		}
		wg.Wait()
		if len(res.Pages) == 0 && pages[0].Status == 0 && ctx.Err() == nil {
			// The home page is always first. If it can't be reached there is no site to
			// audit, and reporting "one broken page" would bury the real problem.
			return Result{Home: res.Home, Robots: res.Robots}, unreachable(pages[0].fetchErr)
		}
		for _, p := range pages {
			res.Pages = append(res.Pages, p)
			queue = append(queue, p.Links...)
		}
	}
	if len(res.Pages) == 0 && ctx.Err() == nil && !res.Robots.Allowed(c.UserAgent, pathQuery(base.String())) {
		return res, ErrDisallowed
	}
	return res, ctx.Err()
}

// ErrDisallowed means robots.txt keeps the crawler off the home page. Obeying it is the
// point; the Site page tells the customer which line to add.
var ErrDisallowed = errors.New("robots.txt does not allow VellatryBot to fetch the home page")

// ErrUnreachable wraps every failure to reach a site's home page.
var ErrUnreachable = errors.New("the site could not be reached")

// unreachable turns a transport error into a sentence for the Site page. The raw error
// names our own network details, so it is not shown.
func unreachable(err error) error {
	var dns *net.DNSError
	switch {
	case errors.Is(err, ErrBlockedAddress):
		return fmt.Errorf("%w: the domain points to a private or reserved address", ErrUnreachable)
	case errors.As(err, &dns):
		return fmt.Errorf("%w: the domain name does not resolve", ErrUnreachable)
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return fmt.Errorf("%w: the home page timed out", ErrUnreachable)
	default:
		return fmt.Errorf("%w: the connection to the home page failed", ErrUnreachable)
	}
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// fetchPage fetches one URL, following up to 10 redirects within the site by hand so
// the chain length is known.
func (c *Crawler) fetchPage(ctx context.Context, start string) Page {
	p := Page{URL: start, FinalURL: start}
	current := start
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for hop := 0; hop <= 10; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return p
		}
		req.Header.Set("User-Agent", c.UserAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		resp, err := client.Do(req)
		if err != nil {
			p.fetchErr = err
			return p // status 0: unreachable
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			resp.Body.Close()
			next := resolve(mustURL(current), loc)
			if next == "" {
				p.Status = resp.StatusCode
				return p
			}
			p.Redirects++
			p.FinalURL = next
			current = next
			base, _ := url.Parse(start)
			if !sameSite(base, next) {
				p.Status = resp.StatusCode // redirects off-site; not followed
				return p
			}
			continue
		}
		p.Status = resp.StatusCode
		p.ContentType = resp.Header.Get("Content-Type")
		if strings.Contains(strings.ToLower(resp.Header.Get("X-Robots-Tag")), "noindex") {
			p.Noindex = true
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, c.MaxBytes))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && strings.HasPrefix(strings.ToLower(p.ContentType), "text/html") {
			noindex := p.Noindex
			ParseHTML(&p, mustURL(current), body)
			p.Noindex = p.Noindex || noindex
		}
		return p
	}
	return p
}

func (c *Crawler) get(ctx context.Context, u string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var r io.Reader = io.LimitReader(resp.Body, c.MaxBytes)
	if strings.HasSuffix(strings.ToLower(u), ".gz") || strings.Contains(resp.Header.Get("Content-Type"), "gzip") {
		if gz, err := gzip.NewReader(r); err == nil {
			defer gz.Close()
			r = io.LimitReader(gz, c.MaxBytes)
		}
	}
	body, err := io.ReadAll(r)
	return body, resp.StatusCode, err
}

// sitemapURLs reads sitemaps and sitemap indexes (at most 20 documents) and returns
// same-site page URLs, capped at three times the page budget.
func (c *Crawler) sitemapURLs(ctx context.Context, base *url.URL, roots []string, res *Result) []string {
	type loc struct {
		Loc string `xml:"loc"`
	}
	type doc struct {
		XMLName  xml.Name
		URLs     []loc `xml:"url"`
		Sitemaps []loc `xml:"sitemap"`
	}
	var out []string
	queue := append([]string(nil), roots...)
	seen := map[string]bool{}
	for fetched := 0; len(queue) > 0 && fetched < 20 && len(out) < c.MaxPages*3; fetched++ {
		u := queue[0]
		queue = queue[1:]
		if seen[u] {
			continue
		}
		seen[u] = true
		body, status, err := c.get(ctx, u)
		if err != nil || status != http.StatusOK {
			continue
		}
		var d doc
		if xml.Unmarshal(body, &d) != nil {
			continue
		}
		for _, s := range d.Sitemaps {
			queue = append(queue, strings.TrimSpace(s.Loc))
		}
		found := 0
		for _, l := range d.URLs {
			if loc := strings.TrimSpace(l.Loc); sameSite(base, loc) {
				out = append(out, loc)
				found++
			}
		}
		if found > 0 {
			res.Sitemaps = append(res.Sitemaps, u)
		}
	}
	return out
}

func normalise(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

func pathQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "/"
	}
	return u.RequestURI()
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		return &url.URL{}
	}
	return u
}

func mustPath(raw string) string { return mustURL(raw).Path }
