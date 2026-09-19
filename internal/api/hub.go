package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/reports"
)

// The reports hub is public HTML served by the api from stored rows. Nobody signs in
// with Clerk here: a sign-in link emailed to an allowed address opens a hub session.

const hubCookie = "vellatry_hub"

func writeHTML(w http.ResponseWriter, status int, page []byte) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'self'; frame-ancestors 'self'; base-uri 'none'")
	w.WriteHeader(status)
	_, _ = w.Write(page)
}

// hubHeaders keeps the hub out of search engines, caches and other sites' frames.
func hubHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Cache-Control", "private, no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

var slugRE = regexp.MustCompile(`^[a-z2-7]{8,32}$`)

type hubCtx struct {
	org, slug, base, brand, orgName string
}

// hubFor resolves a hub by its slug. It is the one system-role read the hub makes, and
// it returns only the org id.
func (s *Server) hubFor(ctx context.Context, slug string) (hubCtx, bool, error) {
	h := hubCtx{slug: slug, base: "/hub/" + slug}
	if !slugRE.MatchString(slug) {
		return h, false, nil
	}
	err := db.InSystem(ctx, s.Pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT org_id::text FROM hubs WHERE slug = $1 AND enabled`, slug).Scan(&h.org)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return h, false, nil
	}
	if err != nil {
		return h, false, err
	}
	err = db.InTenant(ctx, s.Pool, h.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT o.name, coalesce((SELECT name FROM brands ORDER BY created_at LIMIT 1), '') FROM orgs o`).Scan(&h.orgName, &h.brand)
	})
	return h, err == nil, err
}

func (s *Server) hubPage(w http.ResponseWriter, status int, p reports.HubPage) {
	page, err := reports.RenderHubPage(p)
	if err != nil {
		s.Logger.Error("render hub page", "error", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	writeHTML(w, status, page)
}

func (s *Server) hubNotFound(w http.ResponseWriter) {
	s.hubPage(w, http.StatusNotFound, reports.HubPage{Kind: "message", Error: "Not found",
		Message: "This reports link doesn't exist or has been changed. Ask the marketing team for the current one."})
}

func (s *Server) hubError(w http.ResponseWriter, r *http.Request, err error) {
	s.Logger.ErrorContext(r.Context(), "hub request failed", "path", r.URL.Path, "error", err)
	s.hubPage(w, http.StatusInternalServerError, reports.HubPage{Kind: "message", Error: "Something went wrong", Message: "Try again in a moment."})
}

// withHub resolves the slug for every hub route.
func (s *Server) withHub(fn func(w http.ResponseWriter, r *http.Request, h hubCtx)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, ok, err := s.hubFor(r.Context(), r.PathValue("slug"))
		switch {
		case err != nil:
			s.hubError(w, r, err)
		case !ok:
			s.hubNotFound(w)
		default:
			fn(w, r, h)
		}
	}
}

// sameOrigin rejects a cross-site form post. Browsers send Origin on POST.
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return origin == "" // no header: a non-browser client, which cannot carry a victim's cookie
	}
	hub, err := url.Parse(s.HubURL)
	return err == nil && strings.EqualFold(origin, hub.Scheme+"://"+hub.Host)
}

func (s *Server) viewer(r *http.Request, h hubCtx) (string, error) {
	c, err := r.Cookie(hubCookie)
	if err != nil || c.Value == "" {
		return "", reports.ErrInvalidLink
	}
	var email string
	err = db.InTenant(r.Context(), s.Pool, h.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		email, err = reports.SessionEmail(ctx, tx, c.Value, time.Now())
		return err
	})
	return email, err
}

func (s *Server) setHubCookie(w http.ResponseWriter, h hubCtx, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: hubCookie, Value: value, Path: h.base, MaxAge: maxAge, HttpOnly: true,
		Secure: strings.HasPrefix(s.HubURL, "https://"), SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) hubHome(w http.ResponseWriter, r *http.Request, h hubCtx) {
	email, err := s.viewer(r, h)
	if errors.Is(err, reports.ErrInvalidLink) || errors.Is(err, reports.ErrNotAllowed) {
		s.hubPage(w, http.StatusOK, reports.HubPage{Kind: "login", Org: h.orgName, Brand: h.brand, Action: h.base + "/login", Base: h.base})
		return
	}
	if err != nil {
		s.hubError(w, r, err)
		return
	}
	var list []reports.HubEntry
	err = db.InTenant(r.Context(), s.Pool, h.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		list, err = reports.PublishedReports(ctx, tx)
		return err
	})
	if err != nil {
		s.hubError(w, r, err)
		return
	}
	s.hubPage(w, http.StatusOK, reports.HubPage{Kind: "list", Org: h.orgName, Brand: h.brand, Viewer: email, Reports: list, Base: h.base})
}

// hubLogin asks the worker to email a sign-in link. The api sends nothing itself.
func (s *Server) hubLogin(w http.ResponseWriter, r *http.Request, h hubCtx) {
	if !s.sameOrigin(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	emailAddr := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	page := reports.HubPage{Kind: "login", Org: h.orgName, Brand: h.brand, Action: h.base + "/login", Email: emailAddr, Base: h.base}
	var allowed bool
	err := db.InTenant(r.Context(), s.Pool, h.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if allowed, err = reports.MayView(ctx, tx, emailAddr); err != nil || !allowed {
			return err
		}
		_, err = s.Bus.Emit(ctx, tx, h.org, events.Event{Kind: domainevents.HubLoginRequested, Actor: "hub", Payload: map[string]string{"email": emailAddr}})
		return err
	})
	if err != nil {
		s.hubError(w, r, err)
		return
	}
	if !allowed {
		page.Error = "Use your work email address. Only people at " + h.orgName + " can view these reports."
		s.hubPage(w, http.StatusOK, page)
		return
	}
	page.Kind = "sent"
	s.hubPage(w, http.StatusOK, page)
}

// hubAuthPage shows a button rather than signing in on GET: mail scanners open links in
// emails, and would otherwise use up the one-time link before the person does.
func (s *Server) hubAuthPage(w http.ResponseWriter, r *http.Request, h hubCtx) {
	token := r.URL.Query().Get("token")
	if token == "" || len(token) > 100 {
		s.hubPage(w, http.StatusBadRequest, reports.HubPage{Kind: "message", Error: "That sign-in link is incomplete", Message: "Ask for a new one.", Base: h.base})
		return
	}
	s.hubPage(w, http.StatusOK, reports.HubPage{Kind: "confirm", Org: h.orgName, Brand: h.brand, Action: h.base + "/auth", Token: token, Base: h.base})
}

func (s *Server) hubAuth(w http.ResponseWriter, r *http.Request, h hubCtx) {
	if !s.sameOrigin(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	token := r.PostFormValue("token")
	var session, email string
	err := db.InTenant(r.Context(), s.Pool, h.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if session, email, err = reports.ConsumeLogin(ctx, tx, h.org, token, time.Now()); err != nil {
			return err
		}
		_, err = s.Bus.Emit(ctx, tx, h.org, events.Event{Kind: domainevents.HubSignedIn, Actor: email})
		return err
	})
	if errors.Is(err, reports.ErrInvalidLink) || errors.Is(err, reports.ErrNotAllowed) {
		s.hubPage(w, http.StatusBadRequest, reports.HubPage{Kind: "message", Error: "That sign-in link has expired",
			Message: "Links work once, for 20 minutes. Ask for a new one.", Base: h.base})
		return
	}
	if err != nil {
		s.hubError(w, r, err)
		return
	}
	s.setHubCookie(w, h, session, int(reports.SessionTTL.Seconds()))
	http.Redirect(w, r, h.base, http.StatusSeeOther)
}

func (s *Server) hubLogout(w http.ResponseWriter, r *http.Request, h hubCtx) {
	if !s.sameOrigin(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if c, err := r.Cookie(hubCookie); err == nil && c.Value != "" {
		_ = db.InTenant(r.Context(), s.Pool, h.org, func(ctx context.Context, tx pgx.Tx) error { return reports.EndSession(ctx, tx, c.Value) })
	}
	s.setHubCookie(w, h, "", -1)
	http.Redirect(w, r, h.base, http.StatusSeeOther)
}

func versionParam(r *http.Request) int {
	v, err := strconv.Atoi(r.URL.Query().Get("v"))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// hubReport shows one published version and logs the view.
func (s *Server) hubReport(w http.ResponseWriter, r *http.Request, h hubCtx) {
	email, err := s.viewer(r, h)
	if err != nil {
		http.Redirect(w, r, h.base, http.StatusSeeOther)
		return
	}
	id := r.PathValue("id")
	var v reports.Version
	err = db.InTenant(r.Context(), s.Pool, h.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if v, err = reports.LoadVersion(ctx, tx, id, versionParam(r)); err != nil {
			return err
		}
		return reports.LogView(ctx, tx, h.org, id, v.Version, email, "web")
	})
	if errors.Is(err, reports.ErrNotFound) {
		s.hubPage(w, http.StatusNotFound, reports.HubPage{Kind: "message", Error: "Report not found", Message: "It may have been withdrawn.", Base: h.base})
		return
	}
	if err != nil {
		s.hubError(w, r, err)
		return
	}
	view := reports.View{Snapshot: v.Snapshot, Title: v.Title, Summary: v.Summary, Notes: v.Notes, Accent: v.Accent,
		Version: v.Version, PublishedAt: &v.PublishedAt, BackURL: h.base}
	if v.PDFStatus == "ready" {
		view.PDFURL = h.base + "/reports/" + id + "/pdf?v=" + strconv.Itoa(v.Version)
	}
	page, err := reports.RenderHTML(view)
	if err != nil {
		s.hubError(w, r, err)
		return
	}
	writeHTML(w, http.StatusOK, page)
}

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9 ._-]+`)

func (s *Server) hubPDF(w http.ResponseWriter, r *http.Request, h hubCtx) {
	email, err := s.viewer(r, h)
	if err != nil {
		http.Redirect(w, r, h.base, http.StatusSeeOther)
		return
	}
	id := r.PathValue("id")
	var doc []byte
	var v reports.Version
	err = db.InTenant(r.Context(), s.Pool, h.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if v, err = reports.LoadVersion(ctx, tx, id, versionParam(r)); err != nil {
			return err
		}
		if doc, err = reports.VersionPDF(ctx, tx, id, v.Version); err != nil {
			return err
		}
		return reports.LogView(ctx, tx, h.org, id, v.Version, email, "pdf")
	})
	if errors.Is(err, reports.ErrNotFound) {
		s.hubPage(w, http.StatusNotFound, reports.HubPage{Kind: "message", Error: "PDF not available", Message: "Open the report instead.", Base: h.base})
		return
	}
	if err != nil {
		s.hubError(w, r, err)
		return
	}
	name := strings.TrimSpace(unsafeFilename.ReplaceAllString(v.Title, ""))
	if name == "" {
		name = "report"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="`+name+`.pdf"`)
	_, _ = w.Write(doc)
}
