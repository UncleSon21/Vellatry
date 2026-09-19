package secrets

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

const key = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes

func TestSealBindsToOrg(t *testing.T) {
	b, err := NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal("org-a", []byte("refresh-token"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("refresh-token")) {
		t.Fatal("plaintext visible in sealed value")
	}
	got, err := b.Open("org-a", sealed)
	if err != nil || string(got) != "refresh-token" {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := b.Open("org-b", sealed); !errors.Is(err, ErrOpen) {
		t.Errorf("sealed value opened for another org: %v", err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := b.Open("org-a", sealed); !errors.Is(err, ErrOpen) {
		t.Errorf("tampered value opened: %v", err)
	}
}

func TestSignVerify(t *testing.T) {
	b, _ := NewBox(key)
	now := time.Unix(1_800_000_000, 0)
	type state struct{ Org, User string }
	tok, err := b.Sign(state{"o", "u"}, 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	var got state
	if err := b.Verify(tok, &got, now.Add(time.Minute)); err != nil || got.Org != "o" {
		t.Fatalf("verify: %+v %v", got, err)
	}
	if err := b.Verify(tok, &got, now.Add(11*time.Minute)); !errors.Is(err, ErrExpired) {
		t.Errorf("expired token: %v", err)
	}
	if err := b.Verify(tok[:len(tok)-2]+"xx", &got, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("forged signature: %v", err)
	}
	other, _ := NewBox("ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=")
	if err := other.Verify(tok, &got, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("token from another key: %v", err)
	}
}

func TestNewBoxRejectsBadKeys(t *testing.T) {
	for _, k := range []string{"", "short", "bm90IDMyIGJ5dGVz"} {
		if _, err := NewBox(k); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
}
