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

func TestBestPrefersSynthesizedPlans(t *testing.T) {
	// Two root plans ran out of budget; only one fitted its synthesis in
	// the reserve. It wins without scoring, which the budget would refuse.
	joined := &Plan{ID: "p1", Status: StatusPartial, joined: true}
	synthesized := &Plan{ID: "p2", Status: StatusPartial}
	if got := best(context.Background(), nil, []*Plan{joined, synthesized}); got != synthesized {
		t.Errorf("best = %s, want the synthesized plan", got.ID)
	}
}
