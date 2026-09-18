package detect

import "testing"

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
