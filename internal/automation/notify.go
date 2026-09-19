package automation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxImmediatePerHour caps immediate alerts per organisation. Beyond it, new alerts go
// to the digest instead, so a bad crawl or a flapping connection cannot flood a channel.
const MaxImmediatePerHour = 10

// Notification is something worth telling the team.
type Notification struct {
	WatcherID      string
	Kind           string
	DedupeKey      string // the same key merges into one notification
	Severity       string // critical | warning | info
	Title          string
	Body           string
	Link           string // dashboard path
	Data           any
	Delivery       string // immediate | digest
	DestinationIDs []string
}

// Recorded is the outcome of Record.
type Recorded struct {
	ID       int64
	New      bool   // false when it merged into an earlier notification with the same key
	Delivery string // may be digest even if immediate was asked for (throttled)
}

// Record stores n, or merges it into the notification with the same dedupe key (a
// repeat counts an occurrence and sends nothing). Immediate delivery is downgraded to
// the digest once the hourly cap is reached.
func Record(ctx context.Context, tx pgx.Tx, org string, n Notification) (Recorded, error) {
	if n.Severity == "" {
		n.Severity = "info"
	}
	if n.Delivery == "" {
		n.Delivery = "immediate"
	}
	if n.DestinationIDs == nil {
		n.DestinationIDs = []string{}
	}
	if n.Delivery == "immediate" && n.Kind != "digest" {
		var recent int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE delivery = 'immediate' AND kind <> 'digest'
			AND created_at > now() - interval '1 hour'`).Scan(&recent); err != nil {
			return Recorded{}, err
		}
		if recent >= MaxImmediatePerHour {
			n.Delivery = "digest"
		}
	}
	data, err := json.Marshal(n.Data)
	if err != nil {
		return Recorded{}, err
	}
	if n.Data == nil {
		data = []byte("{}")
	}
	var r Recorded
	err = tx.QueryRow(ctx, `
		INSERT INTO notifications (org_id, watcher_id, kind, dedupe_key, severity, title, body, link, data, delivery, destination_ids)
		VALUES ($1, nullif($2, '')::uuid, $3, $4, $5, $6, $7, nullif($8, ''), $9, $10, $11::uuid[])
		ON CONFLICT (org_id, dedupe_key) DO UPDATE SET occurrences = notifications.occurrences + 1, last_seen_at = now()
		RETURNING id, (xmax = 0), delivery`,
		org, n.WatcherID, n.Kind, n.DedupeKey, n.Severity, n.Title, n.Body, n.Link, data, n.Delivery, n.DestinationIDs).
		Scan(&r.ID, &r.New, &r.Delivery)
	return r, err
}

// Stored is a notification as the renderers and the dashboard see it.
type Stored struct {
	ID             int64           `json:"id"`
	Kind           string          `json:"kind"`
	Severity       string          `json:"severity"`
	Title          string          `json:"title"`
	Body           string          `json:"body"`
	Link           *string         `json:"link"`
	Data           json.RawMessage `json:"data"`
	Delivery       string          `json:"delivery"`
	DestinationIDs []string        `json:"-"`
	Occurrences    int             `json:"occurrences"`
	Status         string          `json:"status"`
	CreatedAt      time.Time       `json:"created_at"`
	LastSeenAt     time.Time       `json:"last_seen_at"`
	SentAt         *time.Time      `json:"sent_at"`
}

const storedCols = `id, kind, severity, title, body, link, data, delivery, destination_ids::text[], occurrences, status, created_at, last_seen_at, sent_at`

func scanStored(r pgx.Row) (Stored, error) {
	var s Stored
	err := r.Scan(&s.ID, &s.Kind, &s.Severity, &s.Title, &s.Body, &s.Link, &s.Data, &s.Delivery, &s.DestinationIDs,
		&s.Occurrences, &s.Status, &s.CreatedAt, &s.LastSeenAt, &s.SentAt)
	return s, err
}

// LoadNotification reads one notification, locking it for delivery.
func LoadNotification(ctx context.Context, tx pgx.Tx, id int64) (Stored, error) {
	return scanStored(tx.QueryRow(ctx, `SELECT `+storedCols+` FROM notifications WHERE id = $1 FOR UPDATE`, id))
}

// LoadNotifications lists the newest notifications for the dashboard.
func LoadNotifications(ctx context.Context, tx pgx.Tx, limit int) ([]Stored, error) {
	rows, err := tx.Query(ctx, `SELECT `+storedCols+` FROM notifications WHERE kind <> 'digest' ORDER BY last_seen_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Stored, error) { return scanStored(r) })
}

// PendingDigest returns the notifications waiting for the digest, most severe first.
func PendingDigest(ctx context.Context, tx pgx.Tx, limit int) ([]Stored, error) {
	rows, err := tx.Query(ctx, `SELECT `+storedCols+` FROM notifications WHERE delivery = 'digest' AND status = 'pending'
		ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Stored, error) { return scanStored(r) })
}
