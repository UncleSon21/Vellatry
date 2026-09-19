package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/visibility/detect"
	"github.com/UncleSon21/vellatry/internal/visibility/engine"
	"github.com/UncleSon21/vellatry/internal/visibility/gap"
	"github.com/UncleSon21/vellatry/internal/visibility/promptgen"
)

// DetectorVersion is stamped on every signal row; bump when detect changes behaviour.
const DetectorVersion = "detect-1"

// Source is one citation shown with an answer.
type Source struct {
	URL    string `json:"url"`
	Title  string `json:"title,omitempty"`
	Domain string `json:"domain,omitempty"`
}

// Observed is what an engine answered, independent of the provider.
type Observed struct {
	Present       bool
	Text          string
	Sources       []Source
	FanOut        []string
	BrandEntities []string
	Model         string
}

// Change is a blindspot that opened or resolved.
type Change struct {
	PromptID   string `json:"prompt_id"`
	Engine     string `json:"engine"`
	Kind       string `json:"kind"`
	Confirmed  bool   `json:"confirmed"`
	Competitor string `json:"competitor,omitempty"`
}

// Outcome is what recording an answer changed.
type Outcome struct {
	AnswerID  int64
	New       bool // false when the answer had already been recorded
	Mentioned bool
	Cell      engine.Cell
	Band      gap.Band
	Opened    []Change
	Resolved  []Change
	Harvested int // new candidate prompts from fan-out queries
}

// maxCandidatesPerTopic caps fan-out harvesting so discovery cannot grow without bound.
const maxCandidatesPerTopic = 30

// RecordAnswer stores one answer and everything derived from it.
func RecordAnswer(ctx context.Context, tx pgx.Tx, t Task, obs Observed, brand detect.Entity, comps []detect.Entity) (Outcome, error) {
	var out Outcome
	sources, err := json.Marshal(nonNil(obs.Sources))
	if err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO answers (org_id, prompt_id, task_id, engine, round, present, text, sources, fan_out, brand_entities, model, method_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, nullif($11, ''), $12)
		ON CONFLICT (task_id) DO NOTHING
		RETURNING id`,
		t.OrgID, t.PromptID, t.ID, t.Engine, t.Round, obs.Present, obs.Text, sources,
		nonNilStr(obs.FanOut), nonNilStr(obs.BrandEntities), obs.Model, engine.MethodVersion).Scan(&out.AnswerID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already recorded by an earlier attempt of this job: nothing to add.
		err = tx.QueryRow(ctx, `SELECT id FROM answers WHERE task_id = $1`, t.ID).Scan(&out.AnswerID)
		return out, err
	}
	if err != nil {
		return out, err
	}
	out.New = true

	urls := make([]string, len(obs.Sources))
	for i, s := range obs.Sources {
		urls[i] = s.URL
	}
	var res detect.Result
	var verdict gap.Verdict
	if obs.Present {
		res = detect.Analyze(obs.Text, urls, brand, comps)
		verdict = gap.Classify(gap.Signals{
			BrandMentioned: res.Mentioned(), BrandCited: res.BrandCited, SourcesExist: len(urls) > 0,
			CompetitorMentioned: res.CompetitorMentioned(), CompetitorCited: len(res.CitedCompetitors) > 0,
		})
	}
	out.Mentioned = res.Mentioned()
	var mentionedComps []string
	for _, c := range res.Competitors {
		if c.Count > 0 {
			mentionedComps = append(mentionedComps, c.Entity)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO answer_signals (answer_id, org_id, brand_mentioned, brand_count, brand_position, brand_cited,
		    competitors_mentioned, cited_competitors, sources_count, visibility_gap, displacement_gap, detector_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		out.AnswerID, t.OrgID, res.Mentioned(), res.Brand.Count, res.BrandPosition, res.BrandCited,
		nonNilStr(mentionedComps), nonNilStr(res.CitedCompetitors), len(urls), verdict.Visibility, verdict.Displacement,
		DetectorVersion); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE answer_tasks SET status = 'done', completed_at = now() WHERE id = $1`, t.ID); err != nil {
		return out, err
	}

	if t.Purpose != engine.PurposeCheck {
		if err := updateCell(ctx, tx, t, out.AnswerID, &out); err != nil {
			return out, err
		}
	}
	if err := rollup(ctx, tx, t, obs, res); err != nil {
		return out, err
	}
	out.Harvested, err = harvest(ctx, tx, t, obs.FanOut)
	return out, err
}

// updateCell recomputes the cell from the answers stored for its round, advances it,
// and syncs its visibility and displacement blindspots.
func updateCell(ctx context.Context, tx pgx.Tx, t Task, answerID int64, out *Outcome) error {
	var phase string
	var round int
	err := tx.QueryRow(ctx, `SELECT phase, round FROM cells WHERE prompt_id = $1 AND engine = $2 FOR UPDATE`, t.PromptID, t.Engine).Scan(&phase, &round)
	if errors.Is(err, pgx.ErrNoRows) || round != t.Round {
		return nil // the cell moved to a newer round; this late answer only feeds rollups
	}
	if err != nil {
		return err
	}
	c := engine.Cell{Phase: engine.Phase(phase)}
	err = tx.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE a.present),
		       count(*) FILTER (WHERE s.brand_mentioned),
		       count(*) FILTER (WHERE s.displacement_gap)
		FROM answers a
		JOIN answer_signals s ON s.answer_id = a.id
		JOIN answer_tasks k ON k.id = a.task_id
		WHERE a.prompt_id = $1 AND a.engine = $2 AND a.round = $3 AND k.purpose <> 'check'`,
		t.PromptID, t.Engine, t.Round).Scan(&c.Answers, &c.Present, &c.Mentions, &c.Displacements)
	if err != nil {
		return err
	}
	c = engine.Advance(c)
	band := engine.Band(c)
	out.Cell, out.Band = c, band
	bandVal := &band
	if c.Phase != engine.Settled {
		bandVal = nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE cells SET phase = $3, answers = $4, present = $5, mentions = $6, displacements = $7, band = $8,
		       last_answer_at = now(), updated_at = now()
		WHERE prompt_id = $1 AND engine = $2`,
		t.PromptID, t.Engine, string(c.Phase), c.Answers, c.Present, c.Mentions, c.Displacements, bandVal); err != nil {
		return err
	}

	vis, displ := engine.Findings(c)
	settled := c.Phase == engine.Settled
	competitor := ""
	if displ != engine.None {
		if err := tx.QueryRow(ctx, `
			SELECT coalesce((SELECT comp FROM answers a JOIN answer_signals s ON s.answer_id = a.id,
			                 unnest(s.competitors_mentioned) AS comp
			                 WHERE a.prompt_id = $1 AND a.engine = $2 AND a.round = $3
			                 GROUP BY comp ORDER BY count(*) DESC, comp LIMIT 1), '')`,
			t.PromptID, t.Engine, t.Round).Scan(&competitor); err != nil {
			return err
		}
	}
	for _, f := range []struct {
		kind       string
		finding    engine.Finding
		competitor string
	}{{"visibility", vis, ""}, {"displacement", displ, competitor}} {
		ch, change, err := SyncBlindspot(ctx, tx, t.OrgID, t.PromptID, t.Engine, f.kind, f.finding, settled, answerID, f.competitor)
		if err != nil {
			return err
		}
		switch change {
		case Opened:
			out.Opened = append(out.Opened, ch)
		case Resolved:
			out.Resolved = append(out.Resolved, ch)
		}
	}
	return nil
}

// ChangeKind reports what SyncBlindspot did.
type ChangeKind int

const (
	Unchanged ChangeKind = iota
	Opened
	Resolved
)

// SyncBlindspot makes the stored blindspot match a finding. Dismissed blindspots stay
// dismissed: that was the customer's decision. A provisional blindspot that no longer
// holds mid-round is removed quietly; a settled cell with no finding resolves it.
func SyncBlindspot(ctx context.Context, tx pgx.Tx, orgID, promptID, eng, kind string, f engine.Finding, settled bool, evidence int64, competitor string) (Change, ChangeKind, error) {
	ch := Change{PromptID: promptID, Engine: eng, Kind: kind, Confirmed: f == engine.Confirmed, Competitor: competitor}
	var status string
	var confirmed bool
	err := tx.QueryRow(ctx, `SELECT status, confirmed FROM blindspots WHERE prompt_id = $1 AND engine = $2 AND kind = $3 FOR UPDATE`,
		promptID, eng, kind).Scan(&status, &confirmed)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ch, Unchanged, err
	}

	if f == engine.None {
		switch {
		case !exists || status != "open":
			return ch, Unchanged, nil
		case settled:
			_, err := tx.Exec(ctx, `UPDATE blindspots SET status = 'resolved', last_seen = now() WHERE prompt_id = $1 AND engine = $2 AND kind = $3`, promptID, eng, kind)
			return ch, Resolved, err
		case !confirmed:
			_, err := tx.Exec(ctx, `DELETE FROM blindspots WHERE prompt_id = $1 AND engine = $2 AND kind = $3`, promptID, eng, kind)
			return ch, Unchanged, err
		}
		return ch, Unchanged, nil
	}

	if exists && status == "dismissed" {
		return ch, Unchanged, nil
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO blindspots (org_id, prompt_id, engine, kind, status, confirmed, winning_competitor, evidence_answer_id)
		VALUES ($1, $2, $3, $4, 'open', $5, nullif($6, ''), $7)
		ON CONFLICT (prompt_id, engine, kind) DO UPDATE
		  SET status = 'open', confirmed = EXCLUDED.confirmed, winning_competitor = EXCLUDED.winning_competitor,
		      evidence_answer_id = EXCLUDED.evidence_answer_id, last_seen = now()`,
		orgID, promptID, eng, kind, ch.Confirmed, competitor, evidence)
	if err != nil {
		return ch, Unchanged, err
	}
	if !exists || status != "open" || (ch.Confirmed && !confirmed) {
		return ch, Opened, nil
	}
	return ch, Unchanged, nil
}

func rollup(ctx context.Context, tx pgx.Tx, t Task, obs Observed, res detect.Result) error {
	present, mentioned, posN := 0, 0, 0
	if obs.Present {
		present = 1
	}
	if res.Mentioned() {
		mentioned, posN = 1, 1
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO visibility_daily (org_id, day, engine, answers, present, mentioned, position_sum, position_n, method_version)
		VALUES ($1, (now() AT TIME ZONE 'UTC')::date, $2, 1, $3, $4, $5, $6, $7)
		ON CONFLICT (org_id, day, engine) DO UPDATE
		  SET answers = visibility_daily.answers + 1, present = visibility_daily.present + EXCLUDED.present,
		      mentioned = visibility_daily.mentioned + EXCLUDED.mentioned,
		      position_sum = visibility_daily.position_sum + EXCLUDED.position_sum,
		      position_n = visibility_daily.position_n + EXCLUDED.position_n`,
		t.OrgID, t.Engine, present, mentioned, res.BrandPosition, posN, engine.MethodVersion); err != nil {
		return err
	}
	if !obs.Present {
		return nil
	}
	entities := append([]detect.Mention{res.Brand}, res.Competitors...)
	for i, m := range entities {
		if m.Count == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO visibility_daily_entities (org_id, day, engine, entity, is_brand, answers, mentions)
			VALUES ($1, (now() AT TIME ZONE 'UTC')::date, $2, $3, $4, 1, $5)
			ON CONFLICT (org_id, day, engine, entity) DO UPDATE
			  SET answers = visibility_daily_entities.answers + 1,
			      mentions = visibility_daily_entities.mentions + EXCLUDED.mentions`,
			t.OrgID, t.Engine, m.Entity, i == 0, m.Count); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, s := range obs.Sources {
		d := s.Domain
		if d == "" {
			d = detect.Host(s.URL)
		}
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		if _, err := tx.Exec(ctx, `
			INSERT INTO source_citations_daily (org_id, day, engine, domain, citations)
			VALUES ($1, (now() AT TIME ZONE 'UTC')::date, $2, $3, 1)
			ON CONFLICT (org_id, day, engine, domain) DO UPDATE SET citations = source_citations_daily.citations + 1`,
			t.OrgID, t.Engine, d); err != nil {
			return err
		}
	}
	return nil
}

// harvest turns the engine's own fan-out searches into candidate prompts on the same
// topic, up to the per-topic cap.
func harvest(ctx context.Context, tx pgx.Tx, t Task, fanOut []string) (int, error) {
	if len(fanOut) == 0 {
		return 0, nil
	}
	var topicID *string
	var brandID string
	var candidates int
	err := tx.QueryRow(ctx, `
		SELECT p.topic_id::text, p.brand_id::text,
		       (SELECT count(*) FROM prompts q WHERE q.topic_id IS NOT DISTINCT FROM p.topic_id AND q.status = 'candidate')
		FROM prompts p WHERE p.id = $1`, t.PromptID).Scan(&topicID, &brandID, &candidates)
	if err != nil {
		return 0, err
	}
	room := maxCandidatesPerTopic - candidates
	if room <= 0 {
		return 0, nil
	}
	added := 0
	for _, c := range promptgen.FromFanOut(fanOut, []string{t.PromptText}, min(room, 3)) {
		tag, err := tx.Exec(ctx, `
			INSERT INTO prompts (org_id, brand_id, topic_id, text, source, status)
			VALUES ($1, $2, $3, $4, 'fan_out', 'candidate')
			ON CONFLICT DO NOTHING`, t.OrgID, brandID, topicID, c.Text)
		if err != nil {
			return added, err
		}
		added += int(tag.RowsAffected())
	}
	return added, nil
}

func nonNil(s []Source) []Source {
	if s == nil {
		return []Source{}
	}
	return s
}

func nonNilStr(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
