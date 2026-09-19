package budget

import (
	"sync"
	"time"
)

// Reserver is anything a paid client can reserve spend from.
type Reserver interface {
	Reserve(n int) (release func(actual float64), err error)
}

// Daily is a process-wide cap that resets at UTC midnight, for long-running workers
// that pay for external calls on our own account.
type Daily struct {
	LimitUSD    float64
	EstimateUSD float64
	Now         func() time.Time // for tests; defaults to time.Now

	mu  sync.Mutex
	day string
	cur *Budget
}

// Today returns the budget for the current UTC day.
func (d *Daily) Today() *Budget {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	day := now().UTC().Format(time.DateOnly)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cur == nil || d.day != day {
		d.day, d.cur = day, New(d.LimitUSD, d.EstimateUSD)
	}
	return d.cur
}

// Reserve implements Reserver against today's budget.
func (d *Daily) Reserve(n int) (func(float64), error) { return d.Today().Reserve(n) }
