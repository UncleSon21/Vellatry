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
}

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
	}
	var err error
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
