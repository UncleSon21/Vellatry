package detect

import (
	"strings"
	"testing"
)

var koala = Entity{
	Name:       "Koala",
	Aliases:    []string{"Koala Mattress"},
	Exclusions: []string{"koala bear", "koalas"},
	Domains:    []string{"koala.com"},
}

var competitors = []Entity{
	{Name: "Ecosa", Domains: []string{"ecosa.com.au"}},
	{Name: "Emma", Aliases: []string{"Emma Sleep"}, Domains: []string{"emma-sleep.com.au"}},
}

func TestAnalyze(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		citations     []string
		wantCount     int
		wantPosition  int
		wantCited     bool
		wantCompetito bool
		wantCitedComp []string
	}{
		{
			name:         "alias and name counted once each, not double",
			text:         "The Koala Mattress is popular. Many buyers pick Koala for delivery.",
			wantCount:    2,
			wantPosition: 1,
		},
		{
			name:      "exclusion masks lookalike",
			text:      "A koala bear sleeps 20 hours a day, and koalas live in trees.",
			wantCount: 0,
		},
		{
			name:      "no match inside a longer word",
			text:      "Koalaville is a town name.",
			wantCount: 0,
		},
		{
			name:          "position counts competitors mentioned earlier",
			text:          "Ecosa and Emma Sleep lead the market, while Koala is a close third.",
			wantCount:     1,
			wantPosition:  3,
			wantCompetito: true,
		},
		{
			name:          "citations match subdomains and strip www",
			text:          "Several options exist.",
			citations:     []string{"https://www.koala.com/en-au/mattress", "https://shop.ecosa.com.au/x", "https://example.com"},
			wantCited:     true,
			wantCitedComp: []string{"Ecosa"},
		},
		{
			name:      "case insensitive with punctuation boundaries",
			text:      "(KOALA) vs. others",
			wantCount: 1, wantPosition: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Analyze(tt.text, tt.citations, koala, competitors)
			if r.Brand.Count != tt.wantCount {
				t.Errorf("count = %d, want %d", r.Brand.Count, tt.wantCount)
			}
			if r.BrandPosition != tt.wantPosition {
				t.Errorf("position = %d, want %d", r.BrandPosition, tt.wantPosition)
			}
			if r.BrandCited != tt.wantCited {
				t.Errorf("cited = %v, want %v", r.BrandCited, tt.wantCited)
			}
			if r.CompetitorMentioned() != tt.wantCompetito {
				t.Errorf("competitor mentioned = %v, want %v", r.CompetitorMentioned(), tt.wantCompetito)
			}
			if len(r.CitedCompetitors) != len(tt.wantCitedComp) {
				t.Fatalf("cited competitors = %v, want %v", r.CitedCompetitors, tt.wantCitedComp)
			}
			for i := range tt.wantCitedComp {
				if r.CitedCompetitors[i] != tt.wantCitedComp[i] {
					t.Errorf("cited competitors = %v, want %v", r.CitedCompetitors, tt.wantCitedComp)
				}
			}
		})
	}
}

func TestHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://WWW.Koala.com/path": "koala.com",
		"http://shop.example.com.au": "shop.example.com.au",
		"not a url":                  "",
	} {
		if got := Host(in); got != want {
			t.Errorf("Host(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHighlight(t *testing.T) {
	brand := Entity{Name: "Koala", Aliases: []string{"Koala Sleep"}, Exclusions: []string{"koala bear"}}
	comps := []Entity{{Name: "Ecosa"}}
	text := "Koala Sleep beats Ecosa. A koala bear is not a mattress, but Koala is."
	segs := Highlight(text, brand, comps)

	var joined string
	var marked []string
	for _, s := range segs {
		joined += s.Text
		switch {
		case s.Excluded:
			marked = append(marked, "x:"+s.Text)
		case s.Entity != "":
			marked = append(marked, s.Entity+":"+s.Text)
		}
	}
	if joined != text {
		t.Fatalf("segments do not rebuild the text:\n%q\n%q", joined, text)
	}
	want := []string{"Koala:Koala Sleep", "Ecosa:Ecosa", "x:koala bear", "Koala:Koala"}
	if strings.Join(marked, "|") != strings.Join(want, "|") {
		t.Errorf("marks = %v, want %v", marked, want)
	}

	// The preview must agree with what the engine counts.
	r := Analyze(text, nil, brand, comps)
	brandMarks := 0
	for _, s := range segs {
		if s.Brand && !s.Excluded {
			brandMarks++
		}
	}
	if brandMarks != r.Brand.Count {
		t.Errorf("highlighted %d brand mentions, Analyze counted %d", brandMarks, r.Brand.Count)
	}
}

func TestHighlightNoMentions(t *testing.T) {
	segs := Highlight("Nothing to see here.", Entity{Name: "Koala"}, nil)
	if len(segs) != 1 || segs[0].Entity != "" || segs[0].Text != "Nothing to see here." {
		t.Errorf("segments = %+v", segs)
	}
}
