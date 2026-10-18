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

	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1})).Run(context.Background(), "goal")
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

func TestTaskLimitCancelsTheRun(t *testing.T) {
	// The root and two subtasks use up MaxTasks; starting a third cancels
	// the whole run.
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p",
			sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"},
			sub{ID: "c", Description: "C"}, sub{ID: "d", Description: "D"},
		))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTasks: 3})).Run(context.Background(), "goal")
	if !errors.Is(err, ethan.ErrTaskLimit) || !errors.Is(err, ethan.ErrLimitExceeded) {
		t.Fatalf("err = %v, want ErrTaskLimit", err)
	}
	if n := len(m.Calls(ethan.TagExecute)); n > 2 {
		t.Errorf("executed %d subtasks, want at most 2", n)
	}
	if res.Root.Status != ethan.StatusFailed || res.Error == "" {
		t.Errorf("root = %s, Error = %q", res.Root.Status, res.Error)
	}
}

func TestTokenLimitStopsCallsOnceReached(t *testing.T) {
	// The plan spends 20, which reaches the limit: execution never starts.
	m := ethantest.New().On(ethan.TagPlan, atomicPlan()).On(ethan.TagExecute, echoTask)

	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxTokens: 20})).Run(context.Background(), "goal")
	if !errors.Is(err, ethan.ErrTokenLimit) {
		t.Fatalf("err = %v, want ErrTokenLimit", err)
	}
	if len(m.Calls(ethan.TagExecute)) != 0 || res.Usage.Total() != 20 {
		t.Errorf("executed %d times, spent %d", len(m.Calls(ethan.TagExecute)), res.Usage.Total())
	}
}

// blockingModel makes the execution of one task wait until its context is
// canceled, closing started when it begins.
type blockingModel struct {
	*ethantest.Model
	task    string
	started chan struct{}
}

func (m blockingModel) Generate(ctx context.Context, req ethan.Request) (*ethan.Response, error) {
	if req.Tag == ethan.TagExecute && taskOf(req) == m.task {
		close(m.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return m.Model.Generate(ctx, req)
}

func TestCallPastTheTokenLimitCancelsCallsInFlight(t *testing.T) {
	// After the plan (20 of 30), A and B start. A's 20 tokens carry the total
	// past the limit while B is still running; B must be canceled.
	started := make(chan struct{})
	m := blockingModel{task: "B", started: started, Model: ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			<-started
			return echoTask(req)
		})}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTokens: 30})).Run(ctx, "goal")
	if ctx.Err() != nil {
		t.Fatal("B was not canceled")
	}
	if !errors.Is(err, ethan.ErrTokenLimit) {
		t.Fatalf("err = %v, want ErrTokenLimit", err)
	}
	// A's response is paid for, so it counts, but it is not used.
	if a, b := res.Root.Plans[0].Subtasks[0], res.Root.Plans[0].Subtasks[1]; a.Status != ethan.StatusFailed || b.Status != ethan.StatusFailed {
		t.Errorf("A %s, B %s; want both failed", a.Status, b.Status)
	}
	if res.Usage.Total() != 40 {
		t.Errorf("Usage = %d, want 40 including A's response", res.Usage.Total())
	}
}

func TestExceededLimitStopsBacktracking(t *testing.T) {
	// One plan at a time: the plan and two plan scores spend 60 of 70, and
	// executing the first plan carries the total past the limit. The second
	// plan must not be tried.
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p1", sub{ID: "a", Description: "A"}), plan("p2", sub{ID: "b", Description: "B"}))).
		On(ethan.TagScorePlan, ethantest.ReplyJSON(ethan.Score{Value: 0.5})).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTokens: 70}),
		ethan.WithStrategy(ethan.BeamStrategy{Width: 1})).Run(context.Background(), "goal")
	if !errors.Is(err, ethan.ErrTokenLimit) {
		t.Fatalf("err = %v, want ErrTokenLimit", err)
	}
	if n := len(m.Calls(ethan.TagExecute)); n != 1 || res.Root.Plans[1].Status != ethan.StatusPruned {
		t.Errorf("executed %d subtasks, second plan %s; want 1 and pruned", n, res.Root.Plans[1].Status)
	}
}

func TestLimitFailsTheRunEvenIfAParallelPlanSucceeded(t *testing.T) {
	// Both root plans run side by side. "fast" finishes completely; then the
	// second subtask of "slow" goes beyond MaxTasks. The run must fail, and
	// keep "fast" done for a resume to choose among.
	fastDone := make(chan struct{})
	m := ethantest.New().
		On(ethan.TagPlan, decomposeRoot(
			plan("fast", sub{ID: "f", Description: "F"}),
			plan("slow", sub{ID: "s1", Description: "S1"}, sub{ID: "s2", Description: "S2", DependsOn: []string{"s1"}}),
		)).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			if taskOf(req) == "S1" {
				<-fastDone
			}
			return echoTask(req)
		}).
		On(ethan.TagSynthesize, joinResults)
	done := ethan.WithObserver(ethan.ObserverFunc(func(e ethan.Event) {
		if e.Kind == ethan.EventPlanDone && e.PlanID == "t0.p1" {
			close(fastDone)
		}
	}))

	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTasks: 3}), done).Run(context.Background(), "goal")
	if !errors.Is(err, ethan.ErrTaskLimit) {
		t.Fatalf("err = %v, want ErrTaskLimit", err)
	}
	if res.Root.Status != ethan.StatusFailed || res.Root.Plans[0].Status != ethan.StatusDone {
		t.Errorf("root %s, fast plan %s; want failed and done", res.Root.Status, res.Root.Plans[0].Status)
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

	if _, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxConcurrency: 1})).Run(context.Background(), "goal"); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 1 {
		t.Errorf("peak concurrency = %d, want 1", peak.Load())
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

func TestPlannerSeesItsDepth(t *testing.T) {
	m := ethantest.New().On(ethan.TagPlan, atomicPlan()).On(ethan.TagExecute, echoTask)

	if _, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 3})).Run(context.Background(), "goal"); err != nil {
		t.Fatal(err)
	}
	if p := ethantest.Prompt(m.Calls(ethan.TagPlan)[0]); !strings.Contains(p, "<depth>0 (tasks at depth 3") {
		t.Errorf("planner prompt lacks the depth:\n%s", p)
	}
}

func TestCancellationFailsTheRun(t *testing.T) {
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
	if res.Root.Status != ethan.StatusFailed {
		t.Errorf("root = %s", res.Root.Status)
	}
}
