package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"golang.org/x/oauth2"

	"github.com/UncleSon21/vellatry/internal/asana"
	"github.com/UncleSon21/vellatry/internal/automation"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/secrets"
)

// AsanaAPI is what the Asana jobs need from Asana.
type AsanaAPI interface {
	Workspaces(ctx context.Context) ([]asana.Ref, error)
	Projects(ctx context.Context, workspace string) ([]asana.Ref, error)
	TaskByExternal(ctx context.Context, externalID string) (asana.Task, bool, error)
	CreateTask(ctx context.Context, t asana.NewTask) (asana.Task, error)
	SetCompleted(ctx context.Context, gid string, completed bool) error
	Comment(ctx context.Context, gid, text string) error
}

// Asana holds the Asana jobs' dependencies.
type Asana struct {
	Pool   *pgxpool.Pool
	Box    *secrets.Box
	OAuth  *oauth2.Config
	Bus    *events.Bus
	Logger *slog.Logger
	AppURL string

	// Replaceable in tests.
	NewClient func(ctx context.Context, refreshToken string) AsanaAPI
	Exchange  func(ctx context.Context, code string) (*oauth2.Token, error)
	Revoke    func(ctx context.Context, refreshToken string) error
}

// AsanaConfig is the stored state of the Asana connection (never the token).
type AsanaConfig struct {
	Token      bool        `json:"token"` // a refresh token (not a one-time code) is stored
	Listed     bool        `json:"listed"`
	Workspaces []asana.Ref `json:"workspaces"`
	Projects   []asana.Ref `json:"projects"`
	Project    string      `json:"project,omitempty"` // where new tasks go, chosen by the customer
}

// Register adds the Asana jobs.
func (s *Asana) Register(ws *river.Workers) {
	if s.NewClient == nil {
		s.NewClient = func(ctx context.Context, rt string) AsanaAPI { return asana.New(ctx, s.OAuth, rt) }
	}
	if s.Exchange == nil {
		s.Exchange = func(ctx context.Context, code string) (*oauth2.Token, error) { return s.OAuth.Exchange(ctx, code) }
	}
	if s.Revoke == nil {
		s.Revoke = func(ctx context.Context, rt string) error { return asana.Revoke(ctx, nil, s.OAuth, rt) }
	}
	river.AddWorker(ws, &asanaAuthorizeWorker{s: s})
	river.AddWorker(ws, &asanaRevokeWorker{s: s})
	river.AddWorker(ws, &asanaCreateTaskWorker{s: s})
	river.AddWorker(ws, &asanaCloseDoneWorker{s: s})
}

const asanaReconnect = "Asana no longer accepts Vellatry's access. Connect Asana again."

type asanaGrant struct {
	Status string
	Secret []byte
	Config AsanaConfig
}

func loadAsana(ctx context.Context, tx pgx.Tx) (asanaGrant, error) {
	var g asanaGrant
	var cfg []byte
	err := tx.QueryRow(ctx, `SELECT status, secret, config FROM connections WHERE kind = 'asana'`).Scan(&g.Status, &g.Secret, &cfg)
	_ = json.Unmarshal(cfg, &g.Config)
	return g, err
}

func (s *Asana) markBroken(ctx context.Context, org, detail string) error {
	return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE connections SET status = 'broken', status_detail = $1, updated_at = now()
			WHERE kind = 'asana' AND status NOT IN ('revoked', 'broken')`, detail)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.ConnectionBroken, Actor: "asana",
			Payload: domainevents.ConnectionPayload{Kind: "asana", Detail: detail}})
		return err
	})
}

// client opens the stored grant. It fails with asana.ErrAuth when there is none.
func (s *Asana) client(ctx context.Context, org string) (AsanaAPI, AsanaConfig, error) {
	var g asanaGrant
	var rt []byte
	err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if g, err = loadAsana(ctx, tx); err != nil {
			return err
		}
		if g.Status != "connected" {
			return fmt.Errorf("%w: the Asana connection is %s", asana.ErrAuth, g.Status)
		}
		rt, err = s.Box.Open(org, g.Secret)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, g.Config, fmt.Errorf("%w: Asana is not connected", asana.ErrAuth)
	}
	if err != nil {
		return nil, g.Config, err
	}
	return s.NewClient(ctx, string(rt)), g.Config, nil
}

type asanaAuthorizeWorker struct {
	river.WorkerDefaults[jobargs.AsanaAuthorize]
	s *Asana
}

func (w *asanaAuthorizeWorker) Work(ctx context.Context, job *river.Job[jobargs.AsanaAuthorize]) error {
	s, org := w.s, job.Args.OrgID
	var g asanaGrant
	if err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		g, err = loadAsana(ctx, tx)
		return err
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	switch g.Status {
	case "pending":
		code, err := s.Box.Open(org, g.Secret)
		if err != nil {
			return s.markBroken(ctx, org, "Asana sign-in could not be read. Connect Asana again.")
		}
		tok, err := s.Exchange(ctx, string(code))
		if err != nil || tok.RefreshToken == "" {
			s.Logger.WarnContext(ctx, "asana code exchange failed", "org_id", org, "error", err)
			return s.markBroken(ctx, org, "Asana sign-in expired or was cancelled. Connect Asana again.")
		}
		sealed, err := s.Box.Seal(org, []byte(tok.RefreshToken))
		if err != nil {
			return err
		}
		// Save the token before anything else can fail: the code cannot be used twice.
		if err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE connections SET secret = $1, status = 'connected', status_detail = NULL, config = '{"token": true}', updated_at = now() WHERE kind = 'asana'`, sealed)
			return err
		}); err != nil {
			return err
		}
	case "connected":
		if g.Config.Listed {
			return nil
		}
	default:
		return nil
	}

	c, _, err := s.client(ctx, org)
	if err != nil {
		return err
	}
	cfg := AsanaConfig{Token: true, Listed: true, Projects: []asana.Ref{}}
	if cfg.Workspaces, err = c.Workspaces(ctx); err == nil {
		for _, ws := range cfg.Workspaces {
			var ps []asana.Ref
			if ps, err = c.Projects(ctx, ws.GID); err != nil {
				break
			}
			cfg.Projects = append(cfg.Projects, ps...)
		}
	}
	if errors.Is(err, asana.ErrAuth) {
		return s.markBroken(ctx, org, asanaReconnect)
	}
	if err != nil {
		return err // transient; the token is saved
	}
	raw, _ := json.Marshal(cfg)
	return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE connections SET config = $1, status_detail = $2, updated_at = now() WHERE kind = 'asana'`,
			raw, "Choose the project new tasks go to."); err != nil {
			return err
		}
		_, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.ConnectionPropertiesReady, Actor: "asana",
			Payload: map[string]any{"kind": "asana", "projects": len(cfg.Projects)}})
		return err
	})
}

type asanaRevokeWorker struct {
	river.WorkerDefaults[jobargs.AsanaRevoke]
	s *Asana
}

func (w *asanaRevokeWorker) Work(ctx context.Context, job *river.Job[jobargs.AsanaRevoke]) error {
	s, org := w.s, job.Args.OrgID
	var g asanaGrant
	err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		g, err = loadAsana(ctx, tx)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (g.Status != "revoked" || len(g.Secret) == 0)) {
		return nil
	}
	if err != nil {
		return err
	}
	// Only a refresh token (not an unused code) is worth revoking; a code expires anyway.
	if rt, err := s.Box.Open(org, g.Secret); err == nil && g.Config.Token {
		if err := s.Revoke(ctx, string(rt)); err != nil {
			return err
		}
	}
	return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE connections SET secret = NULL, updated_at = now() WHERE kind = 'asana' AND status = 'revoked'`)
		return err
	})
}

// ---- tasks ---------------------------------------------------------------------------

// taskSubject is what a task is created from.
type taskSubject struct {
	Name  string
	Notes string
	Done  bool // already resolved: nothing to create
}

func externalID(org, source, subject string) string {
	return "vellatry:" + org + ":" + source + ":" + subject
}

func (s *Asana) loadSubject(ctx context.Context, tx pgx.Tx, source, id string) (taskSubject, error) {
	var t taskSubject
	switch source {
	case "fix":
		var title, instructions, status string
		var snippet, page *string
		err := tx.QueryRow(ctx, `SELECT title, instructions, snippet, page_url, status FROM fixes WHERE id::text = $1`, id).
			Scan(&title, &instructions, &snippet, &page, &status)
		if err != nil {
			return t, err
		}
		var notes strings.Builder
		notes.WriteString(instructions + "\n")
		if page != nil {
			notes.WriteString("\nPage: " + *page + "\n")
		}
		if snippet != nil {
			notes.WriteString("\nCopy this:\n\n" + *snippet + "\n")
		}
		notes.WriteString("\nVellatry checks the site on its next crawl and closes this task when the change is live.\n")
		notes.WriteString(s.AppURL + "/fixes/" + id + "\n")
		return taskSubject{Name: title, Notes: notes.String(), Done: status == "live" || status == "measured" || status == "dismissed"}, nil
	case "blindspot":
		var engine, kind, prompt, status string
		var competitor, topic *string
		err := tx.QueryRow(ctx, `
			SELECT b.engine, b.kind, p.text, b.status, b.winning_competitor, t.name
			FROM blindspots b JOIN prompts p ON p.id = b.prompt_id LEFT JOIN topics t ON t.id = p.topic_id
			WHERE b.id::text = $1`, id).Scan(&engine, &kind, &prompt, &status, &competitor, &topic)
		if err != nil {
			return t, err
		}
		eng := automation.EngineName(engine)
		name := fmt.Sprintf("AI blindspot on %s: %q", eng, prompt)
		var notes strings.Builder
		switch {
		case kind == "displacement" && competitor != nil:
			fmt.Fprintf(&notes, "%s names %s ahead of us when asked %q.\n", eng, *competitor, prompt)
		case kind == "visibility":
			fmt.Fprintf(&notes, "%s leaves us out of its answers to %q.\n", eng, prompt)
		default:
			fmt.Fprintf(&notes, "%s: a %s blindspot on %q.\n", eng, kind, prompt)
		}
		if topic != nil {
			notes.WriteString("Topic: " + *topic + "\n")
		}
		notes.WriteString("\nThe answers, the sources the engine cited and suggested fixes are in Vellatry. It closes this task when the engine starts mentioning us.\n")
		notes.WriteString(s.AppURL + "/visibility/blindspots/" + id + "\n")
		return taskSubject{Name: name, Notes: notes.String(), Done: status == "resolved" || status == "dismissed"}, nil
	}
	return t, fmt.Errorf("unknown task source %q", source)
}

type asanaCreateTaskWorker struct {
	river.WorkerDefaults[jobargs.AsanaCreateTask]
	s *Asana
}

// Work creates the task, or finds the one an earlier attempt created (by its external
// id), or reopens it if the item came back after being closed.
func (w *asanaCreateTaskWorker) Work(ctx context.Context, job *river.Job[jobargs.AsanaCreateTask]) error {
	s, org, a := w.s, job.Args.OrgID, job.Args
	c, cfg, err := s.client(ctx, org)
	if err != nil {
		return w.fail(ctx, a, err)
	}
	if cfg.Project == "" {
		return w.fail(ctx, a, fmt.Errorf("%w: choose an Asana project for new tasks first", asana.ErrRejected))
	}
	return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		// The row lock keeps a second attempt from creating the same task concurrently.
		err := tx.QueryRow(ctx, `SELECT status FROM task_links WHERE source = $1 AND subject_id = $2 FOR UPDATE`, a.Source, a.SubjectID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "creating") {
			return nil
		}
		if err != nil {
			return err
		}
		subj, err := s.loadSubject(ctx, tx, a.Source, a.SubjectID)
		if errors.Is(err, pgx.ErrNoRows) {
			_, err = tx.Exec(ctx, `DELETE FROM task_links WHERE source = $1 AND subject_id = $2`, a.Source, a.SubjectID)
			return err
		}
		if err != nil {
			return err
		}
		ext := externalID(org, a.Source, a.SubjectID)
		task, found, err := c.TaskByExternal(ctx, ext)
		if err == nil && !found {
			task, err = c.CreateTask(ctx, asana.NewTask{Project: cfg.Project, Name: subj.Name, Notes: subj.Notes, ExternalID: ext})
		} else if err == nil && task.Completed && !subj.Done {
			if err = c.SetCompleted(ctx, task.GID, false); err == nil {
				err = c.Comment(ctx, task.GID, "This came back: Vellatry found the problem again and reopened the task.")
			}
		}
		if err != nil {
			return w.failTx(ctx, tx, a, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE task_links SET status = 'open', external_id = $3, url = $4, error = NULL WHERE source = $1 AND subject_id = $2`,
			a.Source, a.SubjectID, task.GID, task.URL); err != nil {
			return err
		}
		if a.Source == "fix" {
			if _, err := tx.Exec(ctx, `UPDATE fixes SET status = 'sent', route = 'asana', sent_at = coalesce(sent_at, now()) WHERE id::text = $1 AND status = 'proposed'`, a.SubjectID); err != nil {
				return err
			}
		}
		_, err = s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.TaskCreated, SubjectID: a.Source + ":" + a.SubjectID, Actor: "asana",
			Payload: domainevents.TaskPayload{Source: a.Source, SubjectID: a.SubjectID, URL: task.URL}})
		return err
	})
}

// fail records why a task could not be created. Transient errors are returned for River
// to retry; everything else is shown on the item.
func (w *asanaCreateTaskWorker) fail(ctx context.Context, a jobargs.AsanaCreateTask, err error) error {
	if !permanentAsana(err) {
		return err
	}
	return db.InTenant(ctx, w.s.Pool, a.OrgID, func(ctx context.Context, tx pgx.Tx) error { return w.failTx(ctx, tx, a, err) })
}

func (w *asanaCreateTaskWorker) failTx(ctx context.Context, tx pgx.Tx, a jobargs.AsanaCreateTask, err error) error {
	if !permanentAsana(err) {
		return err
	}
	if errors.Is(err, asana.ErrAuth) {
		defer func() { _ = w.s.markBroken(context.WithoutCancel(ctx), a.OrgID, asanaReconnect) }()
	}
	msg := strings.TrimPrefix(err.Error(), "asana: ")
	if _, e := tx.Exec(ctx, `UPDATE task_links SET status = 'failed', error = $3 WHERE source = $1 AND subject_id = $2`, a.Source, a.SubjectID, msg); e != nil {
		return e
	}
	_, e := w.s.Bus.Emit(ctx, tx, a.OrgID, events.Event{Kind: domainevents.TaskFailed, SubjectID: a.Source + ":" + a.SubjectID, Actor: "asana",
		Payload: domainevents.TaskPayload{Source: a.Source, SubjectID: a.SubjectID, Error: msg}})
	return e
}

func permanentAsana(err error) bool {
	return errors.Is(err, asana.ErrAuth) || errors.Is(err, asana.ErrRejected) || errors.Is(err, asana.ErrNotFound)
}

type asanaCloseDoneWorker struct {
	river.WorkerDefaults[jobargs.AsanaCloseDone]
	s *Asana
}

func (w *asanaCloseDoneWorker) Timeout(*river.Job[jobargs.AsanaCloseDone]) time.Duration {
	return 5 * time.Minute
}

// Work completes the tasks whose fix went live or whose blindspot resolved (or which the
// team dismissed), with a comment saying why. Rows are locked while Asana is called, so
// two runs never comment twice.
func (w *asanaCloseDoneWorker) Work(ctx context.Context, job *river.Job[jobargs.AsanaCloseDone]) error {
	s, org := w.s, job.Args.OrgID
	var pending bool
	if err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_links WHERE status = 'open')`).Scan(&pending)
	}); err != nil || !pending {
		return err
	}
	c, _, err := s.client(ctx, org)
	if errors.Is(err, asana.ErrAuth) {
		return nil // nothing can be closed until Asana is reconnected; the dashboard says so
	}
	if err != nil {
		return err
	}
	return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT l.source, l.subject_id, l.external_id,
			       coalesce(f.status, b.status) AS item_status
			FROM task_links l
			LEFT JOIN fixes f ON l.source = 'fix' AND f.id::text = l.subject_id
			LEFT JOIN blindspots b ON l.source = 'blindspot' AND b.id::text = l.subject_id
			WHERE l.status = 'open' AND l.external_id IS NOT NULL
			  AND (f.status IN ('live', 'measured', 'dismissed') OR b.status IN ('resolved', 'dismissed'))
			ORDER BY l.created_at
			LIMIT 25
			FOR UPDATE OF l SKIP LOCKED`)
		if err != nil {
			return err
		}
		type link struct{ source, subject, gid, itemStatus string }
		links, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (link, error) {
			var l link
			return l, r.Scan(&l.source, &l.subject, &l.gid, &l.itemStatus)
		})
		if err != nil || len(links) == 0 {
			return err
		}
		closed := 0
		for _, l := range links {
			note := "Vellatry confirmed this is fixed on the live site and closed the task."
			switch {
			case l.itemStatus == "dismissed":
				note = "Dismissed in Vellatry, so the task was closed."
			case l.source == "blindspot":
				note = "The AI engine now mentions the brand for this prompt. Vellatry closed the task."
			}
			err := c.Comment(ctx, l.gid, note)
			if err == nil {
				err = c.SetCompleted(ctx, l.gid, true)
			}
			if errors.Is(err, asana.ErrNotFound) {
				err = nil // deleted in Asana: nothing left to close
			}
			if err != nil {
				if errors.Is(err, asana.ErrAuth) {
					_ = s.markBroken(context.WithoutCancel(ctx), org, asanaReconnect)
					break
				}
				if closed == 0 {
					return err
				}
				break // keep what was closed; the next event or retry picks up the rest
			}
			if _, err := tx.Exec(ctx, `UPDATE task_links SET status = 'completed', completed_at = now() WHERE source = $1 AND subject_id = $2`, l.source, l.subject); err != nil {
				return err
			}
			closed++
		}
		if closed == 0 {
			return nil
		}
		_, err = s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.TasksCompleted, Actor: "asana", Payload: map[string]int{"tasks": closed}})
		return err
	})
}
