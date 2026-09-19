package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/automation"
	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/reports"
)

func reportErr(err error) error {
	if errors.Is(err, reports.ErrNotFound) || isNoRows(err) {
		return notFound("Report not found.")
	}
	return err
}

// ---- series --------------------------------------------------------------------------

func (s *Server) listSeries(w http.ResponseWriter, r *http.Request) {
	var out []reports.Series
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = reports.ListSeries(ctx, tx)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": nonNilT(out), "sections": reports.AllSections})
}

type seriesIn struct {
	Name         *string   `json:"name"`
	Period       *string   `json:"period"`
	Sections     *[]string `json:"sections"`
	Recipients   *[]string `json:"recipients"`
	AutoDraft    *bool     `json:"auto_draft"`
	DraftLagDays *int      `json:"draft_lag_days"`
	Accent       *string   `json:"accent"`
	Archived     *bool     `json:"archived"`
}

// applySeries validates in over cur.
func applySeries(ctx context.Context, tx pgx.Tx, cur reports.Series, in seriesIn) (reports.Series, error) {
	if in.Name != nil {
		cur.Name = strings.TrimSpace(*in.Name)
	}
	if cur.Name == "" || len(cur.Name) > 80 {
		return cur, badRequest("Give the series a name of up to 80 characters.")
	}
	if in.Period != nil {
		cur.Period = *in.Period
	}
	switch cur.Period {
	case reports.Month, reports.FYQuarter, reports.FY, reports.Custom:
	default:
		return cur, badRequest("Period must be month, fy_quarter, fy or custom.")
	}
	if in.Sections != nil {
		cur.Sections = reports.SortedSections(*in.Sections)
	}
	if len(cur.Sections) == 0 {
		return cur, badRequest("Choose at least one section.")
	}
	if in.Recipients != nil {
		cur.Recipients = []string{}
		if len(*in.Recipients) > 0 {
			domains, members, err := reports.ViewerDomains(ctx, tx)
			if err != nil {
				return cur, err
			}
			if cur.Recipients, err = automation.ValidRecipients(*in.Recipients, domains, members); err != nil {
				return cur, automationErr(err)
			}
		}
	}
	if in.AutoDraft != nil {
		cur.AutoDraft = *in.AutoDraft
	}
	if cur.Period == reports.Custom {
		cur.AutoDraft = false // a custom range has no "next period"
	}
	if in.DraftLagDays != nil {
		if *in.DraftLagDays < 0 || *in.DraftLagDays > 28 {
			return cur, badRequest("Drafts can wait 0 to 28 days after the period ends.")
		}
		cur.DraftLagDays = *in.DraftLagDays
	}
	if in.Accent != nil {
		if !reports.ValidAccent(*in.Accent) {
			return cur, badRequest("The colour must look like #1d4ed8.")
		}
		cur.Accent = *in.Accent
	}
	if in.Archived != nil {
		cur.Archived = *in.Archived
	}
	if cur.Recipients == nil {
		cur.Recipients = []string{}
	}
	return cur, nil
}

func (s *Server) addSeries(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in seriesIn
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	var id string
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		cur := reports.Series{Period: reports.Month, Sections: reports.AllSections, AutoDraft: true, DraftLagDays: 4, Accent: reports.DefaultAccent}
		sr, err := applySeries(ctx, tx, cur, in)
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM report_series WHERE NOT archived`).Scan(&n); err != nil {
			return err
		}
		if n >= 20 {
			return badRequest("You can have up to 20 report series.")
		}
		if _, err := reports.EnsureHub(ctx, tx, sessionFrom(ctx).OrgID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO report_series (org_id, name, period, sections, recipients, auto_draft, draft_lag_days, accent)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`,
			sessionFrom(ctx).OrgID, sr.Name, sr.Period, sr.Sections, sr.Recipients, sr.AutoDraft, sr.DraftLagDays, sr.Accent).Scan(&id)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) patchSeries(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in seriesIn
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := reports.LoadSeries(ctx, tx, r.PathValue("id"))
		if isNoRows(err) {
			return notFound("Series not found.")
		}
		if err != nil {
			return err
		}
		sr, err := applySeries(ctx, tx, cur, in)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE report_series SET name = $2, period = $3, sections = $4, recipients = $5, auto_draft = $6, draft_lag_days = $7,
			    accent = $8, archived = $9, updated_at = now() WHERE id::text = $1`,
			cur.ID, sr.Name, sr.Period, sr.Sections, sr.Recipients, sr.AutoDraft, sr.DraftLagDays, sr.Accent, sr.Archived)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requestDraft asks the worker to (re)build a draft. For month, quarter and financial
// year series, any date in the wanted period will do (default: the latest complete
// one); a custom series needs both dates.
func (s *Server) requestDraft(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	var p reports.Period
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		sr, err := reports.LoadSeries(ctx, tx, r.PathValue("id"))
		if isNoRows(err) {
			return notFound("Series not found.")
		}
		if err != nil {
			return err
		}
		today := time.Now().UTC()
		start, errS := time.Parse(time.DateOnly, in.Start)
		end, errE := time.Parse(time.DateOnly, in.End)
		switch {
		case sr.Period == reports.Custom:
			if errS != nil || errE != nil {
				return badRequest("A custom report needs a start and end date (YYYY-MM-DD).")
			}
			if p, err = reports.CustomPeriod(start, end); err != nil {
				return badRequest("The period must run forwards and be at most 366 days.")
			}
		case in.Start == "":
			p, err = reports.LatestComplete(sr.Period, today)
		case errS != nil:
			return badRequest("Start must be a date (YYYY-MM-DD).")
		default:
			p, err = reports.Containing(sr.Period, start)
		}
		if err != nil {
			return err
		}
		if !p.Start.Before(today) {
			return badRequest("That period hasn't started yet.")
		}
		var published bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reports WHERE series_id = $1 AND period_start = $2 AND period_end = $3 AND status <> 'draft')`,
			sr.ID, p.Start, p.End).Scan(&published); err != nil {
			return err
		}
		if published {
			return conflict("That report is already published. Edit it and publish a revision instead.")
		}
		_, err = s.Bus.Emit(ctx, tx, sessionFrom(ctx).OrgID, events.Event{Kind: domainevents.ReportDraftRequested, SubjectID: sr.ID, Actor: sessionFrom(ctx).UserID,
			Payload: domainevents.ReportPayload{SeriesID: sr.ID, Start: p.Start.Format(time.DateOnly), End: p.End.Format(time.DateOnly)}})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "period": p})
}

// ---- reports -------------------------------------------------------------------------

func (s *Server) listReports(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := q.Get("status")
	switch status {
	case "", "draft", "published", "withdrawn":
	default:
		s.fail(w, r, badRequest("Unknown status."))
		return
	}
	var out []reports.ReportRow
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = reports.ListReports(ctx, tx, q.Get("series"), status, limitParam(r, 100, 500))
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

type reportOut struct {
	reports.Report
	Unverified       []string `json:"unverified"`        // figures in the summary or notes the report does not show
	UnpublishedEdits bool     `json:"unpublished_edits"` // the working copy differs from the published version
}

func unverified(rep reports.Report) []string {
	allowed := reports.Figures(rep.Snapshot)
	text := rep.Summary
	for _, n := range rep.Notes {
		text += "\n" + n
	}
	return nonNil(reports.Unverified(text, allowed))
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	var out reportOut
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rep, err := reports.LoadReport(ctx, tx, r.PathValue("id"))
		if err != nil {
			return reportErr(err)
		}
		out = reportOut{Report: rep, Unverified: unverified(rep),
			UnpublishedEdits: rep.Status == "published" && rep.PublishedAt != nil && rep.UpdatedAt.After(rep.PublishedAt.Add(time.Second))}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// previewReport renders the working copy for the team, with the draft banner and the
// team-only list of omitted and incomplete sections.
func (s *Server) previewReport(w http.ResponseWriter, r *http.Request) {
	var page []byte
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rep, err := reports.LoadReport(ctx, tx, r.PathValue("id"))
		if err != nil {
			return reportErr(err)
		}
		page, err = reports.RenderHTML(reports.View{Snapshot: rep.Snapshot, Title: rep.Title, Summary: rep.Summary, Notes: rep.Notes,
			Accent: rep.Accent, Team: true, Draft: rep.Status != "published", Version: rep.Version, PublishedAt: rep.PublishedAt})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeHTML(w, http.StatusOK, page)
}

func (s *Server) patchReport(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		Title   *string            `json:"title"`
		Summary *string            `json:"summary"`
		Notes   *map[string]string `json:"notes"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rep, err := reports.LoadReport(ctx, tx, r.PathValue("id"))
		if err != nil {
			return reportErr(err)
		}
		if rep.Status == "withdrawn" {
			return conflict("This report was withdrawn.")
		}
		if in.Title != nil {
			t := strings.TrimSpace(*in.Title)
			if t == "" || len(t) > 150 {
				return badRequest("The title must be 1 to 150 characters.")
			}
			rep.Title = t
		}
		if in.Summary != nil {
			if len(*in.Summary) > 6000 {
				return badRequest("Keep the summary under 6,000 characters.")
			}
			rep.Summary = strings.TrimSpace(*in.Summary)
		}
		if in.Notes != nil {
			notes := map[string]string{}
			for k, v := range *in.Notes {
				if len(reports.SortedSections([]string{k})) == 0 {
					return badRequest("Unknown section " + k + ".")
				}
				if len(v) > 2000 {
					return badRequest("Keep each section note under 2,000 characters.")
				}
				if v = strings.TrimSpace(v); v != "" {
					notes[k] = v
				}
			}
			rep.Notes = notes
		}
		_, err = tx.Exec(ctx, `UPDATE reports SET title = $2, summary = $3, notes = $4, updated_at = now() WHERE id::text = $1`,
			rep.ID, rep.Title, rep.Summary, rep.Notes)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// publishReport freezes the working copy as a new version. Figures in the team's words
// that the report does not show are flagged first; the team confirms to publish anyway.
func (s *Server) publishReport(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		ConfirmUnverified bool  `json:"confirm_unverified"`
		Notify            *bool `json:"notify"` // email the recipients; default: first publish only
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	var version int
	var flagged []string
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rep, err := reports.LoadReport(ctx, tx, r.PathValue("id"))
		if err != nil {
			return reportErr(err)
		}
		if rep.Snapshot.Empty() {
			return badRequest("This report has no data, so it can't be published.")
		}
		if flagged = unverified(rep); len(flagged) > 0 && !in.ConfirmUnverified {
			return errUnverified
		}
		org, user := sessionFrom(ctx).OrgID, sessionFrom(ctx).UserID
		if version, err = reports.Publish(ctx, tx, org, rep.ID, user); err != nil {
			return err
		}
		notify := version == 1
		if in.Notify != nil {
			notify = *in.Notify
		}
		_, err = s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.ReportPublished, SubjectID: rep.ID, Actor: user,
			Payload: domainevents.ReportPayload{ReportID: rep.ID, SeriesID: rep.SeriesID, Version: version, Notify: notify}})
		return err
	})
	if errors.Is(err, errUnverified) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      "Some figures in the summary or notes don't appear in the report. Check them, or publish anyway.",
			"unverified": flagged,
		})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"version": version})
}

var errUnverified = errors.New("unverified figures")

func (s *Server) withdrawReport(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE reports SET status = 'withdrawn', updated_at = now() WHERE id::text = $1 AND status = 'published'`, r.PathValue("id"))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return notFound("No published report with that id.")
		}
		_, err = s.Bus.Emit(ctx, tx, sessionFrom(ctx).OrgID, events.Event{Kind: domainevents.ReportWithdrawn, SubjectID: r.PathValue("id"),
			Actor: sessionFrom(ctx).UserID, Payload: domainevents.ReportPayload{ReportID: r.PathValue("id")}})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteDraft removes a draft that was never published.
func (s *Server) deleteDraft(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM reports WHERE id::text = $1 AND status = 'draft' AND version = 0`, r.PathValue("id"))
		if err == nil && tag.RowsAffected() == 0 {
			return notFound("No unpublished draft with that id.")
		}
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type viewOut struct {
	Version  int       `json:"version"`
	Viewer   string    `json:"viewer"`
	Format   string    `json:"format"`
	ViewedAt time.Time `json:"viewed_at"`
}

func (s *Server) reportViews(w http.ResponseWriter, r *http.Request) {
	var out []viewOut
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT version, viewer, format, viewed_at FROM report_views WHERE report_id::text = $1 ORDER BY viewed_at DESC LIMIT 500`, r.PathValue("id"))
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (viewOut, error) {
			var v viewOut
			return v, r.Scan(&v.Version, &v.Viewer, &v.Format, &v.ViewedAt)
		})
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

// ---- hub settings --------------------------------------------------------------------

// Free email providers can never be allowed: anyone could then open the hub.
var publicMailDomains = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true, "live.com": true, "msn.com": true,
	"yahoo.com": true, "yahoo.com.au": true, "icloud.com": true, "me.com": true, "mac.com": true, "aol.com": true,
	"proton.me": true, "protonmail.com": true, "bigpond.com": true, "bigpond.net.au": true, "optusnet.com.au": true,
	"gmx.com": true, "zoho.com": true, "yandex.com": true, "mail.com": true, "hey.com": true, "fastmail.com": true,
}

type hubOut struct {
	reports.Hub
	URL     string   `json:"url"`
	Domains []string `json:"domains"` // every domain that can sign in, the brand's included
}

func (s *Server) hubURL(slug string) string { return strings.TrimRight(s.HubURL, "/") + "/hub/" + slug }

func (s *Server) loadHubOut(ctx context.Context, tx pgx.Tx) (hubOut, error) {
	h, err := reports.EnsureHub(ctx, tx, sessionFrom(ctx).OrgID)
	if err != nil {
		return hubOut{}, err
	}
	domains, _, err := reports.ViewerDomains(ctx, tx)
	return hubOut{Hub: h, URL: s.hubURL(h.Slug), Domains: nonNil(domains)}, err
}

func (s *Server) getHub(w http.ResponseWriter, r *http.Request) {
	var out hubOut
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.loadHubOut(ctx, tx)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) patchHub(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		AllowedDomains *[]string `json:"allowed_domains"`
		Enabled        *bool     `json:"enabled"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	var out hubOut
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := reports.EnsureHub(ctx, tx, sessionFrom(ctx).OrgID); err != nil {
			return err
		}
		if in.AllowedDomains != nil {
			if len(*in.AllowedDomains) > 10 {
				return badRequest("Add up to 10 extra domains.")
			}
			var domains []string
			for _, d := range *in.AllowedDomains {
				nd := brand.NormaliseDomain(d)
				if nd == "" || !strings.Contains(nd, ".") {
					return badRequest(d + " is not a domain.")
				}
				if publicMailDomains[nd] {
					return badRequest(nd + " is a public email provider; allowing it would let anyone sign in.")
				}
				domains = append(domains, nd)
			}
			if _, err := tx.Exec(ctx, `UPDATE hubs SET allowed_domains = $1, updated_at = now()`, nonNil(domains)); err != nil {
				return err
			}
		}
		if in.Enabled != nil {
			if _, err := tx.Exec(ctx, `UPDATE hubs SET enabled = $1, updated_at = now()`, *in.Enabled); err != nil {
				return err
			}
		}
		var err error
		out, err = s.loadHubOut(ctx, tx)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// rotateHub gives the hub a new address and signs every viewer out, for when the old
// link went somewhere it should not have.
func (s *Server) rotateHub(w http.ResponseWriter, r *http.Request) {
	if err := canEdit(r); err != nil {
		s.fail(w, r, err)
		return
	}
	var out hubOut
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := reports.EnsureHub(ctx, tx, sessionFrom(ctx).OrgID); err != nil {
			return err
		}
		slug, err := reports.NewSlug()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE hubs SET slug = $1, updated_at = now()`, slug); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM hub_sessions`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM hub_logins`); err != nil {
			return err
		}
		out, err = s.loadHubOut(ctx, tx)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
