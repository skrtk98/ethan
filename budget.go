package ethan

import (
	"encoding/json"
	"errors"
)

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
	// Each model call reserves an estimate of its tokens before it starts
	// and is refused with ErrBudgetExhausted if the reservation does not fit.
	// The Run then returns the best result it can assemble. Zero means
	// unlimited.
	MaxTokens int
	// MaxConcurrency is the number of model calls in flight at once. Default 4.
	MaxConcurrency int
}

// estimateTokens is the default upper estimate of a request's tokens: about
// one token per three bytes of input (generous for English, about right for
// Japanese) plus the output limit.
func estimateTokens(req Request) int {
	n := len(req.System)
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.InputSchema)
	}
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text)
			switch {
			case p.ToolCall != nil:
				n += len(p.ToolCall.Input)
			case p.ToolResult != nil:
				n += len(p.ToolResult.Content)
			case p.Raw != nil && p.Text == "":
				if b, err := json.Marshal(p.Raw); err == nil {
					n += len(b)
				}
			}
		}
	}
	return n/3 + req.MaxTokens
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
