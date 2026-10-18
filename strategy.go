package ethan

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
)

// Strategy decides which candidate plans of a task to execute and which
// result to adopt.
type Strategy interface {
	// Explore returns the plan whose output becomes the task's output, or an
	// error if no plan succeeded.
	Explore(ctx context.Context, x Exploration) (*Plan, error)
}

// Exploration is the handle a Strategy uses to act on one task's plans.
// Its methods are safe to call concurrently for different plans.
type Exploration interface {
	Plans() []*Plan
	// Depth is the depth of the task whose plans these are; the root is 0.
	Depth() int
	// ScorePlan evaluates p before execution and records it on p.
	// It returns nil if the evaluation failed.
	ScorePlan(ctx context.Context, p *Plan) *Score
	// Execute solves p's subtasks and synthesizes their results into
	// p.Output. It returns nil when p produced an output.
	Execute(ctx context.Context, p *Plan) error
	// ScoreResult evaluates p's output and records it on p.
	// It returns nil if the evaluation failed.
	ScoreResult(ctx context.Context, p *Plan) *Score
	// Prune marks p as abandoned without execution.
	Prune(p *Plan)
}

// BeamStrategy is the default Strategy. When there are more plans than the
// width, it scores them all and ranks them. It then executes the plans in
// batches of the width, in parallel, and adopts the best-scoring result of
// the first batch in which any plan succeeds. Later plans are pruned.
//
// Every plan run in parallel multiplies the cost of the subtree below it,
// so by default only the root compares plans side by side; deeper tasks run
// one plan at a time and keep the others for backtracking.
type BeamStrategy struct {
	// Width is the number of plans executed in parallel at the root. Default 2.
	Width int
	// DeepWidth is the number executed in parallel below the root. Default 1.
	DeepWidth int
}

// Explore implements Strategy.
func (s BeamStrategy) Explore(ctx context.Context, x Exploration) (*Plan, error) {
	width := cmp.Or(max(s.Width, 0), 2)
	if x.Depth() > 0 {
		width = cmp.Or(max(s.DeepWidth, 0), 1)
	}
	plans := slices.Clone(x.Plans())
	if len(plans) > width {
		scores := parallel(plans, func(p *Plan) *Score { return x.ScorePlan(ctx, p) })
		ranked := make(map[*Plan]float64, len(plans))
		for i, p := range plans {
			ranked[p] = value(scores[i])
		}
		slices.SortStableFunc(plans, func(a, b *Plan) int { return cmp.Compare(ranked[b], ranked[a]) })
	}

	var errs []error
	for start := 0; start < len(plans); start += width {
		batch := plans[start:min(start+width, len(plans))]
		results := parallel(batch, func(p *Plan) error { return x.Execute(ctx, p) })
		var ok []*Plan
		for i, err := range results {
			if err == nil {
				ok = append(ok, batch[i])
			} else {
				errs = append(errs, err)
			}
		}
		if len(ok) > 0 {
			for _, p := range plans[start+len(batch):] {
				x.Prune(p)
			}
			return best(ctx, x, ok), nil
		}
		if ctx.Err() != nil {
			// The run was canceled or exceeded a limit; backtracking is pointless.
			for _, p := range plans[start+len(batch):] {
				x.Prune(p)
			}
			break
		}
	}
	return nil, errors.Join(errs...)
}

// best returns the highest-scoring of the succeeded plans, scoring results
// only when there is a choice to make.
func best(ctx context.Context, x Exploration, ok []*Plan) *Plan {
	if len(ok) == 1 {
		return ok[0]
	}
	scores := parallel(ok, func(p *Plan) *Score { return x.ScoreResult(ctx, p) })
	bi := 0
	for i := range ok {
		if value(scores[i]) > value(scores[bi]) {
			bi = i
		}
	}
	return ok[bi]
}

func value(s *Score) float64 {
	if s == nil {
		return 0
	}
	return s.Value
}

// parallel applies f to every item concurrently and returns the results in order.
func parallel[T, R any](items []T, f func(T) R) []R {
	out := make([]R, len(items))
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Go(func() { out[i] = f(it) })
	}
	wg.Wait()
	return out
}
