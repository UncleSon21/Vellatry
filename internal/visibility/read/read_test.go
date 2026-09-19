package read

import "testing"

func TestClassify(t *testing.T) {
	comps := []string{"ecosa.com.au"}
	for domain, want := range map[string]SourceType{
		"www.koala.com":          Owned,
		"shop.koala.com":         Owned,
		"ecosa.com.au":           Competitor,
		"www.reddit.com":         UGC,
		"productreview.com.au":   Review,
		"en.wikipedia.org":       Reference,
		"health.gov.au":          Reference,
		"smh.com.au":             Editorial,
		"notkoala.com":           Editorial, // suffix without a dot boundary is not the brand
		"koala.com.evil.example": Editorial,
	} {
		if got := Classify(domain, "koala.com", comps); got != want {
			t.Errorf("Classify(%q) = %s, want %s", domain, got, want)
		}
	}
}

func TestScorePriority(t *testing.T) {
	demand := 1000
	p := ScorePriority(&demand, "displacement", true)
	if p.Demand != 4 || p.Severity != 1 || p.Certainty != 1 || p.Score != 4 {
		t.Errorf("priority = %+v", p)
	}
	q := ScorePriority(nil, "visibility", false)
	if q.Score != 0.45 {
		t.Errorf("unknown demand, provisional visibility: %+v", q)
	}
	if ScorePriority(&demand, "visibility", false).Score >= p.Score {
		t.Error("a confirmed displacement must outrank a provisional visibility gap at equal demand")
	}
}

func TestPct(t *testing.T) {
	if pct(1, 0) != nil {
		t.Error("no denominator should be nil, not 0")
	}
	if *pct(1, 3) != 33.33 {
		t.Errorf("pct(1,3) = %v", *pct(1, 3))
	}
}
