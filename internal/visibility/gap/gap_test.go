package gap

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		in   Signals
		want Verdict
	}{
		{"brand named, neutral", Signals{BrandMentioned: true, CompetitorMentioned: true}, Verdict{}},
		{"brand named, negative", Signals{BrandMentioned: true, FramingNegative: true}, Verdict{Sentiment: true}},
		{"absent, nobody winning", Signals{SourcesExist: true}, Verdict{}},
		{"absent, competitor cited only", Signals{SourcesExist: true, CompetitorCited: true}, Verdict{Visibility: true}},
		{"absent, both cited", Signals{SourcesExist: true, CompetitorCited: true, BrandCited: true}, Verdict{}},
		{"absent, competitor named, brand cited", Signals{SourcesExist: true, BrandCited: true, CompetitorMentioned: true}, Verdict{Displacement: true}},
		{"absent, competitor named, no sources", Signals{CompetitorMentioned: true}, Verdict{Visibility: true, Displacement: true}},
		{"judge noise ignored when absent", Signals{FramingNegative: true}, Verdict{}},
	}
	for _, tt := range tests {
		if got := Classify(tt.in); got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestMentionBand(t *testing.T) {
	tests := []struct {
		mentions, successful int
		want                 Band
	}{
		{2, 2, Insufficient},
		{0, 5, Blindspot},
		{1, 5, Blindspot},
		{2, 5, Weak},
		{3, 3, Weak},
		{4, 5, Visible},
		{5, 5, Visible},
	}
	for _, tt := range tests {
		if got := MentionBand(tt.mentions, tt.successful); got != tt.want {
			t.Errorf("MentionBand(%d, %d) = %s, want %s", tt.mentions, tt.successful, got, tt.want)
		}
	}
}
