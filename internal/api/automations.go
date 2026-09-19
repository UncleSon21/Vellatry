package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/automation"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/events"
)

func automationErr(err error) error {
	if errors.Is(err, automation.ErrInvalid) {
		return badRequest(automation.Message(err))
	}
	return err
}

// ---- destinations --------------------------------------------------------------------

type destinationOut struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	Config       json.RawMessage `json:"config"`
	Digest       bool            `json:"digest"`
	Status       string          `json:"status"`
	StatusDetail *string         `json:"status_detail"`
	CreatedAt    time.Time       `json:"created_at"`
}

// listDestinations never returns a webhook URL: it is sealed and only the worker opens it.
func (s *Server) listDestinations(w http.ResponseWriter, r *http.Request) {
	var out []destinationOut
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, kind, name, config, digest, status, status_detail, created_at FROM destinations ORDER BY created_at`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (destinationOut, error) {
			var d destinationOut
			return d, r.Scan(&d.ID, &d.Kind, &d.Name, &d.Config, &d.Digest, &d.Status, &d.StatusDetail, &d.CreatedAt)
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"destinations": nonNilT(out), "slack_available": s.Box != nil})
}

type destinationIn struct {
	Kind       string    `json:"kind"`
	Name       *string   `json:"name"`
	WebhookURL *string   `json:"webhook_url"` // slack
	To         *[]string `json:"to"`          // email
	Digest     *bool     `json:"digest"`
	Status     *string   `json:"status"` // active | paused
}

// allowedRecipients returns the company domains and member emails an email destination
// may send to.
func allowedRecipients(ctx context.Context, tx pgx.Tx) ([]string, []string, error) {
	rows, err := tx.Query(ctx, `SELECT domain FROM brands`)
	if err != nil {
		return nil, nil, err
	}
	domains, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, err
	}
	rows, err = tx.Query(ctx, `SELECT u.email FROM users u JOIN memberships m ON m.user_id = u.id`)
	if err != nil {
		return nil, nil, err
	}
	members, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return domains, members, err
}

func (s *Server) addDestination(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in destinationIn
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	name := ""
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if name == "" || len(name) > 80 {
		s.fail(w, r, badRequest("Give the destination a name of up to 80 characters."))
		return
	}
	digest := in.Digest == nil || *in.Digest
	var id string
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		org := sessionFrom(ctx).OrgID
		var cfg, secret []byte
		switch in.Kind {
		case "slack":
			if s.Box == nil {
				return badRequest("Slack destinations aren't configured on this server.")
			}
			if in.WebhookURL == nil {
				return badRequest("Paste the channel's incoming webhook URL.")
			}
			if err := automation.ValidSlackWebhook(*in.WebhookURL); err != nil {
				return automationErr(err)
			}
			var err error
			if secret, err = s.Box.Seal(org, []byte(strings.TrimSpace(*in.WebhookURL))); err != nil {
				return err
			}
			cfg = []byte("{}")
		case "email":
			if in.To == nil {
				return badRequest("Add at least one recipient.")
			}
			domains, members, err := allowedRecipients(ctx, tx)
			if err != nil {
				return err
			}
			to, err := automation.ValidRecipients(*in.To, domains, members)
			if err != nil {
				return automationErr(err)
			}
			cfg, _ = json.Marshal(map[string][]string{"to": to})
		default:
			return badRequest("A destination is either slack or email.")
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM destinations`).Scan(&n); err != nil {
			return err
		}
		if n >= 20 {
			return badRequest("You can have up to 20 destinations.")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO destinations (org_id, kind, name, config, secret, digest) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`,
			org, in.Kind, name, cfg, secret, digest).Scan(&id); err != nil {
			return err
		}
		_, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.DestinationAdded, SubjectID: id, Actor: sessionFrom(ctx).UserID,
			Payload: map[string]string{"kind": in.Kind, "name": name}})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// patchDestination renames, pauses or resumes a destination, or replaces a Slack webhook
// (which also sends a new test message).
func (s *Server) patchDestination(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, r, notFound("Destination not found."))
		return
	}
	var in destinationIn
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		org := sessionFrom(ctx).OrgID
		var kind, status string
		err := tx.QueryRow(ctx, `SELECT kind, status FROM destinations WHERE id = $1 FOR UPDATE`, id).Scan(&kind, &status)
		if isNoRows(err) {
			return notFound("Destination not found.")
		}
		if err != nil {
			return err
		}
		retest := false
		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if name == "" || len(name) > 80 {
				return badRequest("Give the destination a name of up to 80 characters.")
			}
			if _, err := tx.Exec(ctx, `UPDATE destinations SET name = $2 WHERE id = $1`, id, name); err != nil {
				return err
			}
		}
		if in.Digest != nil {
			if _, err := tx.Exec(ctx, `UPDATE destinations SET digest = $2 WHERE id = $1`, id, *in.Digest); err != nil {
				return err
			}
		}
		if in.WebhookURL != nil {
			if kind != "slack" || s.Box == nil {
				return badRequest("Only a Slack destination has a webhook.")
			}
			if err := automation.ValidSlackWebhook(*in.WebhookURL); err != nil {
				return automationErr(err)
			}
			sealed, err := s.Box.Seal(org, []byte(strings.TrimSpace(*in.WebhookURL)))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE destinations SET secret = $2, status = 'active', status_detail = NULL WHERE id = $1`, id, sealed); err != nil {
				return err
			}
			retest = true
		}
		if in.To != nil {
			if kind != "email" {
				return badRequest("Only an email destination has recipients.")
			}
			domains, members, err := allowedRecipients(ctx, tx)
			if err != nil {
				return err
			}
			to, err := automation.ValidRecipients(*in.To, domains, members)
			if err != nil {
				return automationErr(err)
			}
			cfg, _ := json.Marshal(map[string][]string{"to": to})
			if _, err := tx.Exec(ctx, `UPDATE destinations SET config = $2, status = 'active', status_detail = NULL WHERE id = $1`, id, cfg); err != nil {
				return err
			}
			retest = true
		}
		if in.Status != nil {
			switch *in.Status {
			case "paused":
			case "active":
				retest = retest || status == "broken"
			default:
				return badRequest("Status must be active or paused.")
			}
			if _, err := tx.Exec(ctx, `UPDATE destinations SET status = $2, status_detail = NULL WHERE id = $1`, id, *in.Status); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE destinations SET updated_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		if !retest {
			return nil
		}
		_, err = s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.DestinationAdded, SubjectID: id, Actor: sessionFrom(ctx).UserID,
			Payload: map[string]string{"kind": kind, "reason": "updated"}})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteDestination(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, r, notFound("Destination not found."))
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM destinations WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return notFound("Destination not found.")
		}
		// Watchers pointing only at it fall back to every destination rather than nowhere.
		_, err = tx.Exec(ctx, `UPDATE watchers SET destination_ids = array_remove(destination_ids, $1::uuid) WHERE $1::uuid = ANY(destination_ids)`, id)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- watchers ------------------------------------------------------------------------

func (s *Server) listWatchers(w http.ResponseWriter, r *http.Request) {
	var out []automation.Watcher
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = automation.LoadWatchers(ctx, tx, "")
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kinds": automation.Kinds, "watchers": nonNilT(out)})
}

type watcherIn struct {
	Kind           string             `json:"kind"`
	Params         *automation.Params `json:"params"`
	Delivery       *string            `json:"delivery"`
	DestinationIDs *[]string          `json:"destination_ids"`
	Enabled        *bool              `json:"enabled"`
}

func engines(ctx context.Context, tx pgx.Tx) ([]string, error) {
	var out []string
	err := tx.QueryRow(ctx, `SELECT engines FROM org_settings`).Scan(&out)
	if isNoRows(err) {
		return []string{"chatgpt", "gemini", "ai_overview"}, nil
	}
	return out, err
}

// checkDestinations confirms every id is one of the organisation's destinations.
func checkDestinations(ctx context.Context, tx pgx.Tx, ids []string) error {
	for _, id := range ids {
		if !validUUID(id) {
			return badRequest("Unknown destination.")
		}
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM destinations WHERE id = ANY($1::uuid[])`, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return badRequest("Unknown destination.")
	}
	return nil
}

func (s *Server) saveWatcher(ctx context.Context, tx pgx.Tx, id string, in watcherIn) (string, error) {
	var current automation.Watcher
	if id != "" {
		ws, err := automation.LoadWatchers(ctx, tx, "")
		if err != nil {
			return "", err
		}
		found := false
		for _, w := range ws {
			if w.ID == id {
				current, found = w, true
			}
		}
		if !found {
			return "", notFound("Watcher not found.")
		}
	} else {
		current = automation.Watcher{Kind: in.Kind, Enabled: true}
	}
	if in.Params != nil {
		current.Params = *in.Params
	}
	if in.Delivery != nil {
		current.Delivery = *in.Delivery
	}
	if in.DestinationIDs != nil {
		current.DestinationIDs = *in.DestinationIDs
		if err := checkDestinations(ctx, tx, current.DestinationIDs); err != nil {
			return "", err
		}
	}
	if in.Enabled != nil {
		current.Enabled = *in.Enabled
	}
	eng, err := engines(ctx, tx)
	if err != nil {
		return "", err
	}
	wt, err := automation.Normalise(current, eng)
	if err != nil {
		return "", automationErr(err)
	}
	params, _ := json.Marshal(wt.Params)
	if id == "" {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM watchers`).Scan(&n); err != nil {
			return "", err
		}
		if n >= 50 {
			return "", badRequest("You can have up to 50 watchers.")
		}
		err = tx.QueryRow(ctx, `INSERT INTO watchers (org_id, kind, params, delivery, destination_ids, enabled, source, created_by)
			VALUES ($1, $2, $3, $4, $5::uuid[], $6, 'user', $7) RETURNING id::text`,
			sessionFrom(ctx).OrgID, wt.Kind, params, wt.Delivery, wt.DestinationIDs, wt.Enabled, sessionFrom(ctx).UserID).Scan(&id)
		return id, err
	}
	_, err = tx.Exec(ctx, `UPDATE watchers SET params = $2, delivery = $3, destination_ids = $4::uuid[], enabled = $5, updated_at = now() WHERE id = $1`,
		id, params, wt.Delivery, wt.DestinationIDs, wt.Enabled)
	return id, err
}

func (s *Server) addWatcher(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in watcherIn
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	var id string
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = s.saveWatcher(ctx, tx, "", in)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) patchWatcher(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	var in watcherIn
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if !validUUID(id) || in.Kind != "" {
		s.fail(w, r, badRequest("A watcher's kind can't be changed; add a new one instead."))
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		_, err := s.saveWatcher(ctx, tx, id, in)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteWatcher(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, r, notFound("Watcher not found."))
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM watchers WHERE id = $1`, id)
		if err == nil && tag.RowsAffected() == 0 {
			return notFound("Watcher not found.")
		}
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	var out []automation.Stored
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = automation.LoadNotifications(ctx, tx, limitParam(r, 50, 200))
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

// ---- tasks ---------------------------------------------------------------------------

// sendToAsana queues a task for a fix or blindspot. The customer asked for it (confirm
// first); the worker creates it and the dashboard hears back over the event stream.
func (s *Server) sendToAsana(source string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := canEdit(r); err != nil {
			s.fail(w, r, err)
			return
		}
		id := r.PathValue("id")
		err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
			var cfg []byte
			var status string
			err := tx.QueryRow(ctx, `SELECT status, config FROM connections WHERE kind = 'asana'`).Scan(&status, &cfg)
			if isNoRows(err) || (err == nil && status != "connected") {
				return badRequest("Connect Asana first.")
			}
			if err != nil {
				return err
			}
			var c struct {
				Project string `json:"project"`
			}
			_ = json.Unmarshal(cfg, &c)
			if c.Project == "" {
				return badRequest("Choose the Asana project new tasks go to.")
			}
			var exists bool
			table := map[string]string{"fix": "fixes", "blindspot": "blindspots"}[source]
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id::text = $1)`, id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return notFound("Not found.")
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO task_links (org_id, source, subject_id, requested_by) VALUES ($1, $2, $3, $4)
				ON CONFLICT (org_id, source, subject_id) DO UPDATE SET status = 'creating', error = NULL, requested_by = EXCLUDED.requested_by
				WHERE task_links.status IN ('failed', 'completed')`,
				sessionFrom(ctx).OrgID, source, id, sessionFrom(ctx).UserID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return conflict("This is already in Asana.")
			}
			_, err = s.Bus.Emit(ctx, tx, sessionFrom(ctx).OrgID, events.Event{Kind: domainevents.TaskRequested, SubjectID: source + ":" + id,
				Actor: sessionFrom(ctx).UserID, Payload: domainevents.TaskPayload{Source: source, SubjectID: id}})
			return err
		})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
	}
}
