package reports

import (
	"strings"
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestPeriods(t *testing.T) {
	cases := []struct {
		rule, day, start, end, label string
	}{
		{Month, "2026-08-17", "2026-08-01", "2026-08-31", "August 2026"},
		{Month, "2028-02-10", "2028-02-01", "2028-02-29", "February 2028"},
		{FYQuarter, "2026-07-01", "2026-07-01", "2026-09-30", "Q1 FY27 (Jul to Sep 2026)"},
		{FYQuarter, "2026-12-31", "2026-10-01", "2026-12-31", "Q2 FY27 (Oct to Dec 2026)"},
		{FYQuarter, "2027-02-14", "2027-01-01", "2027-03-31", "Q3 FY27 (Jan to Mar 2027)"},
		{FYQuarter, "2027-06-30", "2027-04-01", "2027-06-30", "Q4 FY27 (Apr to Jun 2027)"},
		{FY, "2026-06-30", "2025-07-01", "2026-06-30", "FY26 (July 2025 to June 2026)"},
		{FY, "2026-07-01", "2026-07-01", "2027-06-30", "FY27 (July 2026 to June 2027)"},
	}
	for _, c := range cases {
		p, err := Containing(c.rule, d(c.day))
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Start.Format(time.DateOnly) + " " + p.End.Format(time.DateOnly) + " " + p.Label; got != c.start+" "+c.end+" "+c.label {
			t.Errorf("Containing(%s, %s) = %s", c.rule, c.day, got)
		}
	}
	p, _ := LatestComplete(FYQuarter, d("2026-10-03"))
	if p.Start != d("2026-07-01") || p.End != d("2026-09-30") {
		t.Errorf("latest complete quarter = %v", p)
	}
	if prev := Previous(FYQuarter, p); prev.Start != d("2026-04-01") || prev.End != d("2026-06-30") {
		t.Errorf("previous quarter = %v", prev)
	}
	c, err := CustomPeriod(d("2026-08-10"), d("2026-08-23"))
	if err != nil || c.Days() != 14 || c.Label != "10 Aug to 23 Aug 2026" {
		t.Errorf("custom = %+v %v", c, err)
	}
	if prev := Previous(Custom, c); prev.Start != d("2026-07-27") || prev.End != d("2026-08-09") {
		t.Errorf("previous custom = %v", prev)
	}
	if _, err := CustomPeriod(d("2026-08-10"), d("2026-08-01")); err == nil {
		t.Error("reversed range accepted")
	}
	if _, err := CustomPeriod(d("2025-01-01"), d("2026-06-01")); err == nil {
		t.Error("range over 366 days accepted")
	}
}

func fp(v float64) *float64 { return &v }
func ip(v int64) *int64     { return &v }

func sample() Snapshot {
	comp := "Snooze"
	return Snapshot{
		Version: SnapshotVersion, Org: "Koala Pty Ltd", Brand: "Koala",
		Period:   Period{Start: d("2026-08-01"), End: d("2026-08-31"), Label: "August 2026"},
		Previous: Period{Start: d("2026-07-01"), End: d("2026-07-31"), Label: "July 2026"},
		Order:    []string{SecVisibility, SecBlindspots, SecSearch},
		Visibility: &VisibilitySection{Answers: 1240, Visibility: Pair{fp(42.5), fp(38.1)}, ShareOfVoice: Pair{fp(31.2), fp(30)},
			ByEngine: []EngineRow{{Engine: "chatgpt", Answers: 600, Visibility: Pair{fp(50), fp(44)}}, {Engine: "gemini", Answers: 640, Visibility: Pair{fp(35.4), nil}}},
			Entities: []EntityRow{{Name: "Koala", IsBrand: true, Visibility: fp(42.5), ShareOfVoice: fp(31.2)}, {Name: "Snooze", Visibility: fp(51), ShareOfVoice: fp(40.3)}},
			Series:   []DayValue{{"2026-08-01", fp(40)}, {"2026-08-02", fp(44)}}},
		Blindspots: &BlindspotSection{Confirmed: 3, Resolved: 2, OpenNow: 7,
			Top: []BlindspotRow{{Engine: "chatgpt", Kind: "displacement", Prompt: `best <script>alert(1)</script> mattress`, Competitor: &comp}}},
		Search: &SearchSection{Clicks: IntPair{12345, ip(11000)}, Impressions: IntPair{250000, ip(260000)}, CTR: Pair{fp(4.9), fp(4.2)}, Position: Pair{fp(8.3), fp(9.1)},
			Queries: []TopRow{{Key: "koala mattress", Clicks: 2100, Impressions: 9000, Position: fp(1.4)}}},
		Omitted:  []Note{{Section: SecTraffic, Reason: "Google Analytics 4 is not connected"}},
		Warnings: []Note{{Section: SecSearch, Reason: "Search Console has 29 of 31 days for this period"}},
	}
}

func TestFiguresAndVerification(t *testing.T) {
	allowed := Figures(sample())
	for _, n := range []string{"42.5", "4.4", "12345", "250000", "3.8", "31.2", "1240", "2026", "12.2", "0.8", "7"} {
		if !allowed[n] {
			t.Errorf("figure %s shown in the report is not allowed", n)
		}
	}
	good := "Koala appeared in 42.5% of AI answers in August 2026, up 4.4 pts, and search clicks reached 12,345, up 12.2%."
	if u := Unverified(good, allowed); len(u) != 0 {
		t.Errorf("verified text flagged: %v", u)
	}
	bad := "Clicks rose 15% to 12,345 and visibility is now 43%."
	if u := Unverified(bad, allowed); strings.Join(u, ",") != "15,43" {
		t.Errorf("unverified = %v, want 15 and 43", u)
	}
	kept, dropped := KeepVerified([]string{good, bad, "Nothing numeric here."}, allowed)
	if dropped != 1 || len(kept) != 2 || kept[0] != good {
		t.Errorf("kept %v, dropped %d", kept, dropped)
	}
	if _, ok := Figures(sample())["40.3"]; !ok {
		t.Error("competitor figures should be allowed")
	}
}

func TestRenderAbsenceAndEscaping(t *testing.T) {
	s := sample()
	client, err := RenderHTML(View{Snapshot: s, Title: "Monthly performance: August 2026", Summary: "First.\n\nSecond <b>para</b>.",
		Notes: map[string]string{SecSearch: "Clicks up after the <i>mattress</i> guide."}, Accent: "#0e9f6e"})
	if err != nil {
		t.Fatal(err)
	}
	page := string(client)
	for _, want := range []string{"AI visibility", "42.5%", "up 4.4 pts", "Blindspots", "Recommends Snooze first", "12,345", "up 12.2%",
		"improved 0.8", "koala mattress", "<p>First.</p>", "Figures from AI answers collected by Vellatry and Google Search Console", "--accent:#0e9f6e"} {
		if !strings.Contains(page, want) {
			t.Errorf("report is missing %q", want)
		}
	}
	for _, absent := range []string{"Website traffic", "Google Analytics", "not connected", "29 of 31", "Draft.", "<script>alert", "<b>para</b>", "<i>mattress</i>"} {
		if strings.Contains(page, absent) {
			t.Errorf("client report contains %q", absent)
		}
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Error("prompt text was not escaped")
	}

	team, _ := RenderHTML(View{Snapshot: s, Title: "t", Team: true, Draft: true})
	for _, want := range []string{"For your team only", "Website traffic: Google Analytics 4 is not connected", "Google Search: Search Console has 29 of 31 days", "Draft."} {
		if !strings.Contains(string(team), want) {
			t.Errorf("team preview is missing %q", want)
		}
	}

	bad, _ := RenderHTML(View{Snapshot: s, Title: "t", Accent: "red;}body{display:none"})
	if !strings.Contains(string(bad), "--accent:"+DefaultAccent) {
		t.Error("an invalid accent must fall back to the default")
	}
	print, _ := RenderHTML(View{Snapshot: s, Title: "t", Print: true, BackURL: "/hub/x", PDFURL: "/hub/x/pdf"})
	if strings.Contains(string(print), "All reports") {
		t.Error("the PDF must not carry navigation")
	}
}

func TestRenderTextMatchesLayout(t *testing.T) {
	text := RenderText(sample())
	for _, want := range []string{"## AI visibility", "- Visibility: 42.5% (up 4.4 pts)", "| Koala | 42.5% | 31.2% |", "## Google Search", "- Clicks: 12,345 (up 12.2%)"} {
		if !strings.Contains(text, want) {
			t.Errorf("text is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Website traffic") {
		t.Error("text includes an omitted section")
	}
}

func TestEmptySnapshot(t *testing.T) {
	if !(Snapshot{}).Empty() || sample().Empty() {
		t.Error("Empty is wrong")
	}
}

func TestSortedSections(t *testing.T) {
	got := SortedSections([]string{"fixes", "search", "bogus", "search", "visibility"})
	if strings.Join(got, ",") != "visibility,search,fixes" {
		t.Errorf("sections = %v", got)
	}
}

func TestHubPages(t *testing.T) {
	page, err := RenderHubPage(HubPage{Kind: "login", Brand: "Koala <Co>", Action: "/hub/abc/login", Email: `"><script>x</script>`, Error: "Use your work email"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(page)
	if !strings.Contains(s, "Koala &lt;Co&gt;") || strings.Contains(s, "<script>x") || !strings.Contains(s, "Use your work email") {
		t.Errorf("login page not escaped or incomplete:\n%s", s)
	}
	confirm, _ := RenderHubPage(HubPage{Kind: "confirm", Action: "/hub/abc/auth", Token: "tok123"})
	if !strings.Contains(string(confirm), `method="post"`) || !strings.Contains(string(confirm), `value="tok123"`) {
		t.Error("the confirm page must post the token")
	}
	list, _ := RenderHubPage(HubPage{Kind: "list", Brand: "Koala", Viewer: "cmo@koala.com.au", Base: "/hub/abc",
		Reports: []HubEntry{{ID: "r1", SeriesName: "Monthly", Title: "Monthly: August 2026", Label: "August 2026", Version: 2, PublishedAt: d("2026-09-05"), PDFReady: true}}})
	for _, want := range []string{"/hub/abc/reports/r1", "revision 2", "/hub/abc/reports/r1/pdf", "cmo@koala.com.au"} {
		if !strings.Contains(string(list), want) {
			t.Errorf("list page missing %q", want)
		}
	}
}

func TestTokens(t *testing.T) {
	a, _ := randomToken(32)
	b, _ := randomToken(32)
	if a == b || len(a) < 40 || HashToken(a) == a || HashToken(a) != HashToken(a) {
		t.Error("tokens are not random, long or hashed consistently")
	}
	slug, err := NewSlug()
	if err != nil || len(slug) != 16 || strings.ToLower(slug) != slug {
		t.Errorf("slug = %q", slug)
	}
}
