package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/platform/db"
)

// OrgRef is one org the user belongs to.
type OrgRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// Session is the signed-in user and the org this request acts on.
type Session struct {
	UserID string   `json:"user_id"`
	Email  string   `json:"email"`
	OrgID  string   `json:"org_id,omitempty"`
	Role   string   `json:"role,omitempty"`
	Orgs   []OrgRef `json:"orgs"`
}

type sessionKey struct{}

func sessionFrom(ctx context.Context) Session {
	s, _ := ctx.Value(sessionKey{}).(Session)
	return s
}

// resolveSession finds or creates the user and picks the org: the X-Org-ID header if
// the user belongs to it, otherwise their first org. Cross-tenant by nature (a user is
// looked up before any org is known), so it runs as a system transaction.
func (s *Server) resolveSession(ctx context.Context, id Identity, requestedOrg string) (Session, error) {
	sess := Session{Email: id.Email, Orgs: []OrgRef{}}
	err := db.InSystem(ctx, s.Pool, func(ctx context.Context, tx pgx.Tx) error {
		email := id.Email
		if email == "" {
			email = id.ExternalID + "@users.invalid" // providers without an email claim
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO users (external_id, email) VALUES ($1, $2)
			ON CONFLICT (external_id) DO UPDATE SET email = EXCLUDED.email
			RETURNING id::text`, id.ExternalID, email).Scan(&sess.UserID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT o.id::text, o.name, m.role FROM memberships m JOIN orgs o ON o.id = m.org_id
			WHERE m.user_id = $1 ORDER BY o.created_at`, sess.UserID)
		if err != nil {
			return err
		}
		sess.Orgs, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (OrgRef, error) {
			var o OrgRef
			return o, r.Scan(&o.ID, &o.Name, &o.Role)
		})
		return err
	})
	if err != nil {
		return sess, err
	}
	for _, o := range sess.Orgs {
		if requestedOrg == "" || o.ID == requestedOrg {
			sess.OrgID, sess.Role = o.ID, o.Role
			break
		}
	}
	if requestedOrg != "" && sess.OrgID == "" {
		return sess, forbidden("You are not a member of that organisation.")
	}
	return sess, nil
}

// tenant runs fn scoped to the session's org.
func (s *Server) tenant(r *http.Request, fn func(ctx context.Context, tx pgx.Tx) error) error {
	sess := sessionFrom(r.Context())
	if sess.OrgID == "" {
		return forbidden("Finish setting up your organisation first.")
	}
	return db.InTenant(r.Context(), s.Pool, sess.OrgID, fn)
}

// canEdit reports whether the session's role may change data. Viewers (for example a
// CMO) read only.
func canEdit(r *http.Request) error {
	switch sessionFrom(r.Context()).Role {
	case "owner", "editor":
		return nil
	}
	return forbidden("Your role can view but not change this.")
}

var errNoRows = pgx.ErrNoRows

func isNoRows(err error) bool { return errors.Is(err, errNoRows) }
