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

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxTokens: 100}), flatCost).Run(context.Background(), "goal")
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

	res, _ := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 1, MaxTokens: 80}), flatCost).Run(context.Background(), "goal")
	if res.Usage.Total() > 80 {
		t.Errorf("spent %d tokens, budget 80", res.Usage.Total())
	}
	if !res.Exhausted {
		t.Error("Exhausted = false")
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
	// C never fits in the budget. It starts after B, while A is still
	// running; A must not be canceled, and the plan becomes partial and
	// names C as missing.
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p",
			sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"},
			sub{ID: "c", Description: "C", DependsOn: []string{"b"}},
		))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			if taskOf(req) == "A" {
				time.Sleep(50 * time.Millisecond)
			}
			return echoTask(req)
		}).
		On(ethan.TagSynthesize, func(req ethan.Request) (*ethan.Response, error) {
			return ethantest.Text(ethantest.Prompt(req)), nil
		})
	costs := ethan.WithTokenEstimator(func(req ethan.Request) int {
		if taskOf(req) == "C" {
			return 1000
		}
		return ethantest.DefaultUsage.Total()
	})

	res, err := ethan.New(m, ethan.WithBudget(ethan.Budget{MaxDepth: 1, MaxTokens: 100}), costs).Run(context.Background(), "goal")
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
	// The root's single subtask X is decomposed into Y and Z; Z never fits.
	m := ethantest.New().
		On(ethan.TagPlan, func(req ethan.Request) (*ethan.Response, error) {
			switch taskOf(req) {
			case "goal":
				return ethantest.JSON(ethan.Decision{Plans: []ethan.PlanProposal{plan("p", sub{ID: "x", Description: "X"})}}), nil
			case "X":
				return ethantest.JSON(ethan.Decision{Plans: []ethan.PlanProposal{plan("q", sub{ID: "y", Description: "Y"}, sub{ID: "z", Description: "Z"})}}), nil
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
	for _, want := range []string{"Depth: 0 (tasks at depth 3", "Tasks that may still start: 8", "Tokens left: about 1000"} {
		if !strings.Contains(p, want) {
			t.Errorf("planner prompt lacks %q:\n%s", want, p)
		}
	}
}
