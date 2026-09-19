// Package config reads the service's settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is every setting the roles need.
type Config struct {
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

	// BigQuery warehouse for raw Search Console and GA4 facts.
	BigQueryProject     string
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
		BigQueryProject:        os.Getenv("BIGQUERY_PROJECT"),
		BigQueryDataset:        env("BIGQUERY_DATASET", "vellatry_raw"),
		BigQueryLocation:       env("BIGQUERY_LOCATION", "australia-southeast1"),
	}
	var err error
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
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is not set")
	}
	if c.ClerkIssuer != "" && c.ClerkJWKSURL == "" {
		c.ClerkJWKSURL = c.ClerkIssuer + "/.well-known/jwks.json"
	}
	return c, nil
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
