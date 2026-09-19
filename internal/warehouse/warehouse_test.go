package warehouse

import (
	"context"
	"testing"
	"time"

	"github.com/UncleSon21/vellatry/internal/google"
)

func day(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }

func TestMemoryReplacesWholeDaysPerTenant(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	rows := []google.SearchRow{
		{Date: "2026-09-01", Query: "mattress", Page: "/a", Impressions: 100, Clicks: 5, Position: 3},
		{Date: "2026-09-01", Query: "pillow", Page: "/b", Impressions: 40, Clicks: 1, Position: 8},
	}
	_ = m.ReplaceSearchDay(ctx, "org-a", day(1), rows)
	_ = m.ReplaceSearchDay(ctx, "org-b", day(1), []google.SearchRow{{Query: "other", Impressions: 999}})
	// A re-pull of the same day replaces it; it does not add to it.
	_ = m.ReplaceSearchDay(ctx, "org-a", day(1), rows[:1])

	q, p, err := m.SearchTop(ctx, "org-a", day(1), day(30), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 1 || q[0].Key != "mattress" || q[0].Impressions != 100 || q[0].PositionSum != 300 {
		t.Errorf("queries = %+v", q)
	}
	if len(p) != 1 || p[0].Key != "/a" {
		t.Errorf("pages = %+v", p)
	}
	imp, _ := m.SearchDetailImpressions(ctx, "org-a", day(1), day(1))
	if imp["2026-09-01"] != 100 {
		t.Errorf("detail impressions = %v", imp)
	}
	if err := m.DeleteTenant(ctx, "org-a"); err != nil {
		t.Fatal(err)
	}
	if q, _, _ := m.SearchTop(ctx, "org-a", day(1), day(30), 10); len(q) != 0 {
		t.Error("tenant data survived deletion")
	}
	if q, _, _ := m.SearchTop(ctx, "org-b", day(1), day(30), 10); len(q) != 1 {
		t.Error("deleting one tenant touched another")
	}
}

func TestTableName(t *testing.T) {
	if got := tableName("search", "3f2a9c1e-5b7d-4e8a-9c21-7d4e5f6a8b90"); got != "search_3f2a9c1e5b7d4e8a9c217d4e5f6a8b90" {
		t.Errorf("tableName = %s", got)
	}
}

func TestMonthBounds(t *testing.T) {
	first, last := MonthBounds(time.Date(2026, 2, 17, 13, 0, 0, 0, time.UTC))
	if first.Format(time.DateOnly) != "2026-02-01" || last.Format(time.DateOnly) != "2026-02-28" {
		t.Errorf("bounds = %s %s", first, last)
	}
}
