package ethan

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrBudgetExhausted is returned when a Run's budget does not allow more
// work: a model call that does not fit in the token budget, or a task beyond
// the node limit.
var ErrBudgetExhausted = errors.New("ethan: budget exhausted")

// ErrNodeLimit is returned for a task that would exceed Budget.MaxNodes.
var ErrNodeLimit = fmt.Errorf("%w: node limit reached", ErrBudgetExhausted)

// Budget bounds the work of a Run. Zero fields take their defaults.
type Budget struct {
	// MaxDepth is the depth at which tasks are no longer decomposed. Default 2.
	MaxDepth int
	// MaxNodes is the number of tasks, leaves included, a Run may start.
	// Subtasks beyond it are not started and their plan becomes partial.
	// Default 40.
	MaxNodes int
	// MaxTokens is the total of input and output tokens a Run may spend.
	// Each model call reserves an estimate of its tokens before it starts
	// and is refused with ErrBudgetExhausted if the reservation does not fit.
	// The Run then returns the best result it can assemble. Zero means
	// unlimited.
	//
	// The cap is only as tight as the estimates: a call that uses more than
	// it reserved (an unusual tokenization, server-side tool results larger
	// than the adapter allowed for) can carry the total past MaxTokens, after
	// which no further call starts.
	MaxTokens int
	// SynthesisReserve is the fraction of MaxTokens held back so that a Run
	// that runs out of budget still integrates what it found instead of only
	// joining it: when the root's chosen plan could not be synthesized
	// within the rest, it is synthesized again on the reserve. Every other
	// call counts the budget as exhausted once the rest is used. A root
	// executed directly releases the reserve. Default 0.15; negative for
	// none.
	SynthesisReserve float64
	// MaxConcurrency is the number of model calls in flight at once. Default 4.
	MaxConcurrency int
}

// estimateTokens is the default estimate of a request's tokens: one token
// per two bytes of everything sent (about double for English, generous for
// Japanese), a small overhead per message, and the output limit.
func estimateTokens(req Request) int {
	n := len(req.System)
	if req.Output != nil {
		n += len(req.Output.Name) + len(req.Output.Description) + len(req.Output.Schema)
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.InputSchema)
	}
	for _, m := range req.Messages {
		n += 32
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
	return n/2 + req.MaxTokens
}

func (b Budget) withDefaults() Budget {
	if b.MaxDepth <= 0 {
		b.MaxDepth = 2
	}
	if b.MaxNodes <= 0 {
		b.MaxNodes = 40
	}
	if b.SynthesisReserve == 0 {
		b.SynthesisReserve = 0.15
	}
	if b.MaxConcurrency <= 0 {
		b.MaxConcurrency = 4
	}
	return b
}

// limits returns the tokens open to every call and those open to the
// root's synthesis, which may also use the reserve. Zero means unlimited.
// A reserve of the whole budget leaves a single token to the other calls,
// never an unlimited allowance.
func (b Budget) limits() (general, synthesis int) {
	if b.MaxTokens <= 0 {
		return 0, 0
	}
	held := int(float64(b.MaxTokens) * min(max(b.SynthesisReserve, 0), 1))
	return max(b.MaxTokens-held, 1), b.MaxTokens
}
