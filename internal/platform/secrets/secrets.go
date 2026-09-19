// Package secrets encrypts credentials at rest and signs short-lived tokens.
//
// Sealed values are bound to the org they belong to (additional authenticated data),
// so a ciphertext copied into another tenant's row fails to open.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Box seals and opens secrets with one 32-byte key.
type Box struct {
	aead    cipher.AEAD
	signKey []byte
}

var (
	ErrOpen    = errors.New("secrets: cannot open sealed value")
	ErrExpired = errors.New("secrets: token expired")
	ErrInvalid = errors.New("secrets: invalid token")
)

// NewBox builds a Box from a base64 (standard or URL) 32-byte key.
func NewBox(encodedKey string) (*Box, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		key, err = base64.RawURLEncoding.DecodeString(encodedKey)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("secrets: key must be 32 bytes, base64-encoded (openssl rand -base64 32)")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// Derive a separate signing key so the encryption key is never used for MACs.
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("vellatry-signing-key"))
	return &Box{aead: aead, signKey: mac.Sum(nil)}, nil
}

// Seal encrypts plaintext for orgID.
func (b *Box) Seal(orgID string, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, []byte(orgID)), nil
}

// Open decrypts a value sealed for orgID.
func (b *Box) Open(orgID string, sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrOpen
	}
	out, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(orgID))
	if err != nil {
		return nil, ErrOpen
	}
	return out, nil
}

// Sign returns a URL-safe token carrying claims until expiry.
func (b *Box) Sign(claims any, ttl time.Duration, now time.Time) (string, error) {
	body, err := json.Marshal(struct {
		C   any   `json:"c"`
		Exp int64 `json:"exp"`
	}{claims, now.Add(ttl).Unix()})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	return payload + "." + b.mac(payload), nil
}

// Verify checks a token from Sign and decodes its claims into v.
func (b *Box) Verify(token string, v any, now time.Time) error {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(b.mac(payload))) {
		return ErrInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return ErrInvalid
	}
	var wrapper struct {
		C   json.RawMessage `json:"c"`
		Exp int64           `json:"exp"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return ErrInvalid
	}
	if now.Unix() > wrapper.Exp {
		return ErrExpired
	}
	if err := json.Unmarshal(wrapper.C, v); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

func (b *Box) mac(payload string) string {
	m := hmac.New(sha256.New, b.signKey)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
