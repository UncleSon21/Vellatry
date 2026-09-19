package reports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Series is a stored report series.
type Series struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Period       string    `json:"period"`
	Sections     []string  `json:"sections"`
	Recipients   []string  `json:"recipients"`
	AutoDraft    bool      `json:"auto_draft"`
	DraftLagDays int       `json:"draft_lag_days"`
	Accent       string    `json:"accent"`
	Archived     bool      `json:"archived"`
	CreatedAt    time.Time `json:"created_at"`
}

const seriesCols = `id::text, name, period, sections, recipients, auto_draft, draft_lag_days, accent, archived, created_at`

func scanSeries(r pgx.Row) (Series, error) {
	var s Series
	err := r.Scan(&s.ID, &s.Name, &s.Period, &s.Sections, &s.Recipients, &s.AutoDraft, &s.DraftLagDays, &s.Accent, &s.Archived, &s.CreatedAt)
	return s, err
}

// LoadSeries reads one series.
func LoadSeries(ctx context.Context, tx pgx.Tx, id string) (Series, error) {
	return scanSeries(tx.QueryRow(ctx, `SELECT `+seriesCols+` FROM report_series WHERE id::text = $1`, id))
}

// ListSeries lists the organisation's series, newest first.
func ListSeries(ctx context.Context, tx pgx.Tx) ([]Series, error) {
	rows, err := tx.Query(ctx, `SELECT `+seriesCols+` FROM report_series ORDER BY archived, created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Series, error) { return scanSeries(r) })
}

// Report is a stored report with its current (draft or published) content.
type Report struct {
	ID           string            `json:"id"`
	SeriesID     string            `json:"series_id"`
	SeriesName   string            `json:"series_name"`
	PeriodStart  string            `json:"period_start"`
	PeriodEnd    string            `json:"period_end"`
	Status       string            `json:"status"`
	Title        string            `json:"title"`
	Snapshot     Snapshot          `json:"snapshot"`
	Summary      string            `json:"summary"`
	SummaryDraft *string           `json:"summary_draft"`
	Notes        map[string]string `json:"notes"`
	Version      int               `json:"version"`
	Accent       string            `json:"accent"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	PublishedAt  *time.Time        `json:"published_at"`
}

// ErrNotFound is returned when a report or version does not exist.
var ErrNotFound = errors.New("reports: not found")

// LoadReport reads one report.
func LoadReport(ctx context.Context, tx pgx.Tx, id string) (Report, error) {
	var r Report
	var snap, notes []byte
	err := tx.QueryRow(ctx, `
		SELECT r.id::text, r.series_id::text, s.name, r.period_start::text, r.period_end::text, r.status, r.title, r.snapshot,
		       r.summary, r.summary_draft, r.notes, r.version, s.accent, r.created_at, r.updated_at, r.published_at
		FROM reports r JOIN report_series s ON s.id = r.series_id WHERE r.id::text = $1`, id).
		Scan(&r.ID, &r.SeriesID, &r.SeriesName, &r.PeriodStart, &r.PeriodEnd, &r.Status, &r.Title, &snap,
			&r.Summary, &r.SummaryDraft, &notes, &r.Version, &r.Accent, &r.CreatedAt, &r.UpdatedAt, &r.PublishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(snap, &r.Snapshot); err != nil {
		return r, err
	}
	r.Notes = map[string]string{}
	_ = json.Unmarshal(notes, &r.Notes)
	return r, nil
}

// ReportRow is a report in a list.
type ReportRow struct {
	ID          string     `json:"id"`
	SeriesID    string     `json:"series_id"`
	SeriesName  string     `json:"series_name"`
	Title       string     `json:"title"`
	Label       string     `json:"label"`
	PeriodStart string     `json:"period_start"`
	PeriodEnd   string     `json:"period_end"`
	Status      string     `json:"status"`
	Version     int        `json:"version"`
	UpdatedAt   time.Time  `json:"updated_at"`
	PublishedAt *time.Time `json:"published_at"`
}

// ListReports lists reports, newest period first. status "" lists every status.
func ListReports(ctx context.Context, tx pgx.Tx, seriesID, status string, limit int) ([]ReportRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.id::text, r.series_id::text, s.name, r.title, coalesce(r.snapshot->'period'->>'label', ''), r.period_start::text, r.period_end::text,
		       r.status, r.version, r.updated_at, r.published_at
		FROM reports r JOIN report_series s ON s.id = r.series_id
		WHERE ($1 = '' OR r.series_id::text = $1) AND ($2 = '' OR r.status = $2)
		ORDER BY r.period_end DESC, s.name LIMIT $3`, seriesID, status, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ReportRow, error) {
		var x ReportRow
		return x, r.Scan(&x.ID, &x.SeriesID, &x.SeriesName, &x.Title, &x.Label, &x.PeriodStart, &x.PeriodEnd, &x.Status, &x.Version, &x.UpdatedAt, &x.PublishedAt)
	})
}

// SaveDraft stores a freshly built draft. An existing draft for the same series and
// period is refreshed only when refresh is set (the team asked for new data); a
// published report is never touched.
func SaveDraft(ctx context.Context, tx pgx.Tx, org string, s Series, snap Snapshot, summaryDraft string, refresh bool) (string, bool, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return "", false, err
	}
	title := s.Name + ": " + snap.Period.Label
	var id string
	var created bool
	err = tx.QueryRow(ctx, `
		INSERT INTO reports (org_id, series_id, period_start, period_end, title, snapshot, summary_draft)
		VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''))
		ON CONFLICT (series_id, period_start, period_end) DO UPDATE
		  SET snapshot = EXCLUDED.snapshot, summary_draft = coalesce(EXCLUDED.summary_draft, reports.summary_draft), updated_at = now()
		  WHERE reports.status = 'draft' AND $8
		RETURNING id::text, (xmax = 0)`,
		org, s.ID, snap.Period.Start, snap.Period.End, title, raw, summaryDraft, refresh).Scan(&id, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil // exists and was left alone
	}
	return id, created, err
}

// Publish freezes the report's current content as a new version.
func Publish(ctx context.Context, tx pgx.Tx, org, id, by string) (int, error) {
	r, err := LoadReport(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM reports WHERE id::text = $1 FOR UPDATE`, id); err != nil {
		return 0, err
	}
	snap, _ := json.Marshal(r.Snapshot)
	notes, _ := json.Marshal(r.Notes)
	version := r.Version + 1
	if _, err := tx.Exec(ctx, `
		INSERT INTO report_versions (report_id, org_id, version, title, snapshot, summary, notes, accent, published_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, org, version, r.Title, snap, r.Summary, notes, r.Accent, by); err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `UPDATE reports SET status = 'published', version = $2, published_at = now(), updated_at = now() WHERE id::text = $1`, id, version)
	return version, err
}

// Version is one published version.
type Version struct {
	ReportID    string            `json:"report_id"`
	SeriesName  string            `json:"series_name"`
	Version     int               `json:"version"`
	Latest      int               `json:"latest"`
	Title       string            `json:"title"`
	Snapshot    Snapshot          `json:"snapshot"`
	Summary     string            `json:"summary"`
	Notes       map[string]string `json:"notes"`
	Accent      string            `json:"accent"`
	PublishedAt time.Time         `json:"published_at"`
	PDFStatus   string            `json:"pdf_status"`
}

// LoadVersion reads a published version; version 0 means the latest. A withdrawn
// report has no readable version.
func LoadVersion(ctx context.Context, tx pgx.Tx, reportID string, version int) (Version, error) {
	var v Version
	var snap, notes []byte
	err := tx.QueryRow(ctx, `
		SELECT v.report_id::text, s.name, v.version, r.version, v.title, v.snapshot, v.summary, v.notes, v.accent, v.published_at, v.pdf_status
		FROM report_versions v JOIN reports r ON r.id = v.report_id JOIN report_series s ON s.id = r.series_id
		WHERE v.report_id::text = $1 AND r.status = 'published' AND v.version = CASE WHEN $2 = 0 THEN r.version ELSE $2 END`,
		reportID, version).Scan(&v.ReportID, &v.SeriesName, &v.Version, &v.Latest, &v.Title, &snap, &v.Summary, &notes, &v.Accent, &v.PublishedAt, &v.PDFStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(snap, &v.Snapshot); err != nil {
		return v, err
	}
	v.Notes = map[string]string{}
	_ = json.Unmarshal(notes, &v.Notes)
	return v, nil
}

// VersionPDF returns a version's PDF, or ErrNotFound when it has not been rendered.
func VersionPDF(ctx context.Context, tx pgx.Tx, reportID string, version int) ([]byte, error) {
	var pdf []byte
	err := tx.QueryRow(ctx, `
		SELECT v.pdf FROM report_versions v JOIN reports r ON r.id = v.report_id
		WHERE v.report_id::text = $1 AND v.version = $2 AND r.status = 'published' AND v.pdf_status = 'ready'`, reportID, version).Scan(&pdf)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && len(pdf) == 0) {
		return nil, ErrNotFound
	}
	return pdf, err
}

// HubEntry is a published report as the hub lists it.
type HubEntry struct {
	ID          string    `json:"id"`
	SeriesName  string    `json:"series_name"`
	Title       string    `json:"title"`
	Label       string    `json:"label"`
	Version     int       `json:"version"`
	PublishedAt time.Time `json:"published_at"`
	PDFReady    bool      `json:"pdf_ready"`
}

// PublishedReports lists what the hub shows: the latest version of every published
// report, newest period first.
func PublishedReports(ctx context.Context, tx pgx.Tx) ([]HubEntry, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.id::text, s.name, v.title, coalesce(v.snapshot->'period'->>'label', ''), v.version, v.published_at, v.pdf_status = 'ready'
		FROM reports r
		JOIN report_series s ON s.id = r.series_id
		JOIN report_versions v ON v.report_id = r.id AND v.version = r.version
		WHERE r.status = 'published'
		ORDER BY r.period_end DESC, s.name LIMIT 200`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (HubEntry, error) {
		var e HubEntry
		return e, r.Scan(&e.ID, &e.SeriesName, &e.Title, &e.Label, &e.Version, &e.PublishedAt, &e.PDFReady)
	})
}

// LogView records a view.
func LogView(ctx context.Context, tx pgx.Tx, org, reportID string, version int, viewer, format string) error {
	_, err := tx.Exec(ctx, `INSERT INTO report_views (org_id, report_id, version, viewer, format) VALUES ($1, $2, $3, $4, $5)`,
		org, reportID, version, viewer, format)
	return err
}
