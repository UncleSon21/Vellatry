package api

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Identity is who signed in, as the auth provider knows them.
type Identity struct {
	ExternalID string
	Email      string
}

// Verifier authenticates a request.
type Verifier interface {
	Verify(r *http.Request) (Identity, error)
}

// ErrUnauthenticated means the request carries no valid credentials.
var ErrUnauthenticated = errors.New("unauthenticated")

// DevVerifier trusts an X-Dev-Email header. Local development only: it is wired in
// solely when VELLATRY_DEV_AUTH=1.
type DevVerifier struct{}

func (DevVerifier) Verify(r *http.Request) (Identity, error) {
	email := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Dev-Email")))
	if email == "" || !strings.Contains(email, "@") {
		return Identity{}, ErrUnauthenticated
	}
	return Identity{ExternalID: "dev:" + email, Email: email}, nil
}

// ClerkVerifier checks Clerk session tokens (RS256 JWTs) against Clerk's JWKS.
type ClerkVerifier struct {
	Issuer            string
	JWKSURL           string
	AuthorizedParties []string // allowed "azp" origins; empty accepts any
	HTTP              *http.Client

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func (v *ClerkVerifier) Verify(r *http.Request) (Identity, error) {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if raw == "" || raw == r.Header.Get("Authorization") {
		if c, err := r.Cookie("__session"); err == nil {
			raw = c.Value
		} else {
			return Identity{}, ErrUnauthenticated
		}
	}
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, v.key,
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(v.Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(5*time.Second),
	)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	if len(v.AuthorizedParties) > 0 {
		azp, _ := claims["azp"].(string)
		if !slices.Contains(v.AuthorizedParties, azp) {
			return Identity{}, fmt.Errorf("%w: unauthorised party %q", ErrUnauthenticated, azp)
		}
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return Identity{}, fmt.Errorf("%w: no subject", ErrUnauthenticated)
	}
	email, _ := claims["email"].(string)
	return Identity{ExternalID: sub, Email: strings.ToLower(email)}, nil
}

func (v *ClerkVerifier) key(t *jwt.Token) (any, error) {
	kid, _ := t.Header["kid"].(string)
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	// Unknown key: refresh (keys rotate), at most once a minute.
	if time.Since(v.fetched) < time.Minute && v.keys != nil {
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	keys, err := fetchJWKS(v.HTTP, v.JWKSURL)
	v.fetched = time.Now()
	if err != nil {
		return nil, err
	}
	v.keys = keys
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown key id %q", kid)
}

func fetchJWKS(c *http.Client, url string) (map[string]*rsa.PublicKey, error) {
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: http %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(keys) == 0 {
		return nil, errors.New("jwks: no RSA keys")
	}
	return keys, nil
}
