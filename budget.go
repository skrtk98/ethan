package ethan

import "errors"

// ErrBudgetExhausted is returned by model calls once the token budget of a
// Run has been spent.
var ErrBudgetExhausted = errors.New("ethan: token budget exhausted")

// Budget bounds the work of a Run. Zero fields take their defaults.
type Budget struct {
	// MaxDepth is the depth at which tasks are no longer decomposed. Default 2.
	MaxDepth int
	// MaxNodes is the number of solved tasks after which tasks are no longer
	// decomposed. It is a soft cap: plans already underway still finish.
	// Default 40.
	MaxNodes int
	// MaxTokens is the total of input and output tokens a Run may spend.
	// Once spent, model calls fail with ErrBudgetExhausted and the Run
	// returns the best result it can assemble. Zero means unlimited.
	MaxTokens int
	// MaxConcurrency is the number of model calls in flight at once. Default 4.
	MaxConcurrency int
}

func (b Budget) withDefaults() Budget {
	if b.MaxDepth <= 0 {
		b.MaxDepth = 2
	}
	if b.MaxNodes <= 0 {
		b.MaxNodes = 40
	}
	if b.MaxConcurrency <= 0 {
		b.MaxConcurrency = 4
	}
	return b
}
