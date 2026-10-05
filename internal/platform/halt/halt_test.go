package halt

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/UncleSon21/vellatry/internal/testdb"
)

func TestFatalSurvivesWrapping(t *testing.T) {
	inner := errors.New("402 payment required")
	err := fmt.Errorf("collecting answers: %w", Account("dataforseo", "the account is out of credit", inner))

	f, ok := IsFatal(err)
	if !ok {
		t.Fatal("a wrapped account error was not recognised; the halt would never trip")
	}
	if f.Service != "dataforseo" || f.Reason != "the account is out of credit" {
		t.Errorf("fatal = %+v", f)
	}
	if !errors.Is(err, inner) {
		t.Error("the original error was lost")
	}
	if _, ok := IsFatal(errors.New("timeout")); ok {
		t.Error("an ordinary error was treated as account-fatal")
	}
}

func TestTripKeepsTheFirstReason(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = Clear(ctx, pool) })

	if s, err := Current(ctx, pool); err != nil || s.Halted {
		t.Fatalf("a fresh database should not be halted: %+v %v", s, err)
	}

	first := &Fatal{Service: "dataforseo", Reason: "the account is out of credit"}
	if err := Trip(ctx, pool, first); err != nil {
		t.Fatal(err)
	}
	// Every other job in flight fails the same way a moment later.
	if err := Trip(ctx, pool, &Fatal{Service: "anthropic", Reason: "the API key was rejected"}); err != nil {
		t.Fatal(err)
	}

	got, err := Current(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Halted || got.Service != "dataforseo" || got.Reason != first.Reason {
		t.Errorf("state = %+v; the first reason explains the outage and should survive", got)
	}
	if got.Since.IsZero() {
		t.Error("a halt with no time cannot be reasoned about")
	}

	was, err := Clear(ctx, pool)
	if err != nil || !was.Halted || was.Service != "dataforseo" {
		t.Fatalf("clear returned %+v, %v", was, err)
	}
	if after, err := Current(ctx, pool); err != nil || after.Halted {
		t.Errorf("still halted after clearing: %+v %v", after, err)
	}
	if again, err := Clear(ctx, pool); err != nil || again.Halted {
		t.Errorf("clearing twice should be harmless: %+v %v", again, err)
	}
}
