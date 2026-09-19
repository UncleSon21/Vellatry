package reports

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/automation"
)

// Hub access: a sign-in link, emailed only to an address at an allowed domain (or a
// team member), opens a session cookie for 30 days. A forwarded hub URL shows nothing
// without it.
const (
	LoginTTL         = 20 * time.Minute
	SessionTTL       = 30 * 24 * time.Hour
	MaxLoginsPerHour = 5
)

var (
	// ErrNotAllowed means the address may not view this hub.
	ErrNotAllowed = errors.New("reports: that address cannot view these reports")
	// ErrTooMany means too many sign-in links were asked for recently.
	ErrTooMany = errors.New("reports: too many sign-in links requested")
	// ErrInvalidLink means a sign-in link is unknown, used or expired.
	ErrInvalidLink = errors.New("reports: the sign-in link has expired or was already used")
)

// Hub is an organisation's reports hub.
type Hub struct {
	Slug           string    `json:"slug"`
	AllowedDomains []string  `json:"allowed_domains"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken is how sign-in and session tokens are stored.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewSlug returns an unguessable hub slug.
func NewSlug() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

// EnsureHub returns the organisation's hub, creating it on first use.
func EnsureHub(ctx context.Context, tx pgx.Tx, org string) (Hub, error) {
	h, err := LoadHub(ctx, tx)
	if !errors.Is(err, ErrNotFound) {
		return h, err
	}
	slug, err := NewSlug()
	if err != nil {
		return h, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hubs (org_id, slug) VALUES ($1, $2) ON CONFLICT (org_id) DO NOTHING`, org, slug); err != nil {
		return h, err
	}
	return LoadHub(ctx, tx)
}

// LoadHub reads the organisation's hub.
func LoadHub(ctx context.Context, tx pgx.Tx) (Hub, error) {
	var h Hub
	err := tx.QueryRow(ctx, `SELECT slug, allowed_domains, enabled, created_at FROM hubs`).Scan(&h.Slug, &h.AllowedDomains, &h.Enabled, &h.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, ErrNotFound
	}
	return h, err
}

// ViewerDomains returns the domains whose addresses may view the hub (the brand's own
// plus any the team added) and the team members' addresses.
func ViewerDomains(ctx context.Context, tx pgx.Tx) (domains, members []string, err error) {
	rows, err := tx.Query(ctx, `SELECT domain FROM brands UNION SELECT unnest(allowed_domains) FROM hubs`)
	if err != nil {
		return nil, nil, err
	}
	if domains, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, nil, err
	}
	rows, err = tx.Query(ctx, `SELECT u.email FROM users u JOIN memberships m ON m.user_id = u.id`)
	if err != nil {
		return nil, nil, err
	}
	members, err = pgx.CollectRows(rows, pgx.RowTo[string])
	return domains, members, err
}

// MayView reports whether email may open this organisation's hub.
func MayView(ctx context.Context, tx pgx.Tx, email string) (bool, error) {
	domains, members, err := ViewerDomains(ctx, tx)
	if err != nil {
		return false, err
	}
	_, err = automation.ValidRecipients([]string{email}, domains, members)
	return err == nil, nil
}

// CreateLogin stores a sign-in link for email and returns its token, which the caller
// emails and does not keep.
func CreateLogin(ctx context.Context, tx pgx.Tx, org, email string, now time.Time) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	ok, err := MayView(ctx, tx, email)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotAllowed
	}
	var recent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM hub_logins WHERE email = $1 AND created_at > $2`, email, now.Add(-time.Hour)).Scan(&recent); err != nil {
		return "", err
	}
	if recent >= MaxLoginsPerHour {
		return "", ErrTooMany
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO hub_logins (token_hash, org_id, email, expires_at, created_at) VALUES ($1, $2, $3, $4, $5)`,
		HashToken(token), org, email, now.Add(LoginTTL), now)
	return token, err
}

// ConsumeLogin uses a sign-in link once and opens a session. It returns the session
// token (for the cookie) and the viewer's address.
func ConsumeLogin(ctx context.Context, tx pgx.Tx, org, token string, now time.Time) (string, string, error) {
	var email string
	err := tx.QueryRow(ctx, `UPDATE hub_logins SET used_at = $2 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2 RETURNING email`,
		HashToken(token), now).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrInvalidLink
	}
	if err != nil {
		return "", "", err
	}
	if ok, err := MayView(ctx, tx, email); err != nil || !ok {
		if err == nil {
			err = ErrNotAllowed
		}
		return "", "", err
	}
	session, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO hub_sessions (token_hash, org_id, email, created_at, expires_at, last_seen_at) VALUES ($1, $2, $3, $4, $5, $4)`,
		HashToken(session), org, email, now, now.Add(SessionTTL))
	return session, email, err
}

// SessionEmail returns the viewer of a session, re-checking that they may still view
// the hub (a domain removed by the team ends its sessions).
func SessionEmail(ctx context.Context, tx pgx.Tx, session string, now time.Time) (string, error) {
	var email string
	var seen time.Time
	err := tx.QueryRow(ctx, `SELECT email, last_seen_at FROM hub_sessions WHERE token_hash = $1 AND expires_at > $2`, HashToken(session), now).Scan(&email, &seen)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidLink
	}
	if err != nil {
		return "", err
	}
	if ok, err := MayView(ctx, tx, email); err != nil || !ok {
		if err == nil {
			err = ErrNotAllowed
		}
		return "", err
	}
	if now.Sub(seen) > time.Hour {
		if _, err := tx.Exec(ctx, `UPDATE hub_sessions SET last_seen_at = $2 WHERE token_hash = $1`, HashToken(session), now); err != nil {
			return "", err
		}
	}
	return email, nil
}

// EndSession signs a viewer out.
func EndSession(ctx context.Context, tx pgx.Tx, session string) error {
	_, err := tx.Exec(ctx, `DELETE FROM hub_sessions WHERE token_hash = $1`, HashToken(session))
	return err
}
