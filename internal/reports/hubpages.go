package reports

import (
	"bytes"
	"html/template"
	"time"
)

// HubPage is one page of the reports hub outside a report itself.
type HubPage struct {
	Kind    string // login | sent | confirm | list | message
	Org     string
	Brand   string
	Action  string // form action
	Token   string // confirm: the sign-in token, posted rather than used on GET
	Email   string
	Error   string
	Message string
	Viewer  string
	Reports []HubEntry
	Base    string // /hub/<slug>
}

// RenderHubPage renders a hub page.
func RenderHubPage(p HubPage) ([]byte, error) {
	var b bytes.Buffer
	err := hubHTML.Execute(&b, p)
	return b.Bytes(), err
}

var hubHTML = template.Must(template.New("hub").Funcs(template.FuncMap{
	"date": func(t time.Time) string { return t.In(sydney()).Format("2 Jan 2006") },
}).Parse(`<!doctype html>
<html lang="en-AU"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow"><title>{{if .Brand}}{{.Brand}} reports{{else}}Reports{{end}}</title>
<style>
*{box-sizing:border-box}
body{margin:0;background:#f6f7f9;color:#101828;font:15px/1.55 -apple-system,"Segoe UI",Roboto,Helvetica,Arial,sans-serif}
.wrap{max-width:720px;margin:0 auto;padding:40px 16px}
.card{background:#fff;border-radius:8px;padding:28px}
h1{font-size:22px;margin:0 0 8px}
p{margin:0 0 16px}
.muted{color:#475467}
label{display:block;font-size:14px;font-weight:600;margin-bottom:6px}
input[type=email]{width:100%;font:inherit;padding:10px 12px;border:1px solid #d0d5dd;border-radius:6px;margin-bottom:16px}
button{font:inherit;background:#101828;color:#fff;border:0;border-radius:6px;padding:10px 16px;cursor:pointer}
.error{background:#fef3f2;border:1px solid #fecdca;color:#b42318;border-radius:6px;padding:10px 12px;margin-bottom:16px;font-size:14px}
ul.reports{list-style:none;margin:0;padding:0}
ul.reports li{border-top:1px solid #eaecf0;padding:14px 0;display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap}
ul.reports a{color:#101828;font-weight:600;text-decoration:none}
ul.reports .meta{color:#667085;font-size:14px}
.top{display:flex;justify-content:space-between;align-items:center;gap:12px;margin-bottom:16px;font-size:14px;color:#475467}
.top form{margin:0}.top button{background:none;color:#475467;padding:0;text-decoration:underline}
</style></head><body><div class="wrap">
{{if eq .Kind "list"}}
<div class="top"><span>Signed in as {{.Viewer}}</span><form method="post" action="{{.Base}}/logout"><button type="submit">Sign out</button></form></div>
<div class="card"><h1>{{.Brand}} reports</h1><p class="muted">{{.Org}}</p>
{{if .Reports}}<ul class="reports">{{range .Reports}}<li><div><a href="{{$.Base}}/reports/{{.ID}}">{{.Title}}</a><div class="meta">{{.SeriesName}}{{if .Label}} · {{.Label}}{{end}}</div></div>
<div class="meta">Published {{date .PublishedAt}}{{if gt .Version 1}}, revision {{.Version}}{{end}}{{if .PDFReady}} · <a href="{{$.Base}}/reports/{{.ID}}/pdf">PDF</a>{{end}}</div></li>{{end}}</ul>
{{else}}<p class="muted">No reports have been published yet. You will get an email when the first one is ready.</p>{{end}}
</div>
{{else if eq .Kind "login"}}
<div class="card"><h1>{{if .Brand}}{{.Brand}} reports{{else}}Reports{{end}}</h1>
<p class="muted">Enter your work email and we will send you a link to sign in. No password needed.</p>
{{if .Error}}<div class="error">{{.Error}}</div>{{end}}
<form method="post" action="{{.Action}}"><label for="email">Work email</label>
<input id="email" name="email" type="email" autocomplete="email" required value="{{.Email}}">
<button type="submit">Email me a sign-in link</button></form></div>
{{else if eq .Kind "sent"}}
<div class="card"><h1>Check your email</h1><p>If {{.Email}} can view these reports, a sign-in link is on its way. It works once and expires in 20 minutes.</p>
<p class="muted"><a href="{{.Base}}">Back</a></p></div>
{{else if eq .Kind "confirm"}}
<div class="card"><h1>Sign in to {{if .Brand}}{{.Brand}} reports{{else}}reports{{end}}</h1>
<form method="post" action="{{.Action}}"><input type="hidden" name="token" value="{{.Token}}"><button type="submit">Continue</button></form></div>
{{else}}
<div class="card"><h1>{{if .Error}}{{.Error}}{{else}}Reports{{end}}</h1>{{if .Message}}<p class="muted">{{.Message}}</p>{{end}}{{if .Base}}<p><a href="{{.Base}}">Go to reports</a></p>{{end}}</div>
{{end}}
</div></body></html>`))
