package google

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fake(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{HTTP: srv.Client(), Endpoints: Endpoints{SearchConsole: srv.URL + "/sc", AnalyticsData: srv.URL + "/data", AnalyticsAdmin: srv.URL + "/admin"}}
}

func TestDayRowsPaginates(t *testing.T) {
	var calls atomic.Int32
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.EscapedPath(), "/sites/sc-domain%3Akoala.com/searchAnalytics/query") {
			t.Errorf("path %s", r.URL.EscapedPath())
		}
		var q saQuery
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &q)
		if q.Type != "web" || len(q.Dimensions) != 4 || q.StartDate != "2026-09-01" {
			t.Errorf("query = %+v", q)
		}
		calls.Add(1)
		n := PageSize
		if q.StartRow >= PageSize {
			n = 3 // second, short page ends pagination
		}
		rows := make([]map[string]any, n)
		for i := range rows {
			rows[i] = map[string]any{"keys": []string{"2026-09-01", "q", "https://koala.com/", "aus"}, "clicks": 1, "impressions": 10, "position": 4.5}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rows": rows})
	})
	rows, err := c.DayRows(context.Background(), "sc-domain:koala.com", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != PageSize+3 || calls.Load() != 2 {
		t.Errorf("rows %d, calls %d", len(rows), calls.Load())
	}
	if rows[0].Impressions != 10 || rows[0].Country != "aus" {
		t.Errorf("row = %+v", rows[0])
	}
}

func TestErrorsClassified(t *testing.T) {
	for status, want := range map[int]error{401: ErrAuth, 403: ErrAuth, 429: ErrTransient, 503: ErrTransient} {
		c := fake(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		if _, err := c.Sites(context.Background()); !errors.Is(err, want) {
			t.Errorf("http %d: got %v, want %v", status, err, want)
		}
	}
}

func TestSitesSkipsUnverified(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"siteEntry":[{"siteUrl":"sc-domain:koala.com","permissionLevel":"siteOwner"},{"siteUrl":"https://x.com/","permissionLevel":"siteUnverifiedUser"}]}`))
	})
	sites, err := c.Sites(context.Background())
	if err != nil || len(sites) != 1 || sites[0].URL != "sc-domain:koala.com" {
		t.Errorf("sites = %+v, %v", sites, err)
	}
}

func TestAnalyticsDayAndProperties(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/properties/42:runReport"):
			_, _ = w.Write([]byte(`{"rowCount":1,"rows":[{"dimensionValues":[{"value":"20260901"},{"value":"Referral"},{"value":"chatgpt.com"},{"value":"/pricing"}],"metricValues":[{"value":"12"},{"value":"10"},{"value":"2.5"}]}]}`))
		case strings.HasSuffix(r.URL.Path, "/accountSummaries"):
			_, _ = w.Write([]byte(`{"accountSummaries":[{"displayName":"Koala","propertySummaries":[{"property":"properties/42","displayName":"koala.com"}]}]}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	rows, err := c.AnalyticsDay(context.Background(), "properties/42", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	if r := rows[0]; r.Date != "2026-09-01" || r.Sessions != 12 || r.KeyEvents != 2.5 || r.Landing != "/pricing" {
		t.Errorf("row = %+v", r)
	}
	props, err := c.Properties(context.Background())
	if err != nil || len(props) != 1 || props[0].ID != "properties/42" {
		t.Errorf("properties = %+v, %v", props, err)
	}
}

func TestAssistant(t *testing.T) {
	for source, want := range map[string]string{
		"chatgpt.com":                "ChatGPT",
		"https://chatgpt.com/":       "ChatGPT",
		"www.perplexity.ai":          "Perplexity",
		"gemini.google.com":          "Gemini",
		"google":                     "",
		"notchatgpt.com":             "",
		"copilot.microsoft.com/chat": "Copilot",
	} {
		if got := Assistant(source); got != want {
			t.Errorf("Assistant(%q) = %q, want %q", source, got, want)
		}
	}
}
