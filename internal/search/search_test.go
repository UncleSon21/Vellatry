package search

import (
	"testing"
	"time"

	"github.com/UncleSon21/vellatry/internal/google"
)

func date(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

func TestAggregateAnalytics(t *testing.T) {
	channels, assistants := AggregateAnalytics([]google.AnalyticsRow{
		{Channel: "Organic Search", Source: "google", Sessions: 10, Users: 8, KeyEvents: 1},
		{Channel: "Organic Search", Source: "google", Sessions: 5, Users: 4},
		{Channel: "Referral", Source: "chatgpt.com", Sessions: 3, Users: 3, KeyEvents: 1},
		{Channel: "Referral", Source: "perplexity.ai", Sessions: 2, Users: 2},
		{Channel: "", Source: "chat.openai.com", Sessions: 1, Users: 1},
	})
	byName := func(list []ChannelDay) map[string]ChannelDay {
		m := map[string]ChannelDay{}
		for _, c := range list {
			m[c.Channel] = c
		}
		return m
	}
	ch, as := byName(channels), byName(assistants)
	if ch["Organic Search"].Sessions != 15 || ch["Referral"].Sessions != 5 || ch["(not set)"].Sessions != 1 {
		t.Errorf("channels = %+v", channels)
	}
	if as["ChatGPT"].Sessions != 4 || as["Perplexity"].Sessions != 2 || len(as) != 2 {
		t.Errorf("assistants = %+v", assistants)
	}
}

func TestChunksDaysMonths(t *testing.T) {
	c := Chunks(date("2026-01-01"), date("2026-01-17"), 7)
	if len(c) != 3 || c[2][0].Format(time.DateOnly) != "2026-01-15" || c[2][1].Format(time.DateOnly) != "2026-01-17" {
		t.Errorf("chunks = %v", c)
	}
	if n := len(Days(date("2026-02-27"), date("2026-03-02"))); n != 4 {
		t.Errorf("days = %d", n)
	}
	m := Months(date("2026-01-31"), date("2026-03-01"))
	if len(m) != 3 || m[0].Format(time.DateOnly) != "2026-01-01" || m[2].Format(time.DateOnly) != "2026-03-01" {
		t.Errorf("months = %v", m)
	}
}
