// Package config reads the service's settings from environment variables.
package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Environments. Production refuses the settings that are only safe on a developer's
// machine; development is the default so a fresh checkout runs.
const (
	Development = "development"
	Production  = "production"
)

// Config is every setting the roles need.
type Config struct {
	Env         string // VELLATRY_ENV: development or production
	DatabaseURL string
	Port        string

	// Auth: Clerk in production; a header-based stand-in for local development only.
	DevAuth                bool
	ClerkIssuer            string
	ClerkJWKSURL           string
	ClerkAuthorizedParties []string
	AllowedOrigins         []string

	// DataForSEO, capped per UTC day on our own account.
	DataForSEOLogin    string
	DataForSEOPassword string
	DataForSEODailyUSD float64
	// KeywordsDailyUSD caps keyword research separately: its calls are cheap and many,
	// and must not eat the budget the Visibility engine needs.
	KeywordsDailyUSD float64

	// LLM gateway.
	AnthropicAPIKey   string
	CheapModel        string
	StrongModel       string
	LLMDailyUSDTenant float64
	// AllowJudge permits the per-item judge purpose (design-partner phase only).
	AllowJudge bool

	// Google connections (Search Console, GA4) and credential encryption.
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string // <api>/oauth/google/callback
	AppURL             string // the web app
	SecretKey          string // base64 32 bytes: openssl rand -base64 32

	// Asana (fixes and blindspots as tasks).
	AsanaClientID     string
	AsanaClientSecret string
	AsanaRedirectURL  string // <api>/oauth/asana/callback

	// Email (alerts, digest, report links, sign-in links) through Postmark.
	PostmarkToken  string
	PostmarkStream string
	EmailFrom      string

	// TimeZone schedules the weekly digest (Monday morning) for customers.
	TimeZone string

	// Reports hub: its public base URL (the api's, or a domain pointing at it) and the
	// Gotenberg server that renders PDFs.
	HubURL       string
	GotenbergURL string

	// EmbedURL is Vellatry's own embedding service (ml/embed), used by the worker to
	// tell when two topics mean the same thing. Empty disables the feature.
	EmbedURL string

	// BigQuery warehouse for raw Search Console and GA4 facts.
	BigQueryProject string
	// BigQueryCredentials is a service-account key (JSON) for hosts with no ambient
	// Google credentials, such as Fly. Empty means Application Default Credentials
	// (gcloud on a laptop, or a Google-hosted machine).
	BigQueryCredentials []byte
	BigQueryDataset     string
	BigQueryLocation    string
	BigQueryMaxBytes    int64
	SearchRetentionDays int
}

// GoogleConfigured reports whether Google connections can be offered.
func (c Config) GoogleConfigured() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != "" && c.GoogleRedirectURL != "" && c.SecretKey != ""
}

// AsanaConfigured reports whether Asana can be offered.
func (c Config) AsanaConfigured() bool {
	return c.AsanaClientID != "" && c.AsanaClientSecret != "" && c.AsanaRedirectURL != "" && c.SecretKey != ""
}

// EmailConfigured reports whether email can be sent.
func (c Config) EmailConfigured() bool { return c.PostmarkToken != "" && c.EmailFrom != "" }

// FromEnv reads the configuration. Only DATABASE_URL is required; features whose
// credentials are missing are disabled by the roles, not guessed.
func FromEnv() (Config, error) {
	c := Config{
		Env:                    env("VELLATRY_ENV", Development),
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		Port:                   env("PORT", "8080"),
		DevAuth:                os.Getenv("VELLATRY_DEV_AUTH") == "1",
		ClerkIssuer:            strings.TrimRight(os.Getenv("CLERK_ISSUER"), "/"),
		ClerkJWKSURL:           os.Getenv("CLERK_JWKS_URL"),
		ClerkAuthorizedParties: list(os.Getenv("CLERK_AUTHORIZED_PARTIES")),
		AllowedOrigins:         list(env("ALLOWED_ORIGINS", "http://localhost:3000")),
		DataForSEOLogin:        os.Getenv("DATAFORSEO_LOGIN"),
		DataForSEOPassword:     os.Getenv("DATAFORSEO_PASSWORD"),
		AnthropicAPIKey:        os.Getenv("ANTHROPIC_API_KEY"),
		CheapModel:             env("VELLATRY_CHEAP_MODEL", "claude-haiku-4-5"),
		StrongModel:            env("VELLATRY_STRONG_MODEL", "claude-opus-5"),
		AllowJudge:             os.Getenv("VELLATRY_ALLOW_JUDGE") == "1",
		GoogleClientID:         os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret:     os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURL:      os.Getenv("GOOGLE_REDIRECT_URL"),
		AppURL:                 strings.TrimRight(env("APP_URL", "http://localhost:3000"), "/"),
		SecretKey:              os.Getenv("VELLATRY_SECRET_KEY"),
		AsanaClientID:          os.Getenv("ASANA_CLIENT_ID"),
		AsanaClientSecret:      os.Getenv("ASANA_CLIENT_SECRET"),
		AsanaRedirectURL:       os.Getenv("ASANA_REDIRECT_URL"),
		PostmarkToken:          os.Getenv("POSTMARK_SERVER_TOKEN"),
		PostmarkStream:         env("POSTMARK_STREAM", "outbound"),
		EmailFrom:              os.Getenv("EMAIL_FROM"),
		TimeZone:               env("VELLATRY_TIME_ZONE", "Australia/Sydney"),
		HubURL:                 strings.TrimRight(env("HUB_URL", "http://localhost:8080"), "/"),
		GotenbergURL:           os.Getenv("GOTENBERG_URL"),
		EmbedURL:               strings.TrimRight(os.Getenv("EMBED_URL"), "/"),
		BigQueryProject:        os.Getenv("BIGQUERY_PROJECT"),
		BigQueryDataset:        env("BIGQUERY_DATASET", "vellatry_raw"),
		BigQueryLocation:       env("BIGQUERY_LOCATION", "australia-southeast1"),
	}
	var err error
	var keyProject string
	if c.BigQueryCredentials, keyProject, err = serviceAccountKey(os.Getenv("BIGQUERY_CREDENTIALS_JSON")); err != nil {
		return c, err
	}
	if c.BigQueryProject == "" {
		c.BigQueryProject = keyProject // the key names its project; setting it twice invites a mismatch
	}
	maxBytes, err := number("BIGQUERY_MAX_BYTES", 1<<30)
	if err != nil {
		return c, err
	}
	c.BigQueryMaxBytes = int64(maxBytes)
	retention, err := number("SEARCH_RETENTION_DAYS", 490)
	if err != nil {
		return c, err
	}
	c.SearchRetentionDays = int(retention)
	if c.DataForSEODailyUSD, err = number("DATAFORSEO_DAILY_USD", 5); err != nil {
		return c, err
	}
	if c.LLMDailyUSDTenant, err = number("LLM_DAILY_USD_PER_TENANT", 2); err != nil {
		return c, err
	}
	if c.KeywordsDailyUSD, err = number("DATAFORSEO_KEYWORDS_DAILY_USD", 3); err != nil {
		return c, err
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is not set")
	}
	if c.ClerkIssuer != "" && c.ClerkJWKSURL == "" {
		c.ClerkJWKSURL = c.ClerkIssuer + "/.well-known/jwks.json"
	}
	switch c.Env {
	case Development:
	case Production:
		if err := c.checkProduction(); err != nil {
			return c, err
		}
	default:
		return c, fmt.Errorf("VELLATRY_ENV must be %s or %s, got %q", Development, Production, c.Env)
	}
	return c, nil
}

// Production reports whether this is a production deployment.
func (c Config) Production() bool { return c.Env == Production }

// checkProduction refuses what is only safe on a developer's machine. It lists every
// problem at once, so a first deploy is one round of fixes rather than one per restart.
func (c Config) checkProduction() error {
	var problems []string
	if c.DevAuth {
		problems = append(problems, "VELLATRY_DEV_AUTH=1 trusts a request header for identity and is refused in production; set CLERK_ISSUER")
	}
	if c.SecretKey == "" {
		problems = append(problems, "VELLATRY_SECRET_KEY is required: it signs event-stream tickets and seals stored credentials (openssl rand -base64 32)")
	}
	urls := [][2]string{{"APP_URL", c.AppURL}, {"HUB_URL", c.HubURL}}
	if len(c.AllowedOrigins) == 0 {
		problems = append(problems, "ALLOWED_ORIGINS must list the dashboard's https origin")
	}
	for _, o := range c.AllowedOrigins {
		urls = append(urls, [2]string{"ALLOWED_ORIGINS", o})
	}
	if c.GoogleConfigured() {
		urls = append(urls, [2]string{"GOOGLE_REDIRECT_URL", c.GoogleRedirectURL})
		if c.BigQueryProject == "" {
			problems = append(problems, "BIGQUERY_PROJECT is required once Google connections are on: without it raw Search Console and GA4 facts are kept in memory and lost on restart")
		}
	}
	if c.AsanaConfigured() {
		urls = append(urls, [2]string{"ASANA_REDIRECT_URL", c.AsanaRedirectURL})
	}
	for _, u := range urls {
		if !publicHTTPS(u[1]) {
			problems = append(problems, fmt.Sprintf("%s must be a public https URL, got %q", u[0], u[1]))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("production configuration:\n  - " + strings.Join(problems, "\n  - "))
}

// serviceAccountKey reads a service-account key given as its JSON or as that JSON
// base64-encoded (easier to pass through a shell). Anything but a service-account key
// is refused: the client library would otherwise accept any credential type it is
// handed. Errors never repeat the value, which is a secret.
func serviceAccountKey(raw string) (key []byte, project string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", nil
	}
	key = []byte(raw)
	if !strings.HasPrefix(raw, "{") {
		if key, err = base64.StdEncoding.DecodeString(raw); err != nil {
			return nil, "", errors.New("BIGQUERY_CREDENTIALS_JSON must be a service-account key: its JSON, or that JSON base64-encoded")
		}
	}
	var k struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
	}
	if json.Unmarshal(key, &k) != nil {
		return nil, "", errors.New("BIGQUERY_CREDENTIALS_JSON is not valid JSON")
	}
	if k.Type != "service_account" || k.ClientEmail == "" || k.PrivateKey == "" {
		return nil, "", errors.New(`BIGQUERY_CREDENTIALS_JSON must be a service-account key (type "service_account", with client_email and private_key)`)
	}
	return key, k.ProjectID, nil
}

func publicHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return false
	}
	switch h := u.Hostname(); {
	case h == "localhost", strings.HasSuffix(h, ".localhost"), h == "127.0.0.1", h == "::1", h == "0.0.0.0":
		return false
	}
	return true
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func number(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%s must be a non-negative number, got %q", key, v)
	}
	return f, nil
}

func list(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
