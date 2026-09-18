// Package budget caps spend on paid external calls. Every paid call reserves budget
// before it is sent and records its actual cost afterwards; a call that would exceed
// the limit is refused before anything is sent (fail closed).
package budget

import (
	"errors"
	"fmt"
	"sync"
)

// ErrExceeded is returned before a paid call that would exceed the budget.
var ErrExceeded = errors.New("budget exceeded")

// Budget caps total spend in USD. It is safe for concurrent use.
type Budget struct {
	mu       sync.Mutex
	limit    float64
	spent    float64
	pending  float64
	estimate float64 // per-unit reservation; raised to the highest cost observed
}

// New returns a budget of limitUSD that reserves initialEstimate per unit until real
// costs are observed.
func New(limitUSD, initialEstimate float64) *Budget {
	return &Budget{limit: limitUSD, estimate: initialEstimate}
}

// Reserve holds budget for n paid units. The returned release must be called with the
// actual cost charged (0 if the call failed); calls after the first are ignored.
func (b *Budget) Reserve(n int) (release func(actual float64), err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	hold := b.estimate * float64(n)
	if b.spent+b.pending+hold > b.limit {
		return nil, fmt.Errorf("%w: spent $%.4f, pending $%.4f, limit $%.2f", ErrExceeded, b.spent, b.pending, b.limit)
	}
	b.pending += hold
	var once sync.Once
	return func(actual float64) {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.pending -= hold
			b.spent += actual
			if n > 0 && actual/float64(n) > b.estimate {
				b.estimate = actual / float64(n)
			}
		})
	}, nil
}

// Spent returns the total charged so far.
func (b *Budget) Spent() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}
