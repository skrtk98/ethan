package ethan_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
)

// chain decomposes the root into A, B after A, and C after B.
func chain() *ethantest.Model {
	return ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("p",
			sub{ID: "a", Description: "A"},
			sub{ID: "b", Description: "B", DependsOn: []string{"a"}},
			sub{ID: "c", Description: "C", DependsOn: []string{"b"}},
		))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)
}

func TestResumeContinuesWhereTheRunStopped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	j, err := ethan.CreateJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	// The plan (20) and A (20) reach the limit: B never starts.
	_, err = ethan.New(chain(), ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTokens: 40})).
		Run(context.Background(), "goal", ethan.JournalTo(j))
	if !errors.Is(err, ethan.ErrTokenLimit) {
		t.Fatalf("first run: err = %v, want ErrTokenLimit", err)
	}

	events, err := ethan.ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	m := chain()
	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1})).Resume(context.Background(), events, ethan.JournalTo(j))
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	if res.Output != "out(A)+out(B)+out(C)" {
		t.Errorf("Output = %q", res.Output)
	}
	if len(m.Calls(ethan.TagPlan)) != 0 {
		t.Error("the root was planned again")
	}
	var executed []string
	for _, req := range m.Calls(ethan.TagExecute) {
		executed = append(executed, ethantest.Task(req))
	}
	if len(executed) != 2 || executed[0] != "B" || executed[1] != "C" {
		t.Errorf("resumed run executed %v, want [B C]", executed)
	}
	if res.Usage.Total() != 100 {
		t.Errorf("Usage = %d, want 100 over both runs", res.Usage.Total())
	}

	// The journal now holds both runs and replays to the finished tree.
	all, err := ethan.ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ethan.Replay(all)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Output != res.Output || replayed.Error != "" || replayed.Usage != res.Usage {
		t.Errorf("replayed output %q, error %q, usage %+v", replayed.Output, replayed.Error, replayed.Usage)
	}
}

func TestReplayRebuildsTheTree(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, threePlans()).
		On(ethan.TagScorePlan, scoreBy(map[string]float64{"plan-low": 0.1, "plan-high": 0.9, "plan-mid": 0.5})).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults).
		On(ethan.TagScoreResult, scoreBy(map[string]float64{"out(HIGH)": 0.3, "out(MID)": 0.8}))
	res, err := ethan.New(m).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}

	replayed, err := ethan.Replay(res.Events)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(res.Root)
	got, _ := json.Marshal(replayed.Root)
	if string(got) != string(want) {
		t.Errorf("replayed tree differs:\n got %s\nwant %s", got, want)
	}
	if replayed.Output != res.Output || replayed.Usage != res.Usage {
		t.Errorf("replayed output %q usage %+v, want %q %+v", replayed.Output, replayed.Usage, res.Output, res.Usage)
	}
}

func TestResumeKeepsScores(t *testing.T) {
	scores := scoreBy(map[string]float64{"plan-low": 0.1, "plan-high": 0.9, "plan-mid": 0.5})
	results := scoreBy(map[string]float64{"out(HIGH)": 0.3, "out(MID)": 0.8})
	// All three plans are scored, then only one of the two top plans may start.
	first := ethantest.New().On(ethan.TagPlan, threePlans()).On(ethan.TagScorePlan, scores).
		On(ethan.TagExecute, echoTask).On(ethan.TagSynthesize, joinResults)
	res, err := ethan.New(first, ethan.WithLimits(ethan.Limits{MaxTasks: 2})).Run(context.Background(), "goal")
	if !errors.Is(err, ethan.ErrTaskLimit) {
		t.Fatalf("first run: err = %v, want ErrTaskLimit", err)
	}

	m := ethantest.New().On(ethan.TagScorePlan, scores).On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults).On(ethan.TagScoreResult, results)
	resumed, err := ethan.New(m).Resume(context.Background(), res.Events)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(m.Calls(ethan.TagScorePlan)); n != 0 {
		t.Errorf("plans scored %d more times", n)
	}
	if resumed.Output != "out(MID)" {
		t.Errorf("Output = %q, want the better result out(MID)", resumed.Output)
	}
}

func TestResumingAFinishedRunCallsNoModel(t *testing.T) {
	res, err := ethan.New(chain(), ethan.WithLimits(ethan.Limits{MaxDepth: 1})).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	m := ethantest.New() // answers nothing
	resumed, err := ethan.New(m).Resume(context.Background(), res.Events)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Output != res.Output || len(m.Calls(ethan.TagPlan))+len(m.Calls(ethan.TagExecute)) != 0 {
		t.Errorf("Output = %q, calls made", resumed.Output)
	}
}

func TestReplayRejectsForeignEvents(t *testing.T) {
	if _, err := ethan.Replay([]ethan.Event{{Kind: ethan.EventTaskStarted, TaskID: "t0"}}); err == nil {
		t.Error("events without run_started were accepted")
	}
	if _, err := ethan.Replay([]ethan.Event{{Kind: ethan.EventRunStarted, Text: "g", Format: ethan.JournalFormat}, {Kind: ethan.EventTaskDone, TaskID: "t9"}}); err == nil {
		t.Error("an unknown task was accepted")
	}
	if _, err := ethan.Replay([]ethan.Event{{Kind: ethan.EventRunStarted, Text: "g", Format: ethan.JournalFormat}, {Kind: ethan.EventRunStarted, Text: "h"}}); err == nil {
		t.Error("two runs in one journal were accepted")
	}
}

func TestJournalOfAnotherFormatIsRejected(t *testing.T) {
	for _, format := range []int{0, ethan.JournalFormat + 1} {
		events := []ethan.Event{{Kind: ethan.EventRunStarted, Text: "g", Format: format}}
		if _, err := ethan.Replay(events); !errors.Is(err, ethan.ErrJournalFormat) {
			t.Errorf("Replay of format %d: err = %v, want ErrJournalFormat", format, err)
		}
		engine := ethan.New(ethantest.New())
		if _, err := engine.Resume(context.Background(), events); !errors.Is(err, ethan.ErrJournalFormat) {
			t.Errorf("Resume of format %d: err = %v, want ErrJournalFormat", format, err)
		}
	}
}

func TestReplayRebuildsAStoppedRun(t *testing.T) {
	// Some subtasks are never started; that, too, must be in the journal.
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("p",
			sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B", DependsOn: []string{"a"}},
			sub{ID: "c", Description: "C", DependsOn: []string{"b"}},
		))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)
	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTasks: 2})).Run(context.Background(), "goal")
	if !errors.Is(err, ethan.ErrTaskLimit) {
		t.Fatalf("err = %v, want ErrTaskLimit", err)
	}
	replayed, err := ethan.Replay(res.Events)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(res.Root)
	got, _ := json.Marshal(replayed.Root)
	if string(got) != string(want) {
		t.Errorf("replayed tree differs:\n got %s\nwant %s", got, want)
	}
	if replayed.Error != res.Error || replayed.Usage != res.Usage {
		t.Errorf("replayed error %q usage %+v, want %q %+v", replayed.Error, replayed.Usage, res.Error, res.Usage)
	}
}

// cut returns events up to and including the first one matching kind and
// task, as if the process had died right after writing it.
func cut(t *testing.T, events []ethan.Event, kind ethan.EventKind, task string) []ethan.Event {
	t.Helper()
	for i, e := range events {
		if e.Kind == kind && e.TaskID == task {
			return events[:i+1]
		}
	}
	t.Fatalf("no %s event for %s", kind, task)
	return nil
}

func TestResumeAfterTheChoiceFinishesTheTask(t *testing.T) {
	// One plan at a time: "bad" fails, "good" succeeds and is chosen. The
	// process dies right after recording the choice; resuming must not try
	// "bad" again.
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("bad", sub{ID: "x", Description: "BAD"}), plan("good", sub{ID: "x", Description: "GOOD"}))).
		On(ethan.TagScorePlan, scoreBy(map[string]float64{"bad": 0.9, "good": 0.5})).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			if ethantest.Task(req) == "BAD" {
				return nil, errors.New("boom")
			}
			return echoTask(req)
		}).
		On(ethan.TagSynthesize, joinResults)
	beam1 := ethan.WithStrategy(ethan.BeamStrategy{Width: 1})
	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1}), beam1).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}

	again := ethantest.New() // answers nothing
	resumed, err := ethan.New(again, beam1).Resume(context.Background(), cut(t, res.Events, ethan.EventPlanChosen, "t0"))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Output != "out(GOOD)" || len(again.Calls(ethan.TagExecute)) != 0 {
		t.Errorf("Output = %q after %d executions; want out(GOOD) and none", resumed.Output, len(again.Calls(ethan.TagExecute)))
	}
	// The journal of the resumed run replays to the same finished tree.
	replayed, err := ethan.Replay(resumed.Events)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Root.Status != ethan.StatusDone || replayed.Output != resumed.Output {
		t.Errorf("replayed root %s with output %q", replayed.Root.Status, replayed.Output)
	}
}

func TestUsageIsJournaledPerCall(t *testing.T) {
	res, err := ethan.New(chain(), ethan.WithLimits(ethan.Limits{MaxDepth: 1})).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	// The process dies right after A's call, before A is recorded done.
	replayed, err := ethan.Replay(cut(t, res.Events, ethan.EventUsage, "t0.p1.t1"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := replayed.Usage.Total(), 2*ethantest.DefaultUsage.Total(); got != want {
		t.Errorf("Usage = %d, want %d for the plan and A's call", got, want)
	}
}

func TestNoToolRunsAfterTheTokenLimit(t *testing.T) {
	// The execution's response carries the total past the limit and asks for
	// a note; the note must not be posted.
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Atomic()).
		On(ethan.TagExecute, func(ethan.Request) (*ethan.Response, error) {
			return ethantest.ToolCalls(ethan.ToolCall{ID: "c1", Name: "note", Input: json.RawMessage(`{"text":"posted too late"}`)}), nil
		})

	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxTokens: 30})).Run(context.Background(), "goal")
	if !errors.Is(err, ethan.ErrTokenLimit) {
		t.Fatalf("err = %v, want ErrTokenLimit", err)
	}
	if len(res.Notes) != 0 {
		t.Errorf("notes = %v", res.Notes)
	}
}

func TestNoToolRunsOnceTheRunIsCanceled(t *testing.T) {
	// Whichever of A and B starts second goes beyond MaxTasks while the
	// other's call is in flight; that response then asks for a note, which
	// must not be posted.
	var once sync.Once
	pruned2 := make(chan struct{})
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			<-pruned2
			return ethantest.ToolCalls(ethan.ToolCall{ID: "c1", Name: "note", Input: json.RawMessage(`{"text":"posted after the cancel"}`)}), nil
		})
	onPrune := ethan.WithObserver(ethan.ObserverFunc(func(e ethan.Event) {
		if e.Kind == ethan.EventTaskPruned {
			once.Do(func() { close(pruned2) })
		}
	}))

	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTasks: 2}), onPrune).Run(context.Background(), "goal")
	if !errors.Is(err, ethan.ErrTaskLimit) {
		t.Fatalf("err = %v, want ErrTaskLimit", err)
	}
	if len(res.Notes) != 0 {
		t.Errorf("notes = %v", res.Notes)
	}
}

// twoNotes is a tool-calling response asking for two notes.
func twoNotes() *ethan.Response {
	call := func(id, text string) ethan.ToolCall {
		return ethan.ToolCall{ID: id, Name: "note", Input: json.RawMessage(`{"text":"` + text + `"}`)}
	}
	return ethantest.ToolCalls(call("c1", "first"), call("c2", "second"))
}

// cancelingTool cancels the run while it runs.
type cancelingTool struct{ cancel context.CancelFunc }

func (cancelingTool) Spec() ethan.ToolSpec {
	return ethan.ToolSpec{Name: "stop", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (c cancelingTool) Call(context.Context, json.RawMessage) (string, error) {
	c.cancel()
	return "stopped", nil
}

func TestNoToolRunsAfterACancelBetweenTools(t *testing.T) {
	// The first tool cancels the run; the second, a note, must not run.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Atomic()).
		On(ethan.TagExecute, func(ethan.Request) (*ethan.Response, error) {
			r := twoNotes()
			r.Parts[0].ToolCall.Name = "stop"
			return r, nil
		})
	agent := ethan.Agent{Name: "a", Tools: []ethan.Tool{cancelingTool{cancel: cancel}}}

	res, _ := ethan.New(m, ethan.WithAgents(agent)).Run(ctx, "goal")
	if len(res.Notes) != 0 {
		t.Errorf("notes = %v", res.Notes)
	}
}

func TestResumeNeverPrunesAFinishedSubtask(t *testing.T) {
	// The first run finishes A, then B fails.
	aDone := make(chan struct{})
	var once sync.Once
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			if ethantest.Task(req) == "B" {
				<-aDone
				return nil, errors.New("B failed")
			}
			return echoTask(req)
		})
	first, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1}), ethan.WithObserver(ethan.ObserverFunc(func(e ethan.Event) {
		if e.Kind == ethan.EventTaskDone && e.TaskID == "t0.p1.t1" {
			once.Do(func() { close(aDone) })
		}
	}))).Run(context.Background(), "goal")
	if err == nil {
		t.Fatal("first run succeeded")
	}

	// On resume B fails at once, canceling its siblings; A must stay done.
	for range 100 {
		res, err := ethan.New(ethantest.New(), ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTasks: 1})).Resume(context.Background(), first.Events)
		if !errors.Is(err, ethan.ErrTaskLimit) {
			t.Fatalf("err = %v, want ErrTaskLimit", err)
		}
		if a := res.Root.Plans[0].Subtasks[0]; a.Status != ethan.StatusDone || a.Output != "out(A)" {
			t.Fatalf("A is %s (%s) after the resume", a.Status, a.Error)
		}
	}
}
