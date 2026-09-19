package reports

import (
	"bytes"
	"fmt"
	"html/template"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// View is everything the report template needs.
type View struct {
	Snapshot    Snapshot
	Title       string
	Summary     string            // paragraphs separated by blank lines
	Notes       map[string]string // section key -> the team's note
	Accent      string            // #rrggbb
	Team        bool              // team preview: draft banner, omitted and incomplete sections listed
	Draft       bool
	Version     int
	PublishedAt *time.Time
	BackURL     string // hub: the report list
	PDFURL      string // hub: set only when the PDF exists
	Print       bool   // rendering for the PDF: no navigation
}

var accentRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// DefaultAccent is the report colour when the series sets none.
const DefaultAccent = "#1d4ed8"

// ValidAccent reports whether c is a #rrggbb colour.
func ValidAccent(c string) bool { return accentRE.MatchString(c) }

var blankLine = regexp.MustCompile(`\n\s*\n`)

// Paragraphs splits text on blank lines.
func Paragraphs(text string) []string {
	var out []string
	for _, p := range blankLine.Split(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")), -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// svgChart draws a daily line with no axis numbers (every figure a reader can quote is
// in the tables and headline metrics, which the figure check covers).
func svgChart(c *Chart, accent string) template.HTML {
	const w, h, pad = 640.0, 140.0, 6.0
	max := 0.0
	for _, p := range c.Points {
		if p.Value != nil && *p.Value > max {
			max = *p.Value
		}
	}
	if max == 0 {
		max = 1
	}
	max *= 1.1
	n := len(c.Points)
	var pts []string
	for i, p := range c.Points {
		if p.Value == nil {
			continue
		}
		x := pad + (w-2*pad)*float64(i)/math.Max(float64(n-1), 1)
		y := h - pad - (h-2*pad)*(*p.Value/max)
		pts = append(pts, strconv.FormatFloat(x, 'f', 1, 64)+","+strconv.FormatFloat(y, 'f', 1, 64))
	}
	return template.HTML(fmt.Sprintf(`<svg viewBox="0 0 %.0f %.0f" width="100%%" height="140" role="img" aria-label="%s" preserveAspectRatio="none">`+
		`<line x1="0" y1="%.0f" x2="%.0f" y2="%.0f" stroke="#e4e7ec" stroke-width="1"/>`+
		`<polyline fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" vector-effect="non-scaling-stroke" points="%s"/></svg>`,
		w, h, template.HTMLEscapeString(c.Caption), h-pad, w, h-pad, accent, strings.Join(pts, " ")))
}

var sectionNames = map[string]string{
	SecVisibility: "AI visibility", SecBlindspots: "Blindspots", SecSearch: "Google Search", SecAIReferrals: "Visits from AI assistants",
	SecTraffic: "Website traffic", SecSite: "Site health", SecFixes: "What changed on the site",
}

// RenderHTML renders a report. The same output is shown in the hub and sent to the PDF
// renderer, so the two cannot drift apart.
func RenderHTML(v View) ([]byte, error) {
	accent := v.Accent
	if !ValidAccent(accent) {
		accent = DefaultAccent
	}
	var omitted, warnings []string
	if v.Team {
		for _, n := range v.Snapshot.Omitted {
			omitted = append(omitted, sectionNames[n.Section]+": "+n.Reason)
		}
		for _, n := range v.Snapshot.Warnings {
			warnings = append(warnings, sectionNames[n.Section]+": "+n.Reason)
		}
	}
	data := map[string]any{
		"V": v, "Blocks": Layout(v.Snapshot, v.Notes), "Summary": Paragraphs(v.Summary), "Accent": template.CSS(accent),
		"Sources": Sources(v.Snapshot), "Omitted": omitted, "Warnings": warnings,
	}
	var b bytes.Buffer
	err := reportHTML.Execute(&b, data)
	return b.Bytes(), err
}

var reportHTML = template.Must(template.New("report").Funcs(template.FuncMap{
	"chart":      func(c *Chart, accent template.CSS) template.HTML { return svgChart(c, string(accent)) },
	"paragraphs": Paragraphs,
	"bold":       func(t Table, i int) bool { return i < len(t.Bold) && t.Bold[i] },
	"date":       func(t time.Time) string { return t.In(sydney()).Format("2 January 2006") },
	"good": func(g *bool) string {
		switch {
		case g == nil:
			return "neutral"
		case *g:
			return "good"
		}
		return "bad"
	},
}).Parse(`<!doctype html>
<html lang="en-AU"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow"><title>{{.V.Title}}</title>
<style>
:root{--accent:{{.Accent}};--ink:#101828;--muted:#475467;--faint:#98a2b3;--line:#eaecf0;--bg:#f6f7f9;--good:#067647;--bad:#b42318}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.55 -apple-system,"Segoe UI",Roboto,Helvetica,Arial,sans-serif}
.page{max-width:880px;margin:0 auto;padding:24px 16px 48px}
.nav{display:flex;justify-content:space-between;gap:12px;font-size:14px;margin-bottom:16px}
.nav a{color:var(--muted)}
.banner{border-radius:8px;padding:12px 16px;margin-bottom:16px;font-size:14px}
.banner.draft{background:#fffaeb;border:1px solid #fedf89}
.banner.team{background:#f0f9ff;border:1px solid #b9e6fe}
.banner ul{margin:6px 0 0;padding-left:18px}
header.cover{background:#fff;border-top:6px solid var(--accent);border-radius:8px;padding:28px 28px 24px;margin-bottom:16px}
.cover .org{color:var(--muted);font-size:14px;margin:0 0 4px}
.cover h1{font-size:26px;line-height:1.25;margin:0 0 6px}
.cover .period{margin:0;color:var(--muted)}
section{background:#fff;border-radius:8px;padding:24px 28px;margin-bottom:16px}
h2{font-size:18px;margin:0 0 4px}
.intro{color:var(--muted);margin:0 0 16px;font-size:14px}
.note{border-left:3px solid var(--accent);padding:4px 0 4px 12px;margin:0 0 16px}
.summary p{margin:0 0 12px}
.metrics{display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:12px;margin-bottom:16px}
.metric{border:1px solid var(--line);border-radius:8px;padding:12px 14px}
.metric .label{font-size:13px;color:var(--muted)}
.metric .value{font-size:24px;font-weight:600;line-height:1.3}
.metric .change{font-size:13px}
.good{color:var(--good)}.bad{color:var(--bad)}.neutral{color:var(--muted)}
figure{margin:0 0 16px}
figcaption{font-size:12px;color:var(--faint);margin-top:4px}
h3{font-size:14px;margin:16px 0 6px}
table{width:100%;border-collapse:collapse;font-size:14px}
th{text-align:left;font-weight:600;color:var(--muted);border-bottom:1px solid var(--line);padding:6px 8px 6px 0}
td{border-bottom:1px solid var(--line);padding:6px 8px 6px 0;vertical-align:top;word-break:break-word}
td.num,th.num{text-align:right;white-space:nowrap}
tr.bold td{font-weight:600}
ul.bullets{margin:0;padding-left:18px}
footer{color:var(--faint);font-size:12px;text-align:center;margin-top:24px}
@media (max-width:600px){header.cover,section{padding:20px 16px}.cover h1{font-size:22px}table{font-size:13px}}
@page{size:A4;margin:14mm}
@media print{body{background:#fff;font-size:12px}.page{max-width:none;padding:0}.nav{display:none}
 header.cover,section{border:1px solid var(--line);break-inside:avoid-page;padding:18px 20px}section table{break-inside:auto}tr{break-inside:avoid}}
</style></head>
<body><div class="page">
{{if and (not .V.Print) (or .V.BackURL .V.PDFURL)}}<div class="nav">{{if .V.BackURL}}<a href="{{.V.BackURL}}">All reports</a>{{else}}<span></span>{{end}}{{if .V.PDFURL}}<a href="{{.V.PDFURL}}">Download PDF</a>{{end}}</div>{{end}}
{{if .V.Draft}}<div class="banner draft"><strong>Draft.</strong> Only your team can see this until it is published.</div>{{end}}
{{if or .Omitted .Warnings}}<div class="banner team"><strong>For your team only; the published report leaves this out.</strong>
{{if .Omitted}}<ul>{{range .Omitted}}<li>Not included. {{.}}</li>{{end}}</ul>{{end}}
{{if .Warnings}}<ul>{{range .Warnings}}<li>Incomplete. {{.}}</li>{{end}}</ul>{{end}}</div>{{end}}
<header class="cover">
<p class="org">{{.V.Snapshot.Org}}</p>
<h1>{{.V.Title}}</h1>
<p class="period">{{.V.Snapshot.Period.Label}}, compared with {{.V.Snapshot.Previous.Label}}{{if .V.PublishedAt}}. Published {{date .V.PublishedAt}}{{if gt .V.Version 1}} (revision {{.V.Version}}){{end}}{{end}}</p>
</header>
{{if .Summary}}<section class="summary"><h2>Summary</h2>{{range .Summary}}<p>{{.}}</p>{{end}}</section>{{end}}
{{range .Blocks}}<section id="{{.Key}}">
<h2>{{.Title}}</h2>{{if .Intro}}<p class="intro">{{.Intro}}</p>{{end}}
{{if .Note}}<div class="note">{{range paragraphs .Note}}<p style="margin:0 0 8px">{{.}}</p>{{end}}</div>{{end}}
{{if .Metrics}}<div class="metrics">{{range .Metrics}}<div class="metric"><div class="label">{{.Label}}</div><div class="value">{{.Value}}</div>{{if .Change}}<div class="change {{good .Good}}">{{.Change}}</div>{{end}}</div>{{end}}</div>{{end}}
{{with .Chart}}<figure>{{chart . $.Accent}}<figcaption>{{.Caption}}</figcaption></figure>{{end}}
{{range .Tables}}{{if .Title}}<h3>{{.Title}}</h3>{{end}}<table><thead><tr>{{range $i, $h := .Head}}<th{{if $i}} class="num"{{end}}>{{$h}}</th>{{end}}</tr></thead><tbody>
{{$t := .}}{{range $r, $row := .Rows}}<tr{{if bold $t $r}} class="bold"{{end}}>{{range $i, $c := $row}}<td{{if $i}} class="num"{{end}}>{{$c}}</td>{{end}}</tr>{{end}}</tbody></table>{{end}}
{{if .Bullets}}<ul class="bullets">{{range .Bullets}}<li>{{.}}</li>{{end}}</ul>{{end}}
</section>{{end}}
<footer>Figures from {{.Sources}}. Prepared with Vellatry.</footer>
</div></body></html>`))

var sydneyLoc *time.Location

func sydney() *time.Location {
	if sydneyLoc == nil {
		if loc, err := time.LoadLocation("Australia/Sydney"); err == nil {
			sydneyLoc = loc
		} else {
			sydneyLoc = time.UTC
		}
	}
	return sydneyLoc
}

// RenderText renders the report's data as plain text: what the figure check allows and
// what the summary drafter reads. Team notes and the summary are left out.
func RenderText(s Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s report for %s, %s, compared with %s.\n", s.Brand, s.Org, s.Period.Label, s.Previous.Label)
	for _, blk := range Layout(s, nil) {
		fmt.Fprintf(&b, "\n## %s\n", blk.Title)
		if blk.Intro != "" {
			b.WriteString(blk.Intro + "\n")
		}
		for _, m := range blk.Metrics {
			fmt.Fprintf(&b, "- %s: %s", m.Label, m.Value)
			if m.Change != "" {
				fmt.Fprintf(&b, " (%s)", m.Change)
			}
			b.WriteString("\n")
		}
		if blk.Chart != nil {
			b.WriteString("(chart: " + blk.Chart.Caption + ")\n")
		}
		for _, t := range blk.Tables {
			if t.Title != "" {
				b.WriteString(t.Title + ":\n")
			}
			b.WriteString("| " + strings.Join(t.Head, " | ") + " |\n")
			for _, r := range t.Rows {
				b.WriteString("| " + strings.Join(r, " | ") + " |\n")
			}
		}
		for _, x := range blk.Bullets {
			b.WriteString("- " + x + "\n")
		}
	}
	return b.String()
}

var numberRE = regexp.MustCompile(`\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?`)

// canonical normalises "1,234.50" to "1234.5" and "42.0" to "42".
func canonical(n string) string {
	f, err := strconv.ParseFloat(strings.ReplaceAll(n, ",", ""), 64)
	if err != nil {
		return n
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Figures is the set of numbers a report shows.
func Figures(s Snapshot) map[string]bool {
	out := map[string]bool{}
	for _, n := range numberRE.FindAllString(RenderText(s), -1) {
		out[canonical(n)] = true
	}
	return out
}

// Unverified returns the numbers in text that the report does not show, in order of
// appearance. A summary may only repeat the report's own figures.
func Unverified(text string, allowed map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range numberRE.FindAllString(text, -1) {
		c := canonical(n)
		if !allowed[c] && !seen[c] {
			seen[c] = true
			out = append(out, n)
		}
	}
	return out
}

// KeepVerified drops every paragraph that cites a number the report does not show, and
// says which were dropped. Used on the model's draft, never on the team's own words.
func KeepVerified(paragraphs []string, allowed map[string]bool) (kept []string, dropped int) {
	for _, p := range paragraphs {
		if len(Unverified(p, allowed)) > 0 {
			dropped++
			continue
		}
		kept = append(kept, p)
	}
	return kept, dropped
}

// SortedSections returns the known sections of keys in canonical order, dropping
// unknown and repeated keys.
func SortedSections(keys []string) []string {
	rank := map[string]int{}
	for i, k := range AllSections {
		rank[k] = i
	}
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		if _, ok := rank[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i]] < rank[out[j]] })
	return out
}
