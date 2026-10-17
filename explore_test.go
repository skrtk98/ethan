package ethan_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
)

type sub = ethan.SubtaskProposal

func plan(rationale string, subtasks ...sub) ethan.PlanProposal {
	return ethan.PlanProposal{Rationale: rationale, Subtasks: subtasks}
}

// decomposeRoot makes the planner decompose the root into plans and treat
// every other task as atomic.
func decomposeRoot(plans ...ethan.PlanProposal) ethantest.Handler {
	return func(req ethan.Request) (*ethan.Response, error) {
		if strings.Contains(ethantest.Prompt(req), "<ancestors>") {
			return ethantest.JSON(ethan.Decision{Atomic: true}), nil
		}
		return ethantest.JSON(ethan.Decision{Plans: plans}), nil
	}
}

var taskRe = regexp.MustCompile(`(?s)<task>\n(.*?)\n</task>`)

func taskOf(req ethan.Request) string {
	m := taskRe.FindStringSubmatch(ethantest.Prompt(req))
	if m == nil {
		return ""
	}
	return m[1]
}

// echoTask answers each execution with "out(<task>)".
func echoTask(req ethan.Request) (*ethan.Response, error) {
	return ethantest.Text("out(" + taskOf(req) + ")"), nil
}

// joinResults synthesizes by concatenating the subtask outputs.
func joinResults(req ethan.Request) (*ethan.Response, error) {
	outs := regexp.MustCompile(`out\([^)]*\)`).FindAllString(ethantest.Prompt(req), -1)
	return ethantest.Text(strings.Join(outs, "+")), nil
}

func TestDecompositionRunsDependenciesInOrderAndSynthesizes(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("only",
			sub{ID: "a", Description: "A"},
			sub{ID: "b", Description: "B", DependsOn: []string{"a"}},
		))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "out(A)+out(B)" {
		t.Errorf("Output = %q", res.Output)
	}
	for _, c := range m.Calls(ethan.TagExecute) {
		if taskOf(c) == "B" && !strings.Contains(ethantest.Prompt(c), "out(A)") {
			t.Error("B was not given A's output")
		}
	}
	if res.Root.Chosen != res.Root.Plans[0].ID {
		t.Errorf("Chosen = %q", res.Root.Chosen)
	}
	b := res.Root.Plans[0].Subtasks[1]
	if b.Depth != 1 || len(b.DependsOn) != 1 || b.DependsOn[0] != res.Root.Plans[0].Subtasks[0].ID {
		t.Errorf("subtask B = %+v", b)
	}
}

func TestIndependentSubtasksRunInParallel(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			wg.Done()
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
				return echoTask(req)
			case <-time.After(2 * time.Second):
				return nil, errors.New("subtasks did not run concurrently")
			}
		}).
		On(ethan.TagSynthesize, joinResults)

	if _, err := ethan.New(m).Run(context.Background(), "goal"); err != nil {
		t.Fatal(err)
	}
}

// scoreBy scores plans and results by looking up a keyword in the prompt.
func scoreBy(scores map[string]float64) ethantest.Handler {
	return func(req ethan.Request) (*ethan.Response, error) {
		p := ethantest.Prompt(req)
		for k, v := range scores {
			if strings.Contains(p, k) {
				return ethantest.JSON(ethan.Score{Value: v, Reason: k}), nil
			}
		}
		return nil, fmt.Errorf("no score for prompt %q", p)
	}
}

func threePlans() ethantest.Handler {
	return decomposeRoot(
		plan("plan-low", sub{ID: "x", Description: "LOW"}),
		plan("plan-high", sub{ID: "x", Description: "HIGH"}),
		plan("plan-mid", sub{ID: "x", Description: "MID"}),
	)
}

func TestBeamExecutesTopPlansAndChoosesBestResult(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, threePlans()).
		On(ethan.TagScorePlan, scoreBy(map[string]float64{"plan-low": 0.1, "plan-high": 0.9, "plan-mid": 0.5})).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults).
		On(ethan.TagScoreResult, scoreBy(map[string]float64{"out(HIGH)": 0.3, "out(MID)": 0.8}))

	res, err := ethan.New(m, ethan.WithStrategy(ethan.BeamStrategy{Width: 2})).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "out(MID)" {
		t.Errorf("Output = %q, want out(MID)", res.Output)
	}
	statuses := map[string]ethan.Status{}
	for _, p := range res.Root.Plans {
		statuses[p.Rationale] = p.Status
	}
	want := map[string]ethan.Status{"plan-low": ethan.StatusPruned, "plan-high": ethan.StatusDone, "plan-mid": ethan.StatusDone}
	for k, v := range want {
		if statuses[k] != v {
			t.Errorf("%s status = %s, want %s", k, statuses[k], v)
		}
	}
	if res.Score == nil || res.Score.Value != 0.8 {
		t.Errorf("Score = %+v", res.Score)
	}
}

func TestBeamBacktracksToRemainingPlansOnFailure(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, threePlans()).
		On(ethan.TagScorePlan, scoreBy(map[string]float64{"plan-low": 0.1, "plan-high": 0.9, "plan-mid": 0.5})).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			if taskOf(req) == "LOW" {
				return echoTask(req)
			}
			return nil, errors.New("boom")
		}).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m, ethan.WithStrategy(ethan.BeamStrategy{Width: 2})).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "out(LOW)" {
		t.Errorf("Output = %q", res.Output)
	}
}

func TestRunFailsWhenAllPlansFail(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, threePlans()).
		On(ethan.TagScorePlan, scoreBy(map[string]float64{"plan": 0.5}))

	res, err := ethan.New(m).Run(context.Background(), "goal")
	if err == nil {
		t.Fatal("want error")
	}
	for _, p := range res.Root.Plans {
		if p.Status != ethan.StatusFailed {
			t.Errorf("plan %s status = %s", p.ID, p.Status)
		}
	}
}

func TestInvalidPlansAreDroppedAndTaskFallsBackToDirectExecution(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(
			plan("cycle", sub{ID: "a", Description: "A", DependsOn: []string{"b"}}, sub{ID: "b", Description: "B", DependsOn: []string{"a"}}),
			plan("dangling", sub{ID: "a", Description: "A", DependsOn: []string{"zzz"}}),
			plan("empty"),
		)).
		On(ethan.TagExecute, echoTask)

	res, err := ethan.New(m).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Root.Atomic || res.Output != "out(goal)" {
		t.Errorf("root = %+v", res.Root)
	}
}

func TestMaxDepthStopsDecomposition(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.ReplyJSON(ethan.Decision{Plans: []ethan.PlanProposal{
			plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}),
		}})).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 1})).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(m.Calls(ethan.TagPlan)); n != 1 {
		t.Errorf("planner called %d times, want 1", n)
	}
	if res.Output != "out(A)+out(B)" {
		t.Errorf("Output = %q", res.Output)
	}
}

// flatCost makes every call reserve exactly what the scripted model spends.
var flatCost = ethan.WithTokenEstimator(func(ethan.Request) int { return ethantest.DefaultUsage.Total() })

func TestTokenBudgetFallsBackToJoinedResults(t *testing.T) {
	// Each call costs 20 tokens: plan(root), plan(A), plan(B), execute(A),
	// execute(B) spend 100, so synthesis is refused.
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxTokens: 100, SynthesisReserve: -1}), flatCost).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exhausted {
		t.Error("Exhausted = false")
	}
	if !strings.Contains(res.Output, "out(A)") || !strings.Contains(res.Output, "out(B)") {
		t.Errorf("Output = %q", res.Output)
	}
	if len(m.Calls(ethan.TagSynthesize)) != 0 {
		t.Error("synthesis was called despite the exhausted budget")
	}
}

func TestRootSynthesisUsesTheReserve(t *testing.T) {
	// Each call costs 20 and 20 of the 100 tokens are held back: the plan
	// and three executions use the other 80, D is refused, and the root's
	// synthesis still runs on the reserve.
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p",
			sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"},
			sub{ID: "c", Description: "C", DependsOn: []string{"a", "b"}},
			sub{ID: "d", Description: "D", DependsOn: []string{"c"}},
		))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, ethantest.ReplyText("synthesized"))

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 1, MaxTokens: 100, SynthesisReserve: 0.2}), flatCost).
		Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exhausted || !res.Partial {
		t.Errorf("Exhausted = %v, Partial = %v; want both", res.Exhausted, res.Partial)
	}
	if res.Output != "synthesized" {
		t.Errorf("Output = %q, want the synthesis", res.Output)
	}
	if res.Usage.Total() > 100 {
		t.Errorf("spent %d tokens, budget 100", res.Usage.Total())
	}
}

func TestAtomicRootReleasesTheReserve(t *testing.T) {
	// 25 of the 45 tokens are held back. The plan (20) uses up the general
	// 20; the execution (20) fits only once the root, executed directly,
	// releases the reserve, and the run then ends within its budget.
	m := ethantest.New().On(ethan.TagPlan, atomicPlan()).On(ethan.TagExecute, echoTask)

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxTokens: 45, SynthesisReserve: 25.0 / 45}), flatCost).
		Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if res.Exhausted {
		t.Error("Exhausted = true")
	}
}

func TestReserveSynthesizesTheChosenRootPlan(t *testing.T) {
	// Two root plans run out of budget before their synthesis. P2 finished
	// all of its subtasks, so it is chosen and synthesized on the reserve,
	// which has room for one synthesis only.
	cDone := make(chan struct{})
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(
			plan("p1", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B", DependsOn: []string{"a"}}),
			plan("p2", sub{ID: "c", Description: "C"}),
		)).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			if taskOf(req) == "A" {
				<-cDone // B, which needs A, starts only after C has spent its tokens
			}
			return echoTask(req)
		}).
		On(ethan.TagSynthesize, joinResults)
	done := ethan.WithObserver(ethan.ObserverFunc(func(e ethan.Event) {
		if e.Kind == ethan.EventTaskDone && e.TaskID == "t0.p2.t1" {
			close(cDone)
		}
	}))
	// plan(root) 20, execute A and C 40: the general 60 is used up and B is
	// refused; the reserve of 25 fits one synthesis.
	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 1, MaxTokens: 85, SynthesisReserve: 25.0 / 85}),
		ethan.WithStrategy(ethan.BeamStrategy{Width: 2}), flatCost, done).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if res.Root.Chosen != "t0.p2" || res.Output != "out(C)" {
		t.Errorf("chose %s with output %q; want t0.p2 synthesized to out(C)", res.Root.Chosen, res.Output)
	}
	if n := len(m.Calls(ethan.TagSynthesize)); n != 1 {
		t.Errorf("%d syntheses, want the chosen plan's only", n)
	}
}

func TestFailedReserveSynthesisIsAnError(t *testing.T) {
	// The root's synthesis is refused for budget and its retry on the
	// reserve fails for another reason, which must not pass as success.
	var calls atomic.Int32
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, func(ethan.Request) (*ethan.Response, error) {
			calls.Add(1)
			return nil, errors.New("model down")
		})

	_, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 1, MaxTokens: 80, SynthesisReserve: 0.25}), flatCost).
		Run(context.Background(), "goal")
	if err == nil || !strings.Contains(err.Error(), "model down") || calls.Load() != 1 {
		t.Errorf("err = %v after %d syntheses; want the retry's failure", err, calls.Load())
	}
}

// refusingSynthesizer is refused for budget on every call.
type refusingSynthesizer struct{ calls atomic.Int32 }

func (s *refusingSynthesizer) Synthesize(context.Context, ethan.Brief, []ethan.SubtaskResult) (string, error) {
	s.calls.Add(1)
	return "", ethan.ErrBudgetExhausted
}

func TestCancellationBeforeReserveSynthesisIsAFailure(t *testing.T) {
	// The caller cancels once the root's plan is done but only joined; the
	// retry on the reserve is refused for budget, which must not hide the
	// cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}))).
		On(ethan.TagExecute, echoTask)
	syn := &refusingSynthesizer{}
	cancelOnDone := ethan.WithObserver(ethan.ObserverFunc(func(e ethan.Event) {
		if e.Kind == ethan.EventPlanDone && e.PlanID == "t0.p1" {
			cancel()
		}
	}))

	_, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 1}), ethan.WithSynthesizer(syn), cancelOnDone).Run(ctx, "goal")
	if !errors.Is(err, context.Canceled) || syn.calls.Load() != 2 {
		t.Errorf("err = %v after %d syntheses, want context.Canceled after the retry", err, syn.calls.Load())
	}
}

func TestSynthesisBelowTheRootLeavesTheReserveAlone(t *testing.T) {
	// X is decomposed into Y and Z. The plan calls for the root and X and
	// the executions of Y and Z use the 80 general tokens, so X's synthesis
	// is refused and joins; the reserve is left for the root's synthesis.
	m := ethantest.New().
		On(ethan.TagPlan, func(req ethan.Request) (*ethan.Response, error) {
			switch taskOf(req) {
			case "goal":
				return ethantest.JSON(ethan.Decision{Plans: []ethan.PlanProposal{plan("root", sub{ID: "x", Description: "X"})}}), nil
			case "X":
				return ethantest.JSON(ethan.Decision{Plans: []ethan.PlanProposal{plan("x",
					sub{ID: "y", Description: "Y"}, sub{ID: "z", Description: "Z"})}}), nil
			}
			return ethantest.JSON(ethan.Decision{Atomic: true}), nil
		}).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, ethantest.ReplyText("synthesized"))

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxTokens: 100, SynthesisReserve: 0.2}), flatCost).
		Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(m.Calls(ethan.TagSynthesize)); n != 1 || res.Output != "synthesized" {
		t.Errorf("%d syntheses, Output = %q; want only the root's", n, res.Output)
	}
	if x := res.Root.Plans[0].Subtasks[0]; !strings.Contains(x.Output, "out(Y)") {
		t.Errorf("X's output = %q, want Y and Z joined", x.Output)
	}
}

func TestTokenBudgetExhaustedBeforeAnyResultFails(t *testing.T) {
	m := ethantest.New().On(ethan.TagPlan, threePlans()).On(ethan.TagExecute, echoTask)

	_, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxTokens: 20}), flatCost).Run(context.Background(), "goal")
	if !errors.Is(err, ethan.ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
}

func TestMaxConcurrencyLimitsModelCalls(t *testing.T) {
	var inFlight, peak atomic.Int32
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}, sub{ID: "c", Description: "C"}))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			return echoTask(req)
		}).
		On(ethan.TagSynthesize, joinResults)

	if _, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxConcurrency: 1})).Run(context.Background(), "goal"); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 1 {
		t.Errorf("peak concurrency = %d, want 1", peak.Load())
	}
}

func TestConcurrentCallsCannotOverspend(t *testing.T) {
	// The root plan costs 20; four parallel executions of 20 each would
	// overshoot 80, so the reservation must refuse one of them up front.
	release := make(chan struct{})
	var started atomic.Int32
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p",
			sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"},
			sub{ID: "c", Description: "C"}, sub{ID: "d", Description: "D"},
		))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			if started.Add(1) == 3 {
				close(release)
			}
			<-release
			return echoTask(req)
		}).
		On(ethan.TagSynthesize, joinResults)

	res, _ := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 1, MaxTokens: 80, SynthesisReserve: -1}), flatCost).Run(context.Background(), "goal")
	if res.Usage.Total() > 80 {
		t.Errorf("spent %d tokens, budget 80", res.Usage.Total())
	}
	if !res.Exhausted {
		t.Error("Exhausted = false")
	}
}

func TestReservationsWaitingOnEachOtherDoNotDeadlock(t *testing.T) {
	// Two executions each hold 30 and then ask for 30 more, which fits only
	// once the other releases. One must give up so the other can finish.
	var ready sync.WaitGroup
	ready.Add(2)
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			ready.Done()
			ready.Wait()
			if err := req.Reserve(30); err != nil {
				return &ethan.Response{Usage: ethantest.DefaultUsage}, err
			}
			return echoTask(req)
		}).
		On(ethan.TagSynthesize, joinResults)
	est := ethan.WithTokenEstimator(func(ethan.Request) int { return 30 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 1, MaxTokens: 100, SynthesisReserve: -1}), est).Run(ctx, "goal")
	if ctx.Err() != nil {
		t.Fatal("deadlocked")
	}
	if err != nil {
		t.Fatal(err)
	}
	done := 0
	for _, st := range res.Root.Plans[0].Subtasks {
		if st.Status == ethan.StatusDone {
			done++
		}
	}
	if done != 1 || !res.Partial || !res.Exhausted {
		t.Errorf("done = %d, Partial = %v, Exhausted = %v; want one finished subtask and the refusal recorded",
			done, res.Partial, res.Exhausted)
	}
}

func TestExhaustedWhenLastCallReachesTheLimit(t *testing.T) {
	m := ethantest.New().On(ethan.TagPlan, atomicPlan()).On(ethan.TagExecute, ethantest.ReplyText("x"))
	small := ethan.WithTokenEstimator(func(ethan.Request) int { return 1 })

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxTokens: 40}), small).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exhausted {
		t.Error("spending exactly the budget did not set Exhausted")
	}
}

func TestUsageOfFailedCallsIsCounted(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, atomicPlan()).
		On(ethan.TagExecute, func(ethan.Request) (*ethan.Response, error) {
			return &ethan.Response{Usage: ethan.Usage{InputTokens: 500}}, errors.New("refused")
		})

	res, _ := ethan.New(m).Run(context.Background(), "goal")
	if res.Usage.InputTokens < 500 {
		t.Errorf("Usage = %+v, want the failed call's 500 input tokens", res.Usage)
	}
}

func TestAdapterContinuationsAreReserved(t *testing.T) {
	var reserveErr error
	m := ethantest.New().
		On(ethan.TagPlan, atomicPlan()).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			// An adapter resuming a paused turn asks for more budget first.
			reserveErr = req.Reserve(1000)
			if reserveErr != nil {
				return &ethan.Response{Usage: ethantest.DefaultUsage}, reserveErr
			}
			return ethantest.Text("x"), nil
		})

	_, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxTokens: 500}), flatCost).Run(context.Background(), "goal")
	if !errors.Is(reserveErr, ethan.ErrBudgetExhausted) || !errors.Is(err, ethan.ErrBudgetExhausted) {
		t.Errorf("reserve err = %v, run err = %v", reserveErr, err)
	}
}

func TestBudgetExhaustionKeepsCompletedSubtasks(t *testing.T) {
	// C never fits in the budget. It is refused while A is running; A must
	// not be canceled, and the plan becomes partial and names C as missing.
	aRunning, cRefused := make(chan struct{}), make(chan struct{})
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p",
			sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"},
			sub{ID: "c", Description: "C", DependsOn: []string{"b"}},
		))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			if taskOf(req) == "A" {
				close(aRunning)
				<-cRefused
			}
			return echoTask(req)
		}).
		On(ethan.TagSynthesize, func(req ethan.Request) (*ethan.Response, error) {
			return ethantest.Text(ethantest.Prompt(req)), nil
		})
	costs := ethan.WithTokenEstimator(func(req ethan.Request) int {
		if taskOf(req) == "C" {
			<-aRunning
			return 1000
		}
		return ethantest.DefaultUsage.Total()
	})
	refusal := ethan.WithObserver(ethan.ObserverFunc(func(e ethan.Event) {
		if e.Kind == ethan.EventTaskFailed && e.TaskID == "t0.p1.t3" {
			close(cRefused)
		}
	}))

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 1, MaxTokens: 100}), costs, refusal).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Partial || !res.Exhausted || res.Root.Status != ethan.StatusPartial {
		t.Errorf("Partial=%v Exhausted=%v root=%s", res.Partial, res.Exhausted, res.Root.Status)
	}
	for _, want := range []string{"out(A)", "out(B)", "NOT COMPLETED"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("Output lacks %q:\n%s", want, res.Output)
		}
	}
	if st := res.Root.Plans[0].Subtasks[0]; st.Status != ethan.StatusDone {
		t.Errorf("in-flight sibling A ended %s", st.Status)
	}
}

func TestPartialResultsPropagateThroughLevels(t *testing.T) {
	// The root's single subtask X is decomposed into Y and then Z; Z never fits.
	m := ethantest.New().
		On(ethan.TagPlan, func(req ethan.Request) (*ethan.Response, error) {
			switch taskOf(req) {
			case "goal":
				return ethantest.JSON(ethan.Decision{Plans: []ethan.PlanProposal{plan("p", sub{ID: "x", Description: "X"})}}), nil
			case "X":
				return ethantest.JSON(ethan.Decision{Plans: []ethan.PlanProposal{plan("q", sub{ID: "y", Description: "Y"}, sub{ID: "z", Description: "Z", DependsOn: []string{"y"}})}}), nil
			}
			return ethantest.JSON(ethan.Decision{Atomic: true}), nil
		}).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)
	costs := ethan.WithTokenEstimator(func(req ethan.Request) int {
		if taskOf(req) == "Z" {
			return 1000
		}
		return ethantest.DefaultUsage.Total()
	})

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 2, MaxTokens: 200}), costs).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Partial || !strings.Contains(res.Output, "out(Y)") {
		t.Errorf("Partial=%v Output=%q", res.Partial, res.Output)
	}
	if x := res.Root.Plans[0].Subtasks[0]; x.Status != ethan.StatusPartial {
		t.Errorf("X status = %s", x.Status)
	}
}

func TestNodeLimitIsHard(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p",
			sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"},
			sub{ID: "c", Description: "C"}, sub{ID: "d", Description: "D"},
		))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 1, MaxNodes: 3})).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(m.Calls(ethan.TagExecute)); n != 2 {
		t.Errorf("executed %d subtasks, want 2 (root + 2 = 3 nodes)", n)
	}
	if !res.Partial || !res.Exhausted {
		t.Errorf("Partial=%v Exhausted=%v", res.Partial, res.Exhausted)
	}
}

func TestDecompositionNeedsRoomForTwoNodes(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}))).
		On(ethan.TagExecute, echoTask)

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxNodes: 2})).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Root.Atomic || len(m.Calls(ethan.TagPlan)) != 0 {
		t.Errorf("root decomposed with room for only one more task")
	}
}

func TestBeamRunsOnePlanAtATimeBelowRoot(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, func(req ethan.Request) (*ethan.Response, error) {
			switch taskOf(req) {
			case "goal":
				return ethantest.JSON(ethan.Decision{Plans: []ethan.PlanProposal{plan("p", sub{ID: "x", Description: "X"})}}), nil
			case "X":
				return ethantest.JSON(ethan.Decision{Plans: []ethan.PlanProposal{
					plan("first", sub{ID: "y", Description: "Y1"}),
					plan("second", sub{ID: "y", Description: "Y2"}),
				}}), nil
			}
			return ethantest.JSON(ethan.Decision{Atomic: true}), nil
		}).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	x := res.Root.Plans[0].Subtasks[0]
	if x.Plans[0].Status != ethan.StatusDone || x.Plans[1].Status != ethan.StatusPruned {
		t.Errorf("X's plans: %s, %s; want done, pruned", x.Plans[0].Status, x.Plans[1].Status)
	}
	if len(m.Calls(ethan.TagScoreResult)) != 0 {
		t.Error("results were scored although only one plan ran")
	}
}

func TestPlannerSeesDepthAndBudget(t *testing.T) {
	m := ethantest.New().On(ethan.TagPlan, atomicPlan()).On(ethan.TagExecute, echoTask)

	_, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 3, MaxNodes: 9, MaxTokens: 1000}), flatCost).
		Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	p := ethantest.Prompt(m.Calls(ethan.TagPlan)[0])
	// The planner sees the tokens left outside the 15% held back for synthesis.
	for _, want := range []string{"Depth: 0 (tasks at depth 3", "Tasks that may still start: 8", "Tokens left: about 850"} {
		if !strings.Contains(p, want) {
			t.Errorf("planner prompt lacks %q:\n%s", want, p)
		}
	}
}

func TestCancellationIsNeverAPartialSuccess(t *testing.T) {
	// A finishes, then the caller cancels before B (which needs A) starts.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p",
			sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B", DependsOn: []string{"a"}},
		))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			cancel()
			return echoTask(req)
		}).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m).Run(ctx, "goal")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Partial || res.Root.Status != ethan.StatusFailed {
		t.Errorf("Partial=%v root=%s", res.Partial, res.Root.Status)
	}
}
