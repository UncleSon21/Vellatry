package config

import (
	"bufio"
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

// clean unsets every variable these tests depend on, so the machine running them
// cannot change the outcome.
func clean(t *testing.T) {
	t.Helper()
	for _, k := range []string{"VELLATRY_ENV", "DATABASE_URL", "VELLATRY_DEV_AUTH", "VELLATRY_SECRET_KEY", "APP_URL", "HUB_URL",
		"ALLOWED_ORIGINS", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_REDIRECT_URL", "BIGQUERY_PROJECT",
		"ASANA_CLIENT_ID", "ASANA_CLIENT_SECRET", "ASANA_REDIRECT_URL", "CLERK_ISSUER", "BIGQUERY_CREDENTIALS_JSON"} {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "postgres://vellatry_app@db/vellatry")
}

func production(t *testing.T) {
	t.Helper()
	clean(t)
	t.Setenv("VELLATRY_ENV", "production")
	t.Setenv("VELLATRY_SECRET_KEY", "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=")
	t.Setenv("APP_URL", "https://app.vellatry.example")
	t.Setenv("HUB_URL", "https://api.vellatry.example")
	t.Setenv("ALLOWED_ORIGINS", "https://app.vellatry.example")
	t.Setenv("CLERK_ISSUER", "https://clerk.vellatry.example")
}

func TestDevelopmentIsTheDefault(t *testing.T) {
	clean(t)
	t.Setenv("VELLATRY_DEV_AUTH", "1")
	c, err := FromEnv()
	if err != nil || c.Production() || !c.DevAuth || c.AppURL != "http://localhost:3000" {
		t.Fatalf("development: %+v, %v", c, err)
	}
}

func TestProductionAcceptsAPublicSetup(t *testing.T) {
	production(t)
	if c, err := FromEnv(); err != nil || !c.Production() {
		t.Fatalf("production: %v", err)
	}
}

func TestProductionRefusesDevelopmentSettings(t *testing.T) {
	clean(t)
	t.Setenv("VELLATRY_ENV", "production")
	t.Setenv("VELLATRY_DEV_AUTH", "1")
	_, err := FromEnv()
	if err == nil {
		t.Fatal("production with header sign-in and localhost URLs was accepted")
	}
	// Every problem is reported at once.
	for _, want := range []string{"VELLATRY_DEV_AUTH", "VELLATRY_SECRET_KEY", `APP_URL must be a public https URL, got "http://localhost:3000"`,
		"HUB_URL", "ALLOWED_ORIGINS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestProductionGoogleNeedsTheWarehouse(t *testing.T) {
	production(t)
	t.Setenv("GOOGLE_CLIENT_ID", "id")
	t.Setenv("GOOGLE_CLIENT_SECRET", "secret")
	t.Setenv("GOOGLE_REDIRECT_URL", "https://api.vellatry.example/oauth/google/callback")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "BIGQUERY_PROJECT") {
		t.Fatalf("Google on without BigQuery: %v", err)
	}
	t.Setenv("BIGQUERY_PROJECT", "vellatry")
	if _, err := FromEnv(); err != nil {
		t.Fatalf("Google with BigQuery: %v", err)
	}
	t.Setenv("GOOGLE_REDIRECT_URL", "http://localhost:8080/oauth/google/callback")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "GOOGLE_REDIRECT_URL") {
		t.Fatalf("a localhost redirect in production: %v", err)
	}
}

func TestUnknownEnvironment(t *testing.T) {
	clean(t)
	t.Setenv("VELLATRY_ENV", "prod")
	if _, err := FromEnv(); err == nil {
		t.Fatal(`VELLATRY_ENV "prod" was accepted`)
	}
}

// The deployed settings are checked here, not discovered on the first release: the
// [env] block of fly.toml plus the secrets docs/deploy.md asks for must pass.
func TestFlyConfigPassesProductionChecks(t *testing.T) {
	production(t)
	f, err := os.Open("../../fly.toml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	inEnv, seen := false, 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inEnv = line == "[env]"
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !inEnv || !ok || strings.HasPrefix(line, "#") {
			continue
		}
		t.Setenv(strings.TrimSpace(key), strings.Trim(strings.TrimSpace(value), `"`))
		seen++
	}
	if seen == 0 {
		t.Fatal("fly.toml has no [env] settings")
	}
	c, err := FromEnv()
	if err != nil {
		t.Fatalf("fly.toml's [env] fails the production checks: %v", err)
	}
	if !c.Production() {
		t.Error("fly.toml does not set VELLATRY_ENV=production")
	}
}

func TestBigQueryServiceAccountKey(t *testing.T) {
	const key = `{"type":"service_account","project_id":"vellatry-au","client_email":"warehouse@vellatry-au.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nnot-a-real-key\n-----END PRIVATE KEY-----\n"}`
	for name, value := range map[string]string{"json": key, "base64": base64.StdEncoding.EncodeToString([]byte(key))} {
		clean(t)
		t.Setenv("BIGQUERY_CREDENTIALS_JSON", value)
		t.Setenv("BIGQUERY_PROJECT", "")
		c, err := FromEnv()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(c.BigQueryCredentials) != key || c.BigQueryProject != "vellatry-au" {
			t.Errorf("%s: project %q, %d key bytes; the project comes from the key", name, c.BigQueryProject, len(c.BigQueryCredentials))
		}
	}

	// A project set explicitly wins over the key's.
	clean(t)
	t.Setenv("BIGQUERY_CREDENTIALS_JSON", key)
	t.Setenv("BIGQUERY_PROJECT", "other-project")
	if c, err := FromEnv(); err != nil || c.BigQueryProject != "other-project" {
		t.Errorf("explicit project: %q, %v", c.BigQueryProject, err)
	}

	// Anything but a service-account key is refused, and the error never repeats the secret.
	for name, value := range map[string]string{
		"a user credential": `{"type":"authorized_user","client_id":"id","client_secret":"hunter2-secret","refresh_token":"rt"}`,
		"not json":          "hunter2-secret-not-base64!",
		"no private key":    `{"type":"service_account","client_email":"x@y.iam.gserviceaccount.com","note":"hunter2-secret"}`,
	} {
		clean(t)
		t.Setenv("BIGQUERY_CREDENTIALS_JSON", value)
		_, err := FromEnv()
		if err == nil {
			t.Errorf("%s was accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: the error repeats the secret: %v", name, err)
		}
	}
}
