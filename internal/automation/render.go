package automation

import (
	"bytes"
	"encoding/json"
	"fmt"
	htmltemplate "html/template"
	"math"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"
)

// Rendered is one notification in every format a destination needs.
type Rendered struct {
	Subject string
	Text    string
	HTML    string
	Slack   []byte // an incoming-webhook payload
}

// Render formats a stored notification. appURL is the web app's base URL, for links.
func Render(n Stored, appURL string) (Rendered, error) {
	if n.Kind == "digest" {
		var d Digest
		if err := json.Unmarshal(n.Data, &d); err != nil {
			return Rendered{}, fmt.Errorf("automation: digest data: %w", err)
		}
		return RenderDigest(d, appURL)
	}
	link := ""
	if n.Link != nil && *n.Link != "" {
		link = appURL + *n.Link
	}
	r := Rendered{Subject: n.Title}
	var text strings.Builder
	text.WriteString(n.Title + "\n\n" + n.Body + "\n")
	if n.Occurrences > 1 {
		fmt.Fprintf(&text, "\nSeen %d times.\n", n.Occurrences)
	}
	if link != "" {
		text.WriteString("\nOpen in Vellatry: " + link + "\n")
	}
	r.Text = text.String()

	var html bytes.Buffer
	if err := alertHTML.Execute(&html, map[string]any{"N": n, "Link": link, "Colour": severityColour(n.Severity)}); err != nil {
		return r, err
	}
	r.HTML = html.String()

	line := "*" + slackEscape(n.Title) + "*"
	if link != "" {
		line = "*<" + link + "|" + slackEscape(n.Title) + ">*"
	}
	blocks := []any{
		section(line + "\n" + slackEscape(n.Body)),
	}
	if n.Occurrences > 1 {
		blocks = append(blocks, contextBlock(fmt.Sprintf("Seen %d times", n.Occurrences)))
	}
	r.Slack, _ = json.Marshal(map[string]any{"text": n.Title, "blocks": blocks})
	return r, nil
}

func severityColour(s string) string {
	switch s {
	case "critical":
		return "#b42318"
	case "warning":
		return "#b54708"
	}
	return "#344054"
}

// slackEscape escapes the three characters Slack's mrkdwn treats as control characters.
// Prompts, competitor names and page titles come from customers and AI answers.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func section(text string) map[string]any {
	return map[string]any{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": text}}
}

func contextBlock(text string) map[string]any {
	return map[string]any{"type": "context", "elements": []map[string]string{{"type": "mrkdwn", "text": text}}}
}

var alertHTML = htmltemplate.Must(htmltemplate.New("alert").Parse(`<!doctype html>
<html><body style="margin:0;padding:24px;background:#f6f7f9;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#101828">
<table role="presentation" width="100%" style="max-width:560px;margin:0 auto;background:#fff;border-radius:8px;border-top:4px solid {{.Colour}}">
<tr><td style="padding:24px">
<h1 style="font-size:18px;margin:0 0 12px">{{.N.Title}}</h1>
<p style="font-size:15px;line-height:1.5;margin:0 0 16px;white-space:pre-line">{{.N.Body}}</p>
{{if gt .N.Occurrences 1}}<p style="font-size:13px;color:#667085;margin:0 0 16px">Seen {{.N.Occurrences}} times.</p>{{end}}
{{if .Link}}<a href="{{.Link}}" style="display:inline-block;background:#101828;color:#fff;text-decoration:none;padding:10px 16px;border-radius:6px;font-size:14px">Open in Vellatry</a>{{end}}
</td></tr></table>
<p style="max-width:560px;margin:16px auto 0;font-size:12px;color:#98a2b3">You receive this because a Vellatry watcher sends alerts here. Change it under Automations.</p>
</body></html>`))

// ---- digest --------------------------------------------------------------------------

// Formatting helpers shared by the digest templates.
func fmtPct(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return strconv.FormatFloat(*v, 'f', 1, 64) + "%"
}

func fmtPoints(cur, prev *float64) string {
	if cur == nil || prev == nil {
		return ""
	}
	d := math.Round((*cur-*prev)*10) / 10
	switch {
	case d > 0:
		return fmt.Sprintf("up %.1f pts", d)
	case d < 0:
		return fmt.Sprintf("down %.1f pts", -d)
	}
	return "no change"
}

func fmtInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func fmtChange(cur, prev int64) string {
	if prev == 0 {
		if cur == 0 {
			return "no change"
		}
		return "new"
	}
	d := 100 * float64(cur-prev) / float64(prev)
	switch {
	case d >= 0.05:
		return fmt.Sprintf("up %.1f%%", d)
	case d <= -0.05:
		return fmt.Sprintf("down %.1f%%", -d)
	}
	return "no change"
}

func fmtPos(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return strconv.FormatFloat(*v, 'f', 1, 64)
}

func fmtDate(s string) string {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return s
	}
	return t.Format("2 Jan")
}

var digestFuncs = map[string]any{
	"pct": fmtPct, "points": fmtPoints, "int": fmtInt, "change": fmtChange, "pos": fmtPos, "date": fmtDate,
	"engine": EngineName, "slack": slackEscape,
	"i64": func(i int) int64 { return int64(i) },
}

// RenderDigest formats the weekly digest.
func RenderDigest(d Digest, appURL string) (Rendered, error) {
	data := map[string]any{"D": d, "App": appURL}
	r := Rendered{Subject: fmt.Sprintf("%s weekly: %s to %s", d.Brand, fmtDate(d.From), fmtDate(d.To))}
	var b bytes.Buffer
	if err := digestText.Execute(&b, data); err != nil {
		return r, err
	}
	r.Text = b.String()
	b.Reset()
	if err := digestHTML.Execute(&b, data); err != nil {
		return r, err
	}
	r.HTML = b.String()
	b.Reset()
	if err := digestSlack.Execute(&b, data); err != nil {
		return r, err
	}
	var blocks []any
	for _, part := range strings.Split(strings.TrimSpace(b.String()), "\n\n") {
		if part = strings.TrimSpace(part); part != "" {
			if len(part) > maxSlackSection {
				part = part[:strings.LastIndex(part[:maxSlackSection], "\n")] + "\n(more in Vellatry)"
			}
			blocks = append(blocks, section(part))
		}
	}
	r.Slack, _ = json.Marshal(map[string]any{"text": r.Subject, "blocks": blocks})
	return r, nil
}

// maxSlackSection keeps a section under Slack's 3,000-character limit.
const maxSlackSection = 2900

// The Slack template is split into blocks on blank lines. Everything user-supplied goes
// through slack.
var digestSlack = texttemplate.Must(texttemplate.New("slack").Funcs(digestFuncs).Parse(
	`*{{slack .D.Brand}} weekly*, {{date .D.From}} to {{date .D.To}}
{{with .D.Visibility}}
*AI visibility* {{pct .Visibility}}{{with points .Visibility .Previous}} ({{.}}){{end}}, share of voice {{pct .ShareOfVoice}}{{range .ByEngine}}
• {{engine .Engine}}: {{pct .Visibility}}{{with points .Visibility .Previous}} ({{.}}){{end}}{{end}}
{{end}}{{with .D.Blindspots}}
*Blindspots* {{.New}} new, {{.Resolved}} resolved, {{.Open}} open{{range .Top}}
• {{engine .Engine}}: "{{slack .Prompt}}"{{with .Competitor}}, {{slack .}} named instead{{end}}{{end}}
{{end}}{{with .D.Search}}
*Search Console* ({{date .From}} to {{date .To}}) {{int .Clicks}} clicks ({{change .Clicks .PrevClicks}}), {{int .Impressions}} impressions ({{change .Impressions .PrevImpressions}}), average position {{pos .Position}}
{{end}}{{with .D.AIReferrals}}
*AI referrals* ({{date .From}} to {{date .To}}) {{int .Sessions}} sessions ({{change .Sessions .PrevSessions}}){{range .Top}}
• {{slack .Name}}: {{int .Sessions}}{{end}}
{{end}}{{with .D.Site}}
*Site* {{.Critical}} critical and {{.Warning}} warning issues open, {{.FixesLive}} fixes went live, {{.FixesProposed}} waiting (last crawl {{date .LastCrawl}})
{{end}}{{if .D.Alerts}}
*Alerts*{{range .D.Alerts}}
• {{if .Link}}<{{$.App}}{{.Link}}|{{slack .Title}}>{{else}}{{slack .Title}}{{end}}: {{slack .Body}}{{end}}
{{end}}{{if .D.Broken}}
*Needs reconnecting:* {{range $i, $b := .D.Broken}}{{if $i}}, {{end}}{{slack $b}}{{end}}. <{{.App}}/settings/connections|Fix it>
{{end}}
<{{.App}}|Open Vellatry>`))

var digestText = texttemplate.Must(texttemplate.New("text").Funcs(digestFuncs).Parse(
	`{{.D.Brand}} weekly, {{date .D.From}} to {{date .D.To}}
{{with .D.Visibility}}
AI VISIBILITY
{{pct .Visibility}}{{with points .Visibility .Previous}} ({{.}}){{end}} of {{.Answers}} AI answers mentioned you. Share of voice {{pct .ShareOfVoice}}.
{{range .ByEngine}}- {{engine .Engine}}: {{pct .Visibility}}{{with points .Visibility .Previous}} ({{.}}){{end}}
{{end}}{{end}}{{with .D.Blindspots}}
BLINDSPOTS
{{.New}} new, {{.Resolved}} resolved, {{.Open}} open.
{{range .Top}}- {{engine .Engine}}: "{{.Prompt}}"{{with .Competitor}}, {{.}} named instead{{end}}
{{end}}{{end}}{{with .D.Search}}
SEARCH CONSOLE ({{date .From}} to {{date .To}})
{{int .Clicks}} clicks ({{change .Clicks .PrevClicks}}), {{int .Impressions}} impressions ({{change .Impressions .PrevImpressions}}), average position {{pos .Position}}.
{{end}}{{with .D.AIReferrals}}
AI REFERRALS ({{date .From}} to {{date .To}})
{{int .Sessions}} sessions ({{change .Sessions .PrevSessions}}).
{{range .Top}}- {{.Name}}: {{int .Sessions}}
{{end}}{{end}}{{with .D.Site}}
SITE
{{.Critical}} critical and {{.Warning}} warning issues open. {{.FixesLive}} fixes went live this week, {{.FixesProposed}} waiting. Last crawl {{date .LastCrawl}}.
{{end}}{{if .D.Alerts}}
ALERTS
{{range .D.Alerts}}- {{.Title}}: {{.Body}}{{if .Link}} {{$.App}}{{.Link}}{{end}}
{{end}}{{end}}{{if .D.Broken}}
NEEDS RECONNECTING: {{range $i, $b := .D.Broken}}{{if $i}}, {{end}}{{$b}}{{end}}. {{.App}}/settings/connections
{{end}}
Open Vellatry: {{.App}}
`))

var digestHTML = htmltemplate.Must(htmltemplate.New("html").Funcs(digestFuncs).Parse(`<!doctype html>
<html><body style="margin:0;padding:24px;background:#f6f7f9;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#101828">
<table role="presentation" width="100%" style="max-width:600px;margin:0 auto;background:#fff;border-radius:8px">
<tr><td style="padding:24px 24px 8px">
<h1 style="font-size:20px;margin:0">{{.D.Brand}} weekly</h1>
<p style="margin:4px 0 0;color:#667085;font-size:14px">{{date .D.From}} to {{date .D.To}}</p>
</td></tr>
{{with .D.Visibility}}<tr><td style="padding:16px 24px;border-top:1px solid #eaecf0">
<h2 style="font-size:15px;margin:0 0 8px">AI visibility</h2>
<p style="margin:0 0 8px;font-size:14px"><strong style="font-size:22px">{{pct .Visibility}}</strong> {{with points .Visibility .Previous}}<span style="color:#667085">{{.}}</span>{{end}}<br>of {{.Answers}} AI answers mentioned you. Share of voice {{pct .ShareOfVoice}}.</p>
<table role="presentation" style="font-size:14px">{{range .ByEngine}}<tr><td style="padding:2px 16px 2px 0">{{engine .Engine}}</td><td style="padding:2px 16px 2px 0">{{pct .Visibility}}</td><td style="color:#667085">{{points .Visibility .Previous}}</td></tr>{{end}}</table>
</td></tr>{{end}}
{{with .D.Blindspots}}<tr><td style="padding:16px 24px;border-top:1px solid #eaecf0">
<h2 style="font-size:15px;margin:0 0 8px">Blindspots</h2>
<p style="margin:0 0 8px;font-size:14px">{{.New}} new, {{.Resolved}} resolved, {{.Open}} open.</p>
{{if .Top}}<ul style="margin:0;padding-left:18px;font-size:14px">{{range .Top}}<li style="margin-bottom:4px"><a href="{{$.App}}/visibility/blindspots/{{.ID}}" style="color:#101828">{{engine .Engine}}: "{{.Prompt}}"</a>{{with .Competitor}}, {{.}} named instead{{end}}</li>{{end}}</ul>{{end}}
</td></tr>{{end}}
{{with .D.Search}}<tr><td style="padding:16px 24px;border-top:1px solid #eaecf0">
<h2 style="font-size:15px;margin:0 0 8px">Search Console <span style="font-weight:normal;color:#667085">{{date .From}} to {{date .To}}</span></h2>
<p style="margin:0;font-size:14px">{{int .Clicks}} clicks ({{change .Clicks .PrevClicks}}), {{int .Impressions}} impressions ({{change .Impressions .PrevImpressions}}), average position {{pos .Position}}.</p>
</td></tr>{{end}}
{{with .D.AIReferrals}}<tr><td style="padding:16px 24px;border-top:1px solid #eaecf0">
<h2 style="font-size:15px;margin:0 0 8px">AI referrals <span style="font-weight:normal;color:#667085">{{date .From}} to {{date .To}}</span></h2>
<p style="margin:0 0 4px;font-size:14px">{{int .Sessions}} sessions ({{change .Sessions .PrevSessions}}).</p>
{{if .Top}}<p style="margin:0;font-size:14px;color:#475467">{{range $i, $t := .Top}}{{if $i}} · {{end}}{{$t.Name}} {{int $t.Sessions}}{{end}}</p>{{end}}
</td></tr>{{end}}
{{with .D.Site}}<tr><td style="padding:16px 24px;border-top:1px solid #eaecf0">
<h2 style="font-size:15px;margin:0 0 8px">Site</h2>
<p style="margin:0;font-size:14px">{{.Critical}} critical and {{.Warning}} warning issues open. {{.FixesLive}} fixes went live this week, {{.FixesProposed}} waiting. Last crawl {{date .LastCrawl}}.</p>
</td></tr>{{end}}
{{if .D.Alerts}}<tr><td style="padding:16px 24px;border-top:1px solid #eaecf0">
<h2 style="font-size:15px;margin:0 0 8px">Alerts</h2>
<ul style="margin:0;padding-left:18px;font-size:14px">{{range .D.Alerts}}<li style="margin-bottom:6px">{{if .Link}}<a href="{{$.App}}{{.Link}}" style="color:#101828"><strong>{{.Title}}</strong></a>{{else}}<strong>{{.Title}}</strong>{{end}}<br><span style="color:#475467">{{.Body}}</span></li>{{end}}</ul>
</td></tr>{{end}}
{{if .D.Broken}}<tr><td style="padding:16px 24px;border-top:1px solid #eaecf0;background:#fffaeb">
<p style="margin:0;font-size:14px"><strong>Needs reconnecting:</strong> {{range $i, $b := .D.Broken}}{{if $i}}, {{end}}{{$b}}{{end}}. <a href="{{.App}}/settings/connections">Fix it</a></p>
</td></tr>{{end}}
<tr><td style="padding:16px 24px 24px;border-top:1px solid #eaecf0"><a href="{{.App}}" style="display:inline-block;background:#101828;color:#fff;text-decoration:none;padding:10px 16px;border-radius:6px;font-size:14px">Open Vellatry</a></td></tr>
</table>
<p style="max-width:600px;margin:16px auto 0;font-size:12px;color:#98a2b3">The weekly digest goes to destinations with the digest switched on. Change it under Automations.</p>
</body></html>`))
