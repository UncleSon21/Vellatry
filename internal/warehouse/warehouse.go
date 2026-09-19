// Package warehouse stores raw Search Console and GA4 facts outside Postgres.
//
// The previous system kept date x query x page x country x device rows in Postgres,
// upserted in small batches against a wide unique index, aggregated on every read, and
// deleted rows for retention, until the database ran out of disk IO. Here raw facts
// live in BigQuery, one table per tenant and fact type, partitioned by day:
//
//   - a sync replaces whole day partitions in one load job (no upserts, no index),
//   - retention is partition expiry (no DELETE),
//   - rollups are computed here once a night and only small results reach Postgres,
//   - a tenant's data is deleted by dropping its tables.
package warehouse

import (
	"context"
	"time"

	"github.com/UncleSon21/vellatry/internal/google"
)

// Rollup is one aggregated key (a query, page or landing page) over a period.
type Rollup struct {
	Key         string
	Clicks      int64
	Impressions int64
	PositionSum float64 // sum(position x impressions)
	Sessions    int64
	KeyEvents   float64
}

// Warehouse is the raw-fact store.
type Warehouse interface {
	// ReplaceSearchDay replaces one day of a tenant's Search Console rows.
	ReplaceSearchDay(ctx context.Context, orgID string, day time.Time, rows []google.SearchRow) error
	// ReplaceAnalyticsDay replaces one day of a tenant's GA4 rows.
	ReplaceAnalyticsDay(ctx context.Context, orgID string, day time.Time, rows []google.AnalyticsRow) error
	// SearchTop returns the top queries and pages by impressions for [from, to].
	SearchTop(ctx context.Context, orgID string, from, to time.Time, limit int) (queries, pages []Rollup, err error)
	// SearchDetailImpressions returns detail-row impressions per day, for reconciliation.
	SearchDetailImpressions(ctx context.Context, orgID string, from, to time.Time) (map[string]int64, error)
	// LandingTop returns the top landing pages by sessions for [from, to].
	LandingTop(ctx context.Context, orgID string, from, to time.Time, limit int) ([]Rollup, error)
	// DeleteTenant removes all of a tenant's raw facts.
	DeleteTenant(ctx context.Context, orgID string) error
}

// MonthBounds returns the first and last day of t's month.
func MonthBounds(t time.Time) (time.Time, time.Time) {
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return first, first.AddDate(0, 1, -1)
}
