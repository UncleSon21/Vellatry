package api

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func clerkFixture(t *testing.T) (*rsa.PrivateKey, *ClerkVerifier) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	return key, &ClerkVerifier{Issuer: "https://clerk.example", JWKSURL: srv.URL, AuthorizedParties: []string{"https://app.example"}}
}

func sign(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClerkVerifier(t *testing.T) {
	key, v := clerkFixture(t)
	good := jwt.MapClaims{"sub": "user_1", "iss": "https://clerk.example", "azp": "https://app.example", "exp": time.Now().Add(time.Minute).Unix(), "email": "A@Example.com"}
	req := func(tok string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return r
	}

	id, err := v.Verify(req(sign(t, key, "k1", good)))
	if err != nil || id.ExternalID != "user_1" || id.Email != "a@example.com" {
		t.Fatalf("valid token: %+v, %v", id, err)
	}

	bad := map[string]jwt.MapClaims{
		"expired":       with(good, "exp", time.Now().Add(-time.Hour).Unix()),
		"wrong issuer":  with(good, "iss", "https://evil.example"),
		"wrong party":   with(good, "azp", "https://evil.example"),
		"no expiry":     without(good, "exp"),
		"empty subject": with(good, "sub", ""),
	}
	for name, claims := range bad {
		if _, err := v.Verify(req(sign(t, key, "k1", claims))); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s: got %v, want ErrUnauthenticated", name, err)
		}
	}

	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	if _, err := v.Verify(req(sign(t, other, "k1", good))); err == nil {
		t.Error("token signed by another key was accepted")
	}
	if _, err := v.Verify(req(sign(t, key, "unknown", good))); err == nil {
		t.Error("unknown key id was accepted")
	}

	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, good)
	hs.Header["kid"] = "k1"
	hsTok, _ := hs.SignedString([]byte("secret"))
	if _, err := v.Verify(req(hsTok)); err == nil {
		t.Error("HS256 token was accepted (algorithm confusion)")
	}

	if _, err := v.Verify(httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("no credentials: %v", err)
	}
}

func TestDevVerifier(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := (DevVerifier{}).Verify(r); err == nil {
		t.Error("no header should fail")
	}
	r.Header.Set("X-Dev-Email", " Dev@Example.com ")
	id, err := (DevVerifier{}).Verify(r)
	if err != nil || id.Email != "dev@example.com" || id.ExternalID != "dev:dev@example.com" {
		t.Errorf("got %+v, %v", id, err)
	}
}

func with(c jwt.MapClaims, k string, v any) jwt.MapClaims {
	out := jwt.MapClaims{}
	for key, val := range c {
		out[key] = val
	}
	out[k] = v
	return out
}

func without(c jwt.MapClaims, k string) jwt.MapClaims {
	out := with(c, k, nil)
	delete(out, k)
	return out
}
