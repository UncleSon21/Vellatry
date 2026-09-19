// Package google calls Search Console and GA4 for the worker role. It uses plain JSON
// over an OAuth-authenticated HTTP client, with read-only scopes.
package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

var (
	// ErrAuth means the grant no longer works (revoked, expired, or permission removed).
	// The connection is broken until the customer reconnects; retrying cannot help.
	ErrAuth = errors.New("google: access revoked or insufficient")
	// ErrTransient is rate limiting or a server error; retry later.
	ErrTransient = errors.New("google: temporary failure")
)

// Endpoints, overridable in tests.
type Endpoints struct {
	SearchConsole  string // https://searchconsole.googleapis.com/webmasters/v3
	AnalyticsData  string // https://analyticsdata.googleapis.com/v1beta
	AnalyticsAdmin string // https://analyticsadmin.googleapis.com/v1beta
}

// DefaultEndpoints are Google's production hosts.
var DefaultEndpoints = Endpoints{
	SearchConsole:  "https://searchconsole.googleapis.com/webmasters/v3",
	AnalyticsData:  "https://analyticsdata.googleapis.com/v1beta",
	AnalyticsAdmin: "https://analyticsadmin.googleapis.com/v1beta",
}

// Client calls Google on behalf of one grant.
type Client struct {
	HTTP      *http.Client
	Endpoints Endpoints
}

// NewClient returns a client for a stored refresh token.
func NewClient(ctx context.Context, cfg *oauth2.Config, refreshToken string) *Client {
	ts := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	hc := oauth2.NewClient(ctx, ts)
	hc.Timeout = 60 * time.Second
	return &Client{HTTP: hc, Endpoints: DefaultEndpoints}
}

func (c *Client) do(ctx context.Context, method, rawURL string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return fmt.Errorf("%w: %v", ErrAuth, err) // invalid_grant: the refresh token is dead
		}
		return fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: http %d: %s", ErrAuth, resp.StatusCode, snippet(raw))
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("%w: http %d: %s", ErrTransient, resp.StatusCode, snippet(raw))
	case resp.StatusCode >= 400:
		return fmt.Errorf("google: http %d: %s", resp.StatusCode, snippet(raw))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// ---- Search Console ----------------------------------------------------------------

// Site is a Search Console property.
type Site struct {
	URL        string `json:"siteUrl"`
	Permission string `json:"permissionLevel"`
}

// Sites lists the properties the grant can read.
func (c *Client) Sites(ctx context.Context) ([]Site, error) {
	var out struct {
		SiteEntry []Site `json:"siteEntry"`
	}
	if err := c.do(ctx, http.MethodGet, c.Endpoints.SearchConsole+"/sites", nil, &out); err != nil {
		return nil, err
	}
	var usable []Site
	for _, s := range out.SiteEntry {
		if s.Permission != "siteUnverifiedUser" {
			usable = append(usable, s)
		}
	}
	return usable, nil
}

// SearchRow is one row of Search Console data for a day.
type SearchRow struct {
	Date        string  `json:"date"`
	Query       string  `json:"query"`
	Page        string  `json:"page"`
	Country     string  `json:"country"`
	Clicks      int64   `json:"clicks"`
	Impressions int64   `json:"impressions"`
	Position    float64 `json:"position"`
}

// DayTotal is Search Console's total for a day, including anonymised queries.
type DayTotal struct {
	Date        string
	Clicks      int64
	Impressions int64
	Position    float64
}

type saQuery struct {
	StartDate  string   `json:"startDate"`
	EndDate    string   `json:"endDate"`
	Dimensions []string `json:"dimensions"`
	Type       string   `json:"type"`
	DataState  string   `json:"dataState"`
	RowLimit   int      `json:"rowLimit"`
	StartRow   int      `json:"startRow"`
}

type saResponse struct {
	Rows []struct {
		Keys        []string `json:"keys"`
		Clicks      float64  `json:"clicks"`
		Impressions float64  `json:"impressions"`
		Position    float64  `json:"position"`
	} `json:"rows"`
}

// PageSize is Search Console's maximum rows per request.
const PageSize = 25_000

func (c *Client) query(ctx context.Context, site string, q saQuery) (saResponse, error) {
	var out saResponse
	// The property is one path segment and must be fully encoded (":" and "/" included):
	// "sc-domain:koala.com" -> "sc-domain%3Akoala.com".
	u := c.Endpoints.SearchConsole + "/sites/" + strings.ReplaceAll(url.QueryEscape(site), "+", "%20") + "/searchAnalytics/query"
	err := c.do(ctx, http.MethodPost, u, q, &out)
	return out, err
}

// DayRows returns every date x query x page x country row for one day of web search,
// following pagination until a short page.
func (c *Client) DayRows(ctx context.Context, site string, day time.Time) ([]SearchRow, error) {
	d := day.Format(time.DateOnly)
	var rows []SearchRow
	for start := 0; ; start += PageSize {
		resp, err := c.query(ctx, site, saQuery{
			StartDate: d, EndDate: d, Dimensions: []string{"date", "query", "page", "country"},
			Type: "web", DataState: "all", RowLimit: PageSize, StartRow: start,
		})
		if err != nil {
			return nil, err
		}
		for _, r := range resp.Rows {
			if len(r.Keys) != 4 {
				continue
			}
			rows = append(rows, SearchRow{Date: r.Keys[0], Query: r.Keys[1], Page: r.Keys[2], Country: r.Keys[3],
				Clicks: int64(r.Clicks), Impressions: int64(r.Impressions), Position: r.Position})
		}
		if len(resp.Rows) < PageSize {
			return rows, nil
		}
	}
}

// DayTotals returns exact per-day totals for [from, to].
func (c *Client) DayTotals(ctx context.Context, site string, from, to time.Time) ([]DayTotal, error) {
	resp, err := c.query(ctx, site, saQuery{
		StartDate: from.Format(time.DateOnly), EndDate: to.Format(time.DateOnly),
		Dimensions: []string{"date"}, Type: "web", DataState: "all", RowLimit: PageSize,
	})
	if err != nil {
		return nil, err
	}
	out := make([]DayTotal, 0, len(resp.Rows))
	for _, r := range resp.Rows {
		if len(r.Keys) == 1 {
			out = append(out, DayTotal{Date: r.Keys[0], Clicks: int64(r.Clicks), Impressions: int64(r.Impressions), Position: r.Position})
		}
	}
	return out, nil
}

// ---- GA4 ---------------------------------------------------------------------------

// Property is a GA4 property.
type Property struct {
	ID      string `json:"id"` // "properties/123"
	Name    string `json:"name"`
	Account string `json:"account"`
}

// Properties lists the GA4 properties the grant can read.
func (c *Client) Properties(ctx context.Context) ([]Property, error) {
	var out []Property
	next := ""
	for {
		u := c.Endpoints.AnalyticsAdmin + "/accountSummaries?pageSize=200"
		if next != "" {
			u += "&pageToken=" + url.QueryEscape(next)
		}
		var resp struct {
			AccountSummaries []struct {
				DisplayName       string `json:"displayName"`
				PropertySummaries []struct {
					Property    string `json:"property"`
					DisplayName string `json:"displayName"`
				} `json:"propertySummaries"`
			} `json:"accountSummaries"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.do(ctx, http.MethodGet, u, nil, &resp); err != nil {
			return nil, err
		}
		for _, a := range resp.AccountSummaries {
			for _, p := range a.PropertySummaries {
				out = append(out, Property{ID: p.Property, Name: p.DisplayName, Account: a.DisplayName})
			}
		}
		if next = resp.NextPageToken; next == "" {
			return out, nil
		}
	}
}

// AnalyticsRow is one day x channel x source x landing page row.
type AnalyticsRow struct {
	Date      string  `json:"date"`
	Channel   string  `json:"channel"`
	Source    string  `json:"source"`
	Landing   string  `json:"landing_page"`
	Sessions  int64   `json:"sessions"`
	Users     int64   `json:"users"`
	KeyEvents float64 `json:"key_events"`
}

// AnalyticsDay returns every row for one day, following pagination.
func (c *Client) AnalyticsDay(ctx context.Context, property string, day time.Time) ([]AnalyticsRow, error) {
	d := day.Format(time.DateOnly)
	const limit = 100_000
	var out []AnalyticsRow
	for offset := 0; ; offset += limit {
		var resp struct {
			Rows []struct {
				DimensionValues []struct {
					Value string `json:"value"`
				} `json:"dimensionValues"`
				MetricValues []struct {
					Value string `json:"value"`
				} `json:"metricValues"`
			} `json:"rows"`
			RowCount int `json:"rowCount"`
		}
		body := map[string]any{
			"dateRanges": []map[string]string{{"startDate": d, "endDate": d}},
			"dimensions": []map[string]string{{"name": "date"}, {"name": "sessionDefaultChannelGroup"}, {"name": "sessionSource"}, {"name": "landingPage"}},
			"metrics":    []map[string]string{{"name": "sessions"}, {"name": "totalUsers"}, {"name": "keyEvents"}},
			"limit":      limit,
			"offset":     offset,
		}
		if err := c.do(ctx, http.MethodPost, c.Endpoints.AnalyticsData+"/"+property+":runReport", body, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Rows {
			if len(r.DimensionValues) != 4 || len(r.MetricValues) != 3 {
				continue
			}
			sessions, _ := strconv.ParseInt(r.MetricValues[0].Value, 10, 64)
			users, _ := strconv.ParseInt(r.MetricValues[1].Value, 10, 64)
			key, _ := strconv.ParseFloat(r.MetricValues[2].Value, 64)
			out = append(out, AnalyticsRow{
				Date: gaDate(r.DimensionValues[0].Value), Channel: r.DimensionValues[1].Value,
				Source: r.DimensionValues[2].Value, Landing: r.DimensionValues[3].Value,
				Sessions: sessions, Users: users, KeyEvents: key,
			})
		}
		if offset+limit >= resp.RowCount {
			return out, nil
		}
	}
}

// gaDate turns GA4's 20260920 into 2026-09-20.
func gaDate(s string) string {
	if len(s) == 8 && !strings.Contains(s, "-") {
		return s[:4] + "-" + s[4:6] + "-" + s[6:]
	}
	return s
}

func snippet(b []byte) string {
	if len(b) > 300 {
		b = b[:300]
	}
	return string(b)
}

// RevokeURL is Google's token revocation endpoint.
var RevokeURL = "https://oauth2.googleapis.com/revoke"

// Revoke tells Google to invalidate a refresh token. Best effort: the stored token is
// wiped regardless.
func Revoke(ctx context.Context, hc *http.Client, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, RevokeURL, strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusBadRequest { // 400: already invalid
		return fmt.Errorf("google: revoke http %d", resp.StatusCode)
	}
	return nil
}
