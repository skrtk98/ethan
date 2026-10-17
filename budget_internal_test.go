package ethan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEstimateCoversEverythingSent(t *testing.T) {
	schema := json.RawMessage(`{"description":"` + strings.Repeat("x", 3000) + `"}`)
	base := Request{System: "s", Messages: []Message{UserText("hi")}, MaxTokens: 100}
	with := base
	with.Output = &OutputSchema{Name: "o", Schema: schema}

	if got := estimateTokens(with) - estimateTokens(base); got < 1500 {
		t.Errorf("a 3000-byte schema adds %d tokens to the estimate, want at least 1500", got)
	}
	jp := Request{Messages: []Message{UserText(strings.Repeat("日本語", 100))}}
	if got := estimateTokens(jp); got < 300 {
		t.Errorf("300 Japanese characters estimated at %d tokens, want at least 300", got)
	}
}

func TestReservationWaitsForOthersToSettle(t *testing.T) {
	r := &run{engine: &Engine{budget: Budget{MaxTokens: 100}}, released: make(chan struct{})}
	var a, b, c int
	if r.reserve(context.Background(), 40, &a) != nil || r.reserve(context.Background(), 40, &b) != nil {
		t.Fatal("first two reservations failed")
	}
	// C does not fit while A and B hold their reservations, but it will
	// once A releases what it did not use.
	got := make(chan error, 1)
	go func() { got <- r.reserve(context.Background(), 40, &c) }()
	for {
		select {
		case err := <-got:
			t.Fatalf("reserve returned %v instead of waiting", err)
		case <-time.After(time.Millisecond):
		}
		r.mu.Lock()
		waiting := r.waiters == 1
		r.mu.Unlock()
		if waiting {
			break
		}
	}
	r.settle(&a, Usage{InputTokens: 10})
	if err := <-got; err != nil {
		t.Fatalf("reserve after release = %v", err)
	}
	if r.exhausted {
		t.Error("a reservation that only had to wait marked the run exhausted")
	}
}

// unscorable is an exploration whose results cannot be scored, as when the
// budget has run out.
type unscorable struct{ Exploration }

func (unscorable) ScoreResult(context.Context, *Plan) *Score { return nil }

func TestBestPrefersTheMoreFinishedPlanWhenScoresTie(t *testing.T) {
	plan := func(id string, statuses ...Status) *Plan {
		p := &Plan{ID: id, Status: StatusPartial}
		for _, s := range statuses {
			p.Subtasks = append(p.Subtasks, &Task{Status: s})
		}
		return p
	}
	quarter := plan("p1", StatusDone, StatusFailed, StatusFailed, StatusPruned)
	twoThirds := plan("p2", StatusDone, StatusPartial, StatusPruned)
	if got := best(context.Background(), unscorable{}, []*Plan{quarter, twoThirds}); got != twoThirds {
		t.Errorf("best = %s, want p2, which finished more of its subtasks", got.ID)
	}
}

func TestPartialSubtasksCountHalf(t *testing.T) {
	plan := func(statuses ...Status) *Plan {
		p := &Plan{}
		for _, s := range statuses {
			p.Subtasks = append(p.Subtasks, &Task{Status: s})
		}
		return p
	}
	if got := finished(plan(StatusDone, StatusPartial, StatusPartial, StatusFailed)); got != 0.5 {
		t.Errorf("finished = %v, want 0.5", got)
	}
}

func TestWholeBudgetReserveIsNotUnlimited(t *testing.T) {
	for _, share := range []float64{1, 2} {
		general, all := Budget{MaxTokens: 100, SynthesisReserve: share}.limits()
		if general != 1 || all != 100 {
			t.Errorf("reserve %v: limits = %d, %d; want 1, 100", share, general, all)
		}
	}
}
