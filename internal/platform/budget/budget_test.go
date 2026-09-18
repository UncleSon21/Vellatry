package budget

import (
	"errors"
	"sync"
	"testing"
)

func TestReserveRecordsActualAndRaisesEstimate(t *testing.T) {
	b := New(0.01, 0.001)
	release, err := b.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	release(0.006)
	release(0.006) // ignored
	if got := b.Spent(); got != 0.006 {
		t.Errorf("spent = %v, want 0.006", got)
	}
	if _, err := b.Reserve(1); !errors.Is(err, ErrExceeded) {
		t.Errorf("want ErrExceeded after the estimate rose to 0.006, got %v", err)
	}
}

func TestPendingReservationsCountAgainstLimit(t *testing.T) {
	b := New(0.03, 0.01)
	for i := 0; i < 3; i++ {
		if _, err := b.Reserve(1); err != nil {
			t.Fatalf("reservation %d: %v", i, err)
		}
	}
	if _, err := b.Reserve(1); !errors.Is(err, ErrExceeded) {
		t.Errorf("fourth reservation should be refused while three are pending, got %v", err)
	}
}

func TestConcurrentReservationsNeverOverspend(t *testing.T) {
	b := New(1, 0.01)
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if release, err := b.Reserve(1); err == nil {
				release(0.01)
			}
		}()
	}
	wg.Wait()
	if got := b.Spent(); got > 1.0000001 {
		t.Errorf("spent %v exceeds limit 1", got)
	}
}
