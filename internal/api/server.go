// Package api is the api role: it serves the dashboard from stored results and enqueues
// work. It never calls an external service itself; internal/archtest enforces that it
// cannot even import one.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/secrets"
)

// Server holds the api role's dependencies.
type Server struct {
	Pool           *pgxpool.Pool
	Bus            *events.Bus // with domainevents.Subscriptions(), so events trigger the same jobs as in the worker
	Verifier       Verifier
	Hub            *Hub
	Logger         *slog.Logger
	AllowedOrigins []string

	// Connections; nil when not configured. Box seals credentials and signs OAuth state.
	GoogleOAuth *oauth2.Config
	AsanaOAuth  *oauth2.Config
	Box         *secrets.Box
	AppURL      string // the web app, for redirects back from providers
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /oauth/google/callback", s.oauthCallback("google")) // authenticated by its signed state
	mux.HandleFunc("GET /oauth/asana/callback", s.oauthCallback("asana"))

	authed := http.NewServeMux()
	authed.HandleFunc("GET /v1/me", s.me)
	authed.HandleFunc("POST /v1/onboarding", s.onboarding)

	authed.HandleFunc("GET /v1/brand", s.getBrand)
	authed.HandleFunc("PUT /v1/brand", s.putBrand)
	authed.HandleFunc("POST /v1/competitors", s.addCompetitor)
	authed.HandleFunc("DELETE /v1/competitors/{id}", s.deleteCompetitor)

	authed.HandleFunc("GET /v1/settings", s.getSettings)
	authed.HandleFunc("PUT /v1/settings", s.putSettings)

	authed.HandleFunc("GET /v1/topics", s.listTopics)
	authed.HandleFunc("POST /v1/topics", s.addTopic)
	authed.HandleFunc("PATCH /v1/topics/{id}", s.patchTopic)

	authed.HandleFunc("GET /v1/prompts", s.listPrompts)
	authed.HandleFunc("POST /v1/prompts", s.addPrompt)
	authed.HandleFunc("PATCH /v1/prompts/{id}", s.patchPrompt)
	authed.HandleFunc("POST /v1/prompts/{id}/check", s.checkPrompt)
	authed.HandleFunc("GET /v1/prompts/{id}/answers", s.promptAnswers)

	authed.HandleFunc("GET /v1/visibility/performance", s.performance)
	authed.HandleFunc("GET /v1/visibility/grid", s.grid)
	authed.HandleFunc("GET /v1/visibility/blindspots", s.blindspots)
	authed.HandleFunc("PATCH /v1/visibility/blindspots/{id}", s.patchBlindspot)
	authed.HandleFunc("GET /v1/visibility/sources", s.sources)

	authed.HandleFunc("GET /v1/connections", s.listConnections)
	authed.HandleFunc("POST /v1/connections/google/start", s.startOAuth("google"))
	authed.HandleFunc("DELETE /v1/connections/google", s.disconnectGoogle)
	authed.HandleFunc("POST /v1/connections/asana/start", s.startOAuth("asana"))
	authed.HandleFunc("PUT /v1/connections/asana/project", s.chooseAsanaProject)
	authed.HandleFunc("DELETE /v1/connections/asana", s.disconnectAsana)
	authed.HandleFunc("PUT /v1/connections/{kind}", s.chooseProperty)

	authed.HandleFunc("GET /v1/search/overview", s.searchOverview)
	authed.HandleFunc("GET /v1/search/queries", s.searchTop("query"))
	authed.HandleFunc("GET /v1/search/pages", s.searchTop("page"))
	authed.HandleFunc("GET /v1/analytics/channels", s.channels("channel"))
	authed.HandleFunc("GET /v1/analytics/ai-referrals", s.channels("assistant"))

	authed.HandleFunc("POST /v1/site/crawl", s.requestCrawl)
	authed.HandleFunc("GET /v1/site/summary", s.siteSummary)
	authed.HandleFunc("GET /v1/site/findings", s.siteFindings)
	authed.HandleFunc("PATCH /v1/site/findings/{id}", s.patchFinding)
	authed.HandleFunc("GET /v1/site/pages", s.sitePages)
	authed.HandleFunc("GET /v1/fixes", s.listFixes)
	authed.HandleFunc("PATCH /v1/fixes/{id}", s.patchFix)
	authed.HandleFunc("POST /v1/fixes/{id}/asana", s.sendToAsana("fix"))
	authed.HandleFunc("POST /v1/visibility/blindspots/{id}/asana", s.sendToAsana("blindspot"))

	authed.HandleFunc("GET /v1/destinations", s.listDestinations)
	authed.HandleFunc("POST /v1/destinations", s.addDestination)
	authed.HandleFunc("PATCH /v1/destinations/{id}", s.patchDestination)
	authed.HandleFunc("DELETE /v1/destinations/{id}", s.deleteDestination)
	authed.HandleFunc("GET /v1/watchers", s.listWatchers)
	authed.HandleFunc("POST /v1/watchers", s.addWatcher)
	authed.HandleFunc("PATCH /v1/watchers/{id}", s.patchWatcher)
	authed.HandleFunc("DELETE /v1/watchers/{id}", s.deleteWatcher)
	authed.HandleFunc("GET /v1/notifications", s.listNotifications)

	authed.HandleFunc("GET /v1/events", s.listEvents)
	authed.HandleFunc("GET /v1/events/stream", s.streamEvents)

	mux.Handle("/v1/", s.authenticate(authed))
	return s.recover(s.cors(mux))
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Pool.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "database unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := s.Verifier.Verify(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Sign in to continue."})
			return
		}
		sess, err := s.resolveSession(r.Context(), id, r.Header.Get("X-Org-ID"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess)))
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(s.AllowedOrigins, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Org-ID, X-Dev-Email")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				s.Logger.ErrorContext(r.Context(), "panic", "value", v, "stack", string(debug.Stack()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong. Try again."})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
