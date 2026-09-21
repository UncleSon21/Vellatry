package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/visibility/detect"
	"github.com/UncleSon21/vellatry/internal/visibility/promptgen"
)

// The setup wizard. POST /v1/onboarding creates the organisation from its first step
// (name and website), which makes the wizard resumable: every later step edits through
// the ordinary endpoints, and this file adds only what the wizard needs on top: where
// the team is, a way to try the brand setup against real text, and the finish line.

type onboardingStatus struct {
	Org         bool         `json:"org"` // false: the wizard starts at step one
	Completed   bool         `json:"completed"`
	CompletedAt *time.Time   `json:"completed_at,omitempty"`
	CanEdit     bool         `json:"can_edit"`
	Brand       *brand.Brand `json:"brand,omitempty"`
	Competitors int          `json:"competitors"`
	Topics      topicCounts  `json:"topics"`
	Crawl       *crawlState  `json:"crawl"`     // the latest crawl: suggestions arrive when it finishes
	Connected   []string     `json:"connected"` // connection kinds that are working
	Answers     int          `json:"answers"`   // AI answers collected so far, for "Test my setup"
}

type topicCounts struct {
	Active   int `json:"active"`
	Proposed int `json:"proposed"`
}

type crawlState struct {
	Status string `json:"status"` // running | done | failed
	Pages  int    `json:"pages"`
	Error  string `json:"error,omitempty"`
}

func (s *Server) getOnboarding(w http.ResponseWriter, r *http.Request) {
	if sessionFrom(r.Context()).OrgID == "" {
		writeJSON(w, http.StatusOK, onboardingStatus{Connected: []string{}})
		return
	}
	var out onboardingStatus
	if err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = loadOnboarding(ctx, tx)
		return err
	}); err != nil {
		s.fail(w, r, err)
		return
	}
	out.CanEdit = canEdit(r) == nil
	writeJSON(w, http.StatusOK, out)
}

func loadOnboarding(ctx context.Context, tx pgx.Tx) (onboardingStatus, error) {
	out := onboardingStatus{Org: true, Connected: []string{}}
	b, comps, err := brand.Load(ctx, tx)
	switch {
	case err == nil:
		out.Brand, out.Competitors = &b, len(comps)
	case !errors.Is(err, brand.ErrNoBrand):
		return out, err
	}
	err = tx.QueryRow(ctx, `SELECT onboarded_at FROM org_settings`).Scan(&out.CompletedAt)
	if err != nil && !isNoRows(err) {
		return out, err
	}
	out.Completed = out.CompletedAt != nil
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'active'), count(*) FILTER (WHERE status = 'proposed') FROM topics`).
		Scan(&out.Topics.Active, &out.Topics.Proposed); err != nil {
		return out, err
	}
	var c crawlState
	var crawlErr *string
	err = tx.QueryRow(ctx, `SELECT status, pages, error FROM crawls ORDER BY started_at DESC LIMIT 1`).Scan(&c.Status, &c.Pages, &crawlErr)
	switch {
	case err == nil:
		if crawlErr != nil {
			c.Error = *crawlErr
		}
		out.Crawl = &c
	case !isNoRows(err):
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT kind FROM connections WHERE status = 'connected' ORDER BY kind`)
	if err != nil {
		return out, err
	}
	if out.Connected, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return out, err
	}
	if out.Connected == nil {
		out.Connected = []string{}
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FROM answers WHERE present`).Scan(&out.Answers)
	return out, err
}

// completeOnboarding is the wizard's last step. It starts the work that depends on the
// team's answers, so it needs at least one topic to measure; finishing twice is fine.
func (s *Server) completeOnboarding(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var out onboardingStatus
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM topics WHERE status = 'active'`).Scan(&active); err != nil {
			return err
		}
		if active == 0 {
			return badRequest("Choose at least one topic to track before finishing.")
		}
		tag, err := tx.Exec(ctx, `UPDATE org_settings SET onboarded_at = now(), updated_at = now() WHERE onboarded_at IS NULL`)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			sess := sessionFrom(ctx)
			if _, err := s.Bus.Emit(ctx, tx, sess.OrgID, events.Event{Kind: domainevents.OnboardingCompleted, SubjectID: sess.OrgID, Actor: sess.UserID}); err != nil {
				return err
			}
		}
		out, err = loadOnboarding(ctx, tx)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out.CanEdit = true
	writeJSON(w, http.StatusOK, out)
}

// ---- Test my setup -----------------------------------------------------------------

type brandTestRequest struct {
	Text       string    `json:"text"`      // pasted text; or
	AnswerID   int64     `json:"answer_id"` // an answer Vellatry collected
	Aliases    *[]string `json:"aliases"`   // unsaved edits to try before saving them
	Exclusions *[]string `json:"exclusions"`
}

type brandTestResult struct {
	Segments    []detect.Segment `json:"segments"`
	Brand       int              `json:"brand"` // mentions that count, as detection will count them
	Competitors []detect.Mention `json:"competitors"`
	Excluded    int              `json:"excluded"` // lookalikes the exclusions stopped
}

const maxTestText = 20000

// testBrand highlights what the brand setup would count in a piece of text. It uses the
// same matcher as detection, so what the team sees here is what the reports will count.
func (s *Server) testBrand(w http.ResponseWriter, r *http.Request) {
	var in brandTestRequest
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	switch {
	case in.Text == "" && in.AnswerID == 0:
		s.fail(w, r, badRequest("Paste some text or choose an answer to test against."))
		return
	case len(in.Text) > maxTestText:
		s.fail(w, r, badRequest("Paste up to 20,000 characters."))
		return
	}
	var out brandTestResult
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		b, comps, err := brand.Load(ctx, tx)
		if err != nil {
			return err
		}
		if in.AnswerID != 0 {
			err := tx.QueryRow(ctx, `SELECT text FROM answers WHERE id = $1 AND present`, in.AnswerID).Scan(&in.Text)
			if isNoRows(err) {
				return notFound("Answer not found.")
			}
			if err != nil {
				return err
			}
		}
		if in.Aliases != nil {
			b.Aliases = brand.Clean(*in.Aliases)
		}
		if in.Exclusions != nil {
			b.Exclusions = brand.Clean(*in.Exclusions)
		}
		be, ce := brand.Entities(b, comps)
		res := detect.Analyze(in.Text, nil, be, ce)
		out = brandTestResult{Segments: detect.Highlight(in.Text, be, ce), Brand: res.Brand.Count, Competitors: res.Competitors}
		for _, seg := range out.Segments {
			if seg.Excluded {
				out.Excluded++
			}
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, mapBrandErr(err))
		return
	}
	if out.Competitors == nil {
		out.Competitors = []detect.Mention{}
	}
	if out.Segments == nil {
		out.Segments = []detect.Segment{}
	}
	writeJSON(w, http.StatusOK, out)
}

type brandSample struct {
	ID          int64     `json:"id"`
	Engine      string    `json:"engine"`
	Prompt      string    `json:"prompt"`
	Text        string    `json:"text"`
	CollectedAt time.Time `json:"collected_at"`
}

// brandSamples lists recent AI answers to test the setup against: real text from the
// engines, not a made-up example.
func (s *Server) brandSamples(w http.ResponseWriter, r *http.Request) {
	var out []brandSample
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id, a.engine, p.text, left(a.text, $1), a.collected_at
			FROM answers a JOIN prompts p ON p.id = a.prompt_id
			WHERE a.present AND a.text <> ''
			ORDER BY a.collected_at DESC LIMIT 6`, maxTestText)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (brandSample, error) {
			var a brandSample
			return a, r.Scan(&a.ID, &a.Engine, &a.Prompt, &a.Text, &a.CollectedAt)
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

// ---- prompts for a topic -----------------------------------------------------------

// promptsForTopic gives a topic its starting prompts. A topic with none is measured by
// nothing, so every path that makes a topic active comes through here: adding one by
// hand, and approving one Vellatry proposed (from the site or from keyword research).
func promptsForTopic(ctx context.Context, tx pgx.Tx, topicID string) (int, error) {
	var org, brandID, name string
	var code int
	err := tx.QueryRow(ctx, `
		SELECT t.org_id::text, t.brand_id::text, t.name, coalesce(st.location_code, 2036)
		FROM topics t LEFT JOIN org_settings st ON st.org_id = t.org_id WHERE t.id::text = $1`, topicID).
		Scan(&org, &brandID, &name, &code)
	if err != nil {
		return 0, err
	}
	return insertPrompts(ctx, tx, org, brandID, topicID, name, locationName(code))
}

func insertPrompts(ctx context.Context, tx pgx.Tx, org, brandID, topicID, name, location string) (int, error) {
	n := 0
	for _, c := range promptgen.FromTopic(name, location) {
		tag, err := tx.Exec(ctx, `
			INSERT INTO prompts (org_id, brand_id, topic_id, text, source, status) VALUES ($1, $2, $3, $4, $5, 'candidate')
			ON CONFLICT DO NOTHING`, org, brandID, topicID, c.Text, c.Source)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}
