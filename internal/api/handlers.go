package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/UncleSon21/vellatry/internal/automation"
	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/visibility/promptgen"
)

// ---- me and onboarding -------------------------------------------------------------

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, sessionFrom(r.Context()))
}

type onboardingRequest struct {
	OrgName     string             `json:"org_name"`
	Brand       brandInput         `json:"brand"`
	Competitors []brand.Competitor `json:"competitors"`
	Topics      []topicInput       `json:"topics"`
}

type brandInput struct {
	Name            string   `json:"name"`
	Domain          string   `json:"domain"`
	Aliases         []string `json:"aliases"`
	Exclusions      []string `json:"exclusions"`
	Differentiators []string `json:"differentiators"`
}

type topicInput struct {
	Name          string `json:"name"`
	DemandMonthly *int   `json:"demand_monthly"`
}

func (s *Server) onboarding(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if len(sess.Orgs) > 0 {
		s.fail(w, r, conflict("Your organisation is already set up."))
		return
	}
	var req onboardingRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	req.OrgName = strings.TrimSpace(req.OrgName)
	req.Brand.Name = strings.TrimSpace(req.Brand.Name)
	domain := brand.NormaliseDomain(req.Brand.Domain)
	switch {
	case req.OrgName == "":
		s.fail(w, r, badRequest("Enter your organisation's name."))
		return
	case req.Brand.Name == "" || domain == "":
		s.fail(w, r, badRequest("Enter your brand's name and website."))
		return
	case len(req.Competitors) > 20 || len(req.Topics) > 50:
		s.fail(w, r, badRequest("Add up to 20 competitors and 50 topics."))
		return
	}
	for _, c := range req.Competitors {
		if strings.TrimSpace(c.Name) == "" {
			s.fail(w, r, badRequest("Each competitor needs a name."))
			return
		}
	}
	for _, t := range req.Topics {
		if strings.TrimSpace(t.Name) == "" {
			s.fail(w, r, badRequest("Each topic needs a name."))
			return
		}
	}

	var orgID string
	err := db.InSystem(r.Context(), s.Pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO orgs (name) VALUES ($1) RETURNING id::text`, req.OrgName).Scan(&orgID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, sess.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO org_settings (org_id) VALUES ($1)`, orgID); err != nil {
			return err
		}
		b, _ := brand.Apply(brand.Brand{}, brand.Patch{
			Name: &req.Brand.Name, Domain: &domain, Aliases: &req.Brand.Aliases,
			Exclusions: &req.Brand.Exclusions, Differentiators: &req.Brand.Differentiators,
		}, brand.ByUser)
		prov, _ := json.Marshal(b.Provenance)
		if err := tx.QueryRow(ctx, `
			INSERT INTO brands (org_id, name, domain, aliases, exclusions, differentiators, provenance)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
			orgID, b.Name, b.Domain, nonNil(b.Aliases), nonNil(b.Exclusions), nonNil(b.Differentiators), prov).Scan(&b.ID); err != nil {
			return err
		}
		for _, c := range req.Competitors {
			c.Source = brand.ByUser
			if _, err := brand.AddCompetitor(ctx, tx, orgID, b.ID, c); err != nil {
				return err
			}
		}
		for _, t := range req.Topics {
			if _, err := insertTopic(ctx, tx, orgID, b.ID, t, locationName(2036)); err != nil {
				return err
			}
		}
		if err := automation.InsertDefaults(ctx, tx, orgID); err != nil {
			return err
		}
		_, err := s.Bus.Emit(ctx, tx, orgID, events.Event{Kind: domainevents.OrgOnboarded, SubjectID: orgID, Actor: sess.UserID})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"org_id": orgID})
}

// ---- brand -------------------------------------------------------------------------

func (s *Server) getBrand(w http.ResponseWriter, r *http.Request) {
	var b brand.Brand
	var comps []brand.Competitor
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		b, comps, err = brand.Load(ctx, tx)
		return err
	})
	if err != nil {
		s.fail(w, r, mapBrandErr(err))
		return
	}
	if comps == nil {
		comps = []brand.Competitor{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"brand": b, "competitors": comps})
}

func (s *Server) putBrand(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var p brand.Patch
	if err := decode(r, &p); err != nil {
		s.fail(w, r, err)
		return
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) == "" {
		s.fail(w, r, badRequest("The brand needs a name."))
		return
	}
	var out brand.Brand
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		b, _, err := brand.Load(ctx, tx)
		if err != nil {
			return err
		}
		var changed []string
		out, changed = brand.Apply(b, p, brand.ByUser)
		if len(changed) == 0 {
			return nil
		}
		if err := brand.Save(ctx, tx, out); err != nil {
			return err
		}
		_, err = s.Bus.Emit(ctx, tx, b.OrgID, events.Event{Kind: domainevents.BrandUpdated, SubjectID: b.ID, Actor: sessionFrom(ctx).UserID,
			Payload: map[string]any{"fields": changed}})
		return err
	})
	if err != nil {
		s.fail(w, r, mapBrandErr(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) addCompetitor(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var c brand.Competitor
	if err := decode(r, &c); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(c.Name) == "" {
		s.fail(w, r, badRequest("The competitor needs a name."))
		return
	}
	c.Source = brand.ByUser
	var out brand.Competitor
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		b, _, err := brand.Load(ctx, tx)
		if err != nil {
			return err
		}
		out, err = brand.AddCompetitor(ctx, tx, b.OrgID, b.ID, c)
		return err
	})
	if err != nil {
		s.fail(w, r, mapBrandErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) deleteCompetitor(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, r, notFound("Competitor not found."))
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error { return brand.DeleteCompetitor(ctx, tx, id) })
	if err != nil && strings.Contains(err.Error(), "not found") {
		err = notFound("Competitor not found.")
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func mapBrandErr(err error) error {
	if errors.Is(err, brand.ErrNoBrand) {
		return notFound("Your brand isn't set up yet.")
	}
	return err
}

// ---- settings ----------------------------------------------------------------------

// planCaps is the most answers per day each plan may be set to.
var planCaps = map[string]int{"trial": 60, "starter": 150, "growth": 400, "scale": 1000}

var knownEngines = []string{"chatgpt", "gemini", "ai_overview"}

type settingsBody struct {
	Plan              string   `json:"plan"`
	PlanCap           int      `json:"plan_cap"`
	DailyAnswerBudget *int     `json:"daily_answer_budget"`
	DiscoverShare     *float64 `json:"discover_share"`
	Engines           []string `json:"engines"`
	JudgeEnabled      *bool    `json:"judge_enabled"`
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	var out settingsBody
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var budget int
		var share float64
		var judge bool
		err := tx.QueryRow(ctx, `SELECT plan, daily_answer_budget, discover_share::float8, engines, judge_enabled FROM org_settings LIMIT 1`).
			Scan(&out.Plan, &budget, &share, &out.Engines, &judge)
		out.DailyAnswerBudget, out.DiscoverShare, out.JudgeEnabled = &budget, &share, &judge
		out.PlanCap = planCaps[out.Plan]
		return err
	})
	if err != nil {
		if isNoRows(err) {
			err = notFound("Settings aren't set up yet.")
		}
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in settingsBody
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	for _, e := range in.Engines {
		if !slices.Contains(knownEngines, e) {
			s.fail(w, r, badRequest("Unknown engine "+e+"."))
			return
		}
	}
	if in.Engines != nil && len(in.Engines) == 0 {
		s.fail(w, r, badRequest("Choose at least one engine."))
		return
	}
	if in.DiscoverShare != nil && (*in.DiscoverShare < 0 || *in.DiscoverShare > 1) {
		s.fail(w, r, badRequest("Discovery share must be between 0 and 1."))
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var plan string
		if err := tx.QueryRow(ctx, `SELECT plan FROM org_settings LIMIT 1`).Scan(&plan); err != nil {
			return err
		}
		if in.DailyAnswerBudget != nil && (*in.DailyAnswerBudget < 0 || *in.DailyAnswerBudget > planCaps[plan]) {
			return badRequest("Your plan allows up to " + itoa(planCaps[plan]) + " answers a day.")
		}
		_, err := tx.Exec(ctx, `
			UPDATE org_settings SET
			  daily_answer_budget = coalesce($1, daily_answer_budget),
			  discover_share = coalesce($2, discover_share),
			  engines = coalesce($3, engines),
			  judge_enabled = coalesce($4, judge_enabled),
			  updated_at = now()`,
			in.DailyAnswerBudget, in.DiscoverShare, in.Engines, in.JudgeEnabled)
		return err
	})
	if err != nil {
		if isNoRows(err) {
			err = notFound("Settings aren't set up yet.")
		}
		s.fail(w, r, err)
		return
	}
	s.getSettings(w, r)
}

// ---- topics ------------------------------------------------------------------------

type topic struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Source        string    `json:"source"`
	DemandMonthly *int      `json:"demand_monthly"`
	Status        string    `json:"status"`
	Prompts       int       `json:"prompts"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s *Server) listTopics(w http.ResponseWriter, r *http.Request) {
	var out []topic
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.id::text, t.name, t.source, t.demand_monthly, t.status, t.created_at,
			       (SELECT count(*) FROM prompts p WHERE p.topic_id = t.id AND p.status <> 'rejected')::int
			FROM topics t ORDER BY t.demand_monthly DESC NULLS LAST, t.name`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (topic, error) {
			var t topic
			return t, r.Scan(&t.ID, &t.Name, &t.Source, &t.DemandMonthly, &t.Status, &t.CreatedAt, &t.Prompts)
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

func (s *Server) addTopic(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in topicInput
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		s.fail(w, r, badRequest("The topic needs a name."))
		return
	}
	var out topic
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		b, _, err := brand.Load(ctx, tx)
		if err != nil {
			return err
		}
		var code int
		if err := tx.QueryRow(ctx, `SELECT location_code FROM org_settings LIMIT 1`).Scan(&code); err != nil {
			return err
		}
		if out, err = insertTopic(ctx, tx, b.OrgID, b.ID, in, locationName(code)); err != nil {
			return err
		}
		_, err = s.Bus.Emit(ctx, tx, b.OrgID, events.Event{Kind: domainevents.TopicAdded, SubjectID: out.ID, Actor: sessionFrom(ctx).UserID})
		return err
	})
	if err != nil {
		s.fail(w, r, mapBrandErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func insertTopic(ctx context.Context, tx pgx.Tx, orgID, brandID string, in topicInput, location string) (topic, error) {
	t := topic{Name: strings.Join(strings.Fields(in.Name), " "), Source: "manual", DemandMonthly: in.DemandMonthly, Status: "active"}
	err := tx.QueryRow(ctx, `
		INSERT INTO topics (org_id, brand_id, name, source, demand_monthly) VALUES ($1, $2, $3, 'manual', $4)
		RETURNING id::text, created_at`, orgID, brandID, t.Name, in.DemandMonthly).Scan(&t.ID, &t.CreatedAt)
	if isUniqueViolation(err) {
		return t, conflict("A topic with that name already exists.")
	}
	if err != nil {
		return t, err
	}
	for _, c := range promptgen.FromTopic(t.Name, location) {
		tag, err := tx.Exec(ctx, `
			INSERT INTO prompts (org_id, brand_id, topic_id, text, source, status) VALUES ($1, $2, $3, $4, $5, 'candidate')
			ON CONFLICT DO NOTHING`, orgID, brandID, t.ID, c.Text, c.Source)
		if err != nil {
			return t, err
		}
		t.Prompts += int(tag.RowsAffected())
	}
	return t, nil
}

func (s *Server) patchTopic(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		Status        *string `json:"status"`
		DemandMonthly *int    `json:"demand_monthly"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "out_of_scope" {
		s.fail(w, r, badRequest("Status must be active or out_of_scope."))
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, r, notFound("Topic not found."))
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE topics SET status = coalesce($2, status), demand_monthly = coalesce($3, demand_monthly) WHERE id = $1`,
			id, in.Status, in.DemandMonthly)
		if err == nil && tag.RowsAffected() == 0 {
			return notFound("Topic not found.")
		}
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- prompts -----------------------------------------------------------------------

type promptRow struct {
	ID        string          `json:"id"`
	Text      string          `json:"text"`
	Source    string          `json:"source"`
	Status    string          `json:"status"`
	TopicID   *string         `json:"topic_id"`
	Topic     *string         `json:"topic"`
	Cells     json.RawMessage `json:"cells"` // engine -> {phase, band, answers, present, mentions}
	CreatedAt time.Time       `json:"created_at"`
}

var promptStatuses = []string{"candidate", "active", "tracked", "rejected"}

func (s *Server) listPrompts(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "all"
	}
	if status != "all" && !slices.Contains(promptStatuses, status) {
		s.fail(w, r, badRequest("Unknown status."))
		return
	}
	var out []promptRow
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT p.id::text, p.text, p.source, p.status, p.topic_id::text, t.name, p.created_at,
			       coalesce(json_object_agg(c.engine, json_build_object('phase', c.phase, 'band', c.band,
			           'answers', c.answers, 'present', c.present, 'mentions', c.mentions)) FILTER (WHERE c.engine IS NOT NULL), '{}')
			FROM prompts p LEFT JOIN topics t ON t.id = p.topic_id LEFT JOIN cells c ON c.prompt_id = p.id
			WHERE $1 = 'all' OR p.status = $1
			GROUP BY p.id, t.name ORDER BY p.created_at DESC LIMIT 500`, status)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (promptRow, error) {
			var p promptRow
			return p, r.Scan(&p.ID, &p.Text, &p.Source, &p.Status, &p.TopicID, &p.Topic, &p.CreatedAt, &p.Cells)
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

func (s *Server) addPrompt(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		Text    string  `json:"text"`
		TopicID *string `json:"topic_id"`
		Track   bool    `json:"track"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	in.Text = strings.Join(strings.Fields(in.Text), " ")
	if len(in.Text) < 5 || len(in.Text) > 500 {
		s.fail(w, r, badRequest("A prompt is 5 to 500 characters."))
		return
	}
	if in.TopicID != nil && !validUUID(*in.TopicID) {
		s.fail(w, r, badRequest("Unknown topic."))
		return
	}
	status := "candidate"
	if in.Track {
		status = "tracked"
	}
	var id string
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		b, _, err := brand.Load(ctx, tx)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO prompts (org_id, brand_id, topic_id, text, source, status) VALUES ($1, $2, $3, $4, 'manual', $5)
			RETURNING id::text`, b.OrgID, b.ID, in.TopicID, in.Text, status).Scan(&id)
		if isUniqueViolation(err) {
			return conflict("That prompt already exists.")
		}
		if isForeignKeyViolation(err) {
			return badRequest("Unknown topic.")
		}
		return err
	})
	if err != nil {
		s.fail(w, r, mapBrandErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": status})
}

func (s *Server) patchPrompt(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if !slices.Contains(promptStatuses, in.Status) {
		s.fail(w, r, badRequest("Status must be candidate, active, tracked or rejected."))
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, r, notFound("Prompt not found."))
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var from string
		err := tx.QueryRow(ctx, `SELECT status FROM prompts WHERE id = $1 FOR UPDATE`, id).Scan(&from)
		if isNoRows(err) {
			return notFound("Prompt not found.")
		}
		if err != nil || from == in.Status {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE prompts SET status = $2, updated_at = now() WHERE id = $1`, id, in.Status); err != nil {
			return err
		}
		// Rejections are tenant memory: the event log records what the customer ruled out.
		_, err = s.Bus.Emit(ctx, tx, sessionFrom(ctx).OrgID, events.Event{Kind: domainevents.PromptStatusChanged, SubjectID: id,
			Actor: sessionFrom(ctx).UserID, Payload: map[string]string{"from": from, "to": in.Status}})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkPrompt queues a live check on every engine. The answers arrive over the event
// stream; nothing here waits for an engine.
func (s *Server) checkPrompt(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, r, notFound("Prompt not found."))
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM prompts WHERE id = $1`, id).Scan(&status)
		if isNoRows(err) {
			return notFound("Prompt not found.")
		}
		if err != nil {
			return err
		}
		if status == "rejected" {
			return badRequest("This prompt was rejected. Restore it before checking it.")
		}
		_, err = s.Bus.Emit(ctx, tx, sessionFrom(ctx).OrgID, events.Event{Kind: domainevents.CheckRequested, SubjectID: id, Actor: sessionFrom(ctx).UserID})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

// ---- helpers -----------------------------------------------------------------------

func locationName(code int) string {
	switch code {
	case 2036:
		return "Australia"
	case 2554:
		return "New Zealand"
	case 2826:
		return "the UK"
	case 2840:
		return "the US"
	}
	return ""
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23503"
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilT[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
