package ethan

import (
	"errors"
	"fmt"
)

// ErrLimitExceeded is returned when a Run goes beyond one of its Limits. The
// whole Run is canceled at that point; the journal, if any, lets it resume.
var ErrLimitExceeded = errors.New("ethan: limit exceeded")

var (
	// ErrTaskLimit is returned when a task would start beyond Limits.MaxTasks.
	ErrTaskLimit = fmt.Errorf("%w: MaxTasks", ErrLimitExceeded)
	// ErrTokenLimit is returned when the tokens spent go beyond Limits.MaxTokens.
	ErrTokenLimit = fmt.Errorf("%w: MaxTokens", ErrLimitExceeded)
)

// Limits bounds the work of a Run. The engine checks them mechanically and
// does not plan around them: how to spend a budget is the caller's concern.
// Zero fields take their defaults.
type Limits struct {
	// MaxDepth is the depth at which tasks are no longer decomposed but
	// executed directly. Default 2.
	MaxDepth int
	// MaxTasks is the number of tasks, leaves included, a Run may start.
	// Starting one more cancels the Run with ErrTaskLimit. Default 40.
	MaxTasks int
	// MaxTokens is the total of input and output tokens a Run may spend.
	// A model call does not start once the total has reached it, and a call
	// that carries the total past it cancels the Run with ErrTokenLimit.
	// Calls in flight at that moment are canceled and their usage, which the
	// provider does not report, is not counted, so the provider may bill
	// somewhat more than MaxTokens. Zero means unlimited.
	MaxTokens int
	// MaxConcurrency is the number of model calls in flight at once. Default 4.
	MaxConcurrency int
}

func (l Limits) withDefaults() Limits {
	if l.MaxDepth <= 0 {
		l.MaxDepth = 2
	}
	if l.MaxTasks <= 0 {
		l.MaxTasks = 40
	}
	if l.MaxConcurrency <= 0 {
		l.MaxConcurrency = 4
	}
	return l
}
