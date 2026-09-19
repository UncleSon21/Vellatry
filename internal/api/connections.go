package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/googleauth"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
)

type connection struct {
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	StatusDetail *string         `json:"status_detail"`
	Config       json.RawMessage `json:"config"`
	UpdatedAt    time.Time       `json:"updated_at"`
	LastSyncedAt *time.Time      `json:"last_synced_at,omitempty"`
	LastDay      *string         `json:"last_day,omitempty"`
	LastError    *string         `json:"last_error,omitempty"`
}

// listConnections shows every connection openly, broken ones included, with what the
// customer can do about it. Secrets never leave the database.
func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	var out []connection
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.kind, c.status, c.status_detail, c.config, c.updated_at, st.last_synced_at, st.last_day::text, st.last_error
			FROM connections c LEFT JOIN sync_state st ON st.org_id = c.org_id AND st.kind = c.kind
			ORDER BY c.kind`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (connection, error) {
			var c connection
			return c, r.Scan(&c.Kind, &c.Status, &c.StatusDetail, &c.Config, &c.UpdatedAt, &c.LastSyncedAt, &c.LastDay, &c.LastError)
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": nonNilT(out), "google_available": s.GoogleOAuth != nil})
}

type oauthState struct {
	Org  string `json:"org"`
	User string `json:"user"`
}

// startGoogle returns the consent URL. Nothing here calls Google.
func (s *Server) startGoogle(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	if s.GoogleOAuth == nil || s.Box == nil {
		s.fail(w, r, badRequest("Connecting Google isn't configured on this server."))
		return
	}
	sess := sessionFrom(r.Context())
	if sess.OrgID == "" {
		s.fail(w, r, forbidden("Finish setting up your organisation first."))
		return
	}
	state, err := s.Box.Sign(oauthState{Org: sess.OrgID, User: sess.UserID}, 15*time.Minute, time.Now())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": googleauth.ConsentURL(s.GoogleOAuth, state)})
}

// googleCallback is where Google sends the browser back. The signed state identifies
// the org and user. The one-time code is sealed and stored; the worker exchanges it
// (the api never waits on Google) and the dashboard hears back over the event stream.
func (s *Server) googleCallback(w http.ResponseWriter, r *http.Request) {
	back := func(result string) {
		http.Redirect(w, r, s.AppURL+"/settings/connections?google="+url.QueryEscape(result), http.StatusFound)
	}
	if s.GoogleOAuth == nil || s.Box == nil {
		back("unavailable")
		return
	}
	var st oauthState
	if err := s.Box.Verify(r.URL.Query().Get("state"), &st, time.Now()); err != nil {
		back("expired")
		return
	}
	if r.URL.Query().Get("error") != "" {
		back("cancelled")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		back("cancelled")
		return
	}
	sealed, err := s.Box.Seal(st.Org, []byte(code))
	if err != nil {
		s.Logger.ErrorContext(r.Context(), "seal google code", "error", err)
		back("error")
		return
	}
	err = db.InTenant(r.Context(), s.Pool, st.Org, func(ctx context.Context, tx pgx.Tx) error {
		var member bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE user_id = $1 AND role IN ('owner', 'editor'))`, st.User).Scan(&member); err != nil {
			return err
		}
		if !member {
			return forbidden("not an editor")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO connections (org_id, kind, status, secret, config) VALUES ($1, 'google', 'pending', $2, '{}')
			ON CONFLICT (org_id, kind) DO UPDATE SET status = 'pending', secret = EXCLUDED.secret, config = '{}',
			    status_detail = NULL, updated_at = now()`, st.Org, sealed); err != nil {
			return err
		}
		_, err := s.Bus.Emit(ctx, tx, st.Org, events.Event{Kind: domainevents.ConnectionAuthorized, Actor: st.User,
			Payload: domainevents.ConnectionPayload{Kind: "google"}})
		return err
	})
	if err != nil {
		s.Logger.ErrorContext(r.Context(), "store google authorisation", "error", err)
		back("error")
		return
	}
	back("authorized")
}

// chooseProperty connects Search Console or GA4 to one of the properties Google listed
// for this grant.
func (s *Server) chooseProperty(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	kind := r.PathValue("kind")
	if kind != "search_console" && kind != "ga4" {
		s.fail(w, r, notFound("Unknown connection."))
		return
	}
	var in struct {
		Property string `json:"property"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		var cfg []byte
		err := tx.QueryRow(ctx, `SELECT status, config FROM connections WHERE kind = 'google'`).Scan(&status, &cfg)
		if isNoRows(err) || (err == nil && status != "connected") {
			return badRequest("Connect Google first.")
		}
		if err != nil {
			return err
		}
		var listed struct {
			Sites []struct {
				URL string `json:"siteUrl"`
			} `json:"sites"`
			Properties []struct {
				ID string `json:"id"`
			} `json:"properties"`
		}
		_ = json.Unmarshal(cfg, &listed)
		ok := false
		for _, site := range listed.Sites {
			ok = ok || (kind == "search_console" && site.URL == in.Property)
		}
		for _, p := range listed.Properties {
			ok = ok || (kind == "ga4" && p.ID == in.Property)
		}
		if !ok {
			return badRequest("That property isn't available on your Google account.")
		}
		cfgOut, _ := json.Marshal(map[string]string{"property": in.Property})
		if _, err := tx.Exec(ctx, `
			INSERT INTO connections (org_id, kind, status, config) VALUES ($1, $2, 'connected', $3)
			ON CONFLICT (org_id, kind) DO UPDATE SET status = 'connected', config = EXCLUDED.config, status_detail = NULL, updated_at = now()`,
			sessionFrom(ctx).OrgID, kind, cfgOut); err != nil {
			return err
		}
		_, err = s.Bus.Emit(ctx, tx, sessionFrom(ctx).OrgID, events.Event{Kind: domainevents.ConnectionConnected, Actor: sessionFrom(ctx).UserID,
			Payload: domainevents.ConnectionPayload{Kind: kind}})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// disconnectGoogle stops syncing; the worker revokes the grant at Google and wipes the
// token. Synced history is kept so reconnecting does not start from zero.
func (s *Server) disconnectGoogle(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE connections SET status = 'revoked', updated_at = now() WHERE kind IN ('google', 'search_console', 'ga4') AND status <> 'revoked'`)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = s.Bus.Emit(ctx, tx, sessionFrom(ctx).OrgID, events.Event{Kind: domainevents.ConnectionRevoked, Actor: sessionFrom(ctx).UserID,
			Payload: domainevents.ConnectionPayload{Kind: "google"}})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
