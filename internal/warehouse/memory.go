package warehouse

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/UncleSon21/vellatry/internal/google"
)

// Memory is an in-process Warehouse for tests and local development without Google
// Cloud. It keeps the same replace-a-day semantics as BigQuery.
type Memory struct {
	mu        sync.Mutex
	search    map[string]map[string][]google.SearchRow    // org -> day -> rows
	analytics map[string]map[string][]google.AnalyticsRow // org -> day -> rows
}

func NewMemory() *Memory {
	return &Memory{search: map[string]map[string][]google.SearchRow{}, analytics: map[string]map[string][]google.AnalyticsRow{}}
}

func (m *Memory) ReplaceSearchDay(_ context.Context, org string, day time.Time, rows []google.SearchRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.search[org] == nil {
		m.search[org] = map[string][]google.SearchRow{}
	}
	m.search[org][day.Format(time.DateOnly)] = append([]google.SearchRow(nil), rows...)
	return nil
}

func (m *Memory) ReplaceAnalyticsDay(_ context.Context, org string, day time.Time, rows []google.AnalyticsRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.analytics[org] == nil {
		m.analytics[org] = map[string][]google.AnalyticsRow{}
	}
	m.analytics[org][day.Format(time.DateOnly)] = append([]google.AnalyticsRow(nil), rows...)
	return nil
}

func inRange(day string, from, to time.Time) bool {
	return day >= from.Format(time.DateOnly) && day <= to.Format(time.DateOnly)
}

func (m *Memory) SearchTop(_ context.Context, org string, from, to time.Time, limit int) ([]Rollup, []Rollup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q, p := map[string]*Rollup{}, map[string]*Rollup{}
	for day, rows := range m.search[org] {
		if !inRange(day, from, to) {
			continue
		}
		for _, r := range rows {
			for key, agg := range map[string]map[string]*Rollup{r.Query: q, r.Page: p} {
				x := agg[key]
				if x == nil {
					x = &Rollup{Key: key}
					agg[key] = x
				}
				x.Clicks += r.Clicks
				x.Impressions += r.Impressions
				x.PositionSum += r.Position * float64(r.Impressions)
			}
		}
	}
	return top(q, limit), top(p, limit), nil
}

func (m *Memory) QueryPages(_ context.Context, org string, queries []string, from, to time.Time) ([]QueryPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]bool{}
	for _, q := range queries {
		want[q] = true
	}
	totals := map[[2]string]*QueryPage{}
	for day, rows := range m.search[org] {
		if !inRange(day, from, to) {
			continue
		}
		for _, r := range rows {
			if !want[r.Query] || r.Page == "" {
				continue
			}
			key := [2]string{r.Query, r.Page}
			t := totals[key]
			if t == nil {
				t = &QueryPage{Query: r.Query, Page: r.Page}
				totals[key] = t
			}
			t.Clicks += r.Clicks
			t.Impressions += r.Impressions
		}
	}
	out := make([]QueryPage, 0, len(totals))
	for _, t := range totals {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Query != out[j].Query {
			return out[i].Query < out[j].Query
		}
		return out[i].Impressions > out[j].Impressions
	})
	return out, nil
}

func (m *Memory) SearchDetailImpressions(_ context.Context, org string, from, to time.Time) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int64{}
	for day, rows := range m.search[org] {
		if !inRange(day, from, to) {
			continue
		}
		for _, r := range rows {
			out[day] += r.Impressions
		}
	}
	return out, nil
}

func (m *Memory) LandingTop(_ context.Context, org string, from, to time.Time, limit int) ([]Rollup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg := map[string]*Rollup{}
	for day, rows := range m.analytics[org] {
		if !inRange(day, from, to) {
			continue
		}
		for _, r := range rows {
			x := agg[r.Landing]
			if x == nil {
				x = &Rollup{Key: r.Landing}
				agg[r.Landing] = x
			}
			x.Sessions += r.Sessions
			x.KeyEvents += r.KeyEvents
		}
	}
	out := make([]Rollup, 0, len(agg))
	for _, r := range agg {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) DeleteTenant(_ context.Context, org string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.search, org)
	delete(m.analytics, org)
	return nil
}

func top(agg map[string]*Rollup, limit int) []Rollup {
	out := make([]Rollup, 0, len(agg))
	for _, r := range agg {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Impressions != out[j].Impressions {
			return out[i].Impressions > out[j].Impressions
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
