package ethan_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
)

func TestNotesAreSharedThroughTheBlackboard(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("p",
			sub{ID: "a", Description: "A"},
			sub{ID: "b", Description: "B", DependsOn: []string{"a"}},
		))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			last := req.Messages[len(req.Messages)-1]
			if ethantest.Task(req) == "A" && last.Parts[0].ToolResult == nil {
				return ethantest.ToolCalls(ethan.ToolCall{ID: "n1", Name: "note", Input: json.RawMessage(`{"text":"fact X"}`)}), nil
			}
			return echoTask(req)
		}).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Notes) != 1 || res.Notes[0].Text != "fact X" || res.Notes[0].Author != res.Root.Plans[0].Subtasks[0].ID {
		t.Fatalf("Notes = %+v", res.Notes)
	}
	for _, c := range m.Calls(ethan.TagExecute) {
		if ethantest.Task(c) == "B" && !strings.Contains(ethantest.Prompt(c), "fact X") {
			t.Error("B's prompt does not include the note")
		}
	}
	if !slices.ContainsFunc(res.Events, func(e ethan.Event) bool { return e.Kind == ethan.EventNoteAdded }) {
		t.Error("no note_added event")
	}
}

func TestSubtasksRunOnTheirAssignedAgent(t *testing.T) {
	control := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("p",
			sub{ID: "a", Description: "A", Agent: "writer"},
			sub{ID: "b", Description: "B", Agent: "coder"},
			sub{ID: "c", Description: "C"},
		))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)
	coderModel := ethantest.New().On(ethan.TagExecute, echoTask)

	res, err := ethan.New(control, ethan.WithAgents(
		ethan.Agent{Name: "writer", Description: "writes"},
		ethan.Agent{Name: "coder", Description: "codes", Instructions: "You write Go.", Model: coderModel},
	)).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	calls := coderModel.Calls(ethan.TagExecute)
	if len(calls) != 1 || ethantest.Task(calls[0]) != "B" || !strings.HasPrefix(calls[0].System, "You write Go.") {
		t.Errorf("coder calls = %+v", calls)
	}
	agents := []string{}
	for _, st := range res.Root.Plans[0].Subtasks {
		agents = append(agents, st.Agent)
	}
	if want := []string{"writer", "coder", "writer"}; !slices.Equal(agents, want) {
		t.Errorf("agents = %v, want %v", agents, want)
	}
	if res.Usage.Total() == 0 {
		t.Error("usage missing")
	}
}

func TestObserverSeesTheExploration(t *testing.T) {
	var mu sync.Mutex
	var kinds []ethan.EventKind
	obs := ethan.ObserverFunc(func(e ethan.Event) {
		mu.Lock()
		kinds = append(kinds, e.Kind)
		mu.Unlock()
	})
	m := ethantest.New().
		On(ethan.TagPlan, threePlans()).
		On(ethan.TagScorePlan, scoreBy(map[string]float64{"plan-low": 0.1, "plan-high": 0.9, "plan-mid": 0.5})).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults).
		On(ethan.TagScoreResult, scoreBy(map[string]float64{"out(HIGH)": 0.3, "out(MID)": 0.8}))

	res, err := ethan.New(m, ethan.WithObserver(obs)).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if kinds[0] != ethan.EventRunStarted || kinds[len(kinds)-1] != ethan.EventRunDone {
		t.Errorf("events start with %s and end with %s", kinds[0], kinds[len(kinds)-1])
	}
	for _, k := range []ethan.EventKind{
		ethan.EventTaskDecided, ethan.EventPlanScored, ethan.EventPlanStarted, ethan.EventPlanDone,
		ethan.EventResultScored, ethan.EventPlanPruned, ethan.EventPlanChosen,
	} {
		if !slices.Contains(kinds, k) {
			t.Errorf("missing %s", k)
		}
	}
	if len(res.Events) != len(kinds) {
		t.Errorf("Result.Events has %d events, observer saw %d", len(res.Events), len(kinds))
	}
}

func TestWriteTraceProducesJSON(t *testing.T) {
	m := ethantest.New().On(ethan.TagPlan, ethantest.Atomic()).On(ethan.TagExecute, ethantest.ReplyText("done"))
	res, err := ethan.New(m).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := res.WriteTrace(&buf); err != nil {
		t.Fatal(err)
	}
	var trace struct {
		Goal   string            `json:"goal"`
		Output string            `json:"output"`
		Root   ethan.Task        `json:"root"`
		Events []json.RawMessage `json:"events"`
		Usage  ethan.Usage       `json:"usage"`
	}
	if err := json.Unmarshal(buf.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.Goal != "goal" || trace.Output != "done" || trace.Root.ID != "t0" || len(trace.Events) == 0 || trace.Usage.Total() == 0 {
		t.Errorf("trace = %+v", trace)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"notes": []`)) {
		t.Error("a run without notes must write an empty list, not null")
	}
}

// kindJournal fails on events of one kind.
type kindJournal struct{ fail ethan.EventKind }

func (j kindJournal) Append(e ethan.Event) error {
	if e.Kind == j.fail {
		return errors.New("disk full")
	}
	return nil
}

func TestRunDoneThatCannotBeJournaledIsObservedAsAFailure(t *testing.T) {
	m := ethantest.New().On(ethan.TagPlan, ethantest.Atomic()).On(ethan.TagExecute, echoTask)
	var last ethan.Event
	res, err := ethan.New(m).Run(context.Background(), "goal",
		ethan.JournalTo(kindJournal{fail: ethan.EventRunDone}),
		ethan.ObserveWith(ethan.ObserverFunc(func(e ethan.Event) { last = e })))
	if err == nil {
		t.Fatal("no error")
	}
	if end := res.Events[len(res.Events)-1]; end.Kind != ethan.EventRunFailed || last.Kind != ethan.EventRunFailed {
		t.Errorf("last event recorded %s, observed %s; want run_failed", end.Kind, last.Kind)
	}
	if replayed, err := ethan.Replay(res.Events); err != nil || replayed.Error == "" {
		t.Errorf("Replay: %v, Error %q", err, replayed.Error)
	}
}

func TestObserveWithSeesOnlyItsRun(t *testing.T) {
	m := ethantest.New().On(ethan.TagPlan, ethantest.Atomic()).On(ethan.TagExecute, echoTask)
	engine := ethan.New(m)
	var mu sync.Mutex
	goals := map[string]int{}
	var wg sync.WaitGroup
	for _, goal := range []string{"one", "two"} {
		wg.Go(func() {
			engine.Run(context.Background(), goal, ethan.ObserveWith(ethan.ObserverFunc(func(e ethan.Event) {
				if e.Kind == ethan.EventRunStarted {
					mu.Lock()
					goals[goal+":"+e.Text]++
					mu.Unlock()
				}
			})))
		})
	}
	wg.Wait()
	if len(goals) != 2 || goals["one:one"] != 1 || goals["two:two"] != 1 {
		t.Errorf("run_started observed as %v", goals)
	}
}

func TestObserversCannotChangeTheTree(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(
			plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B", DependsOn: []string{"a"}}),
			plan("q", sub{ID: "c", Description: "C"}))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagScorePlan, ethantest.ReplyJSON(ethan.Score{Value: 0.5, Reason: "fine"})).
		On(ethan.TagSynthesize, joinResults)
	vandal := ethan.ObserverFunc(func(e ethan.Event) {
		if e.Score != nil {
			e.Score.Reason = "changed"
		}
		for _, p := range e.Plans {
			for i := range p.Subtasks {
				p.Subtasks[i].DependsOn = nil
			}
		}
	})
	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1}),
		ethan.WithStrategy(ethan.BeamStrategy{Width: 1}), ethan.WithObserver(vandal)).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if s := res.Root.Plans[0].PlanScore; s == nil || s.Reason != "fine" {
		t.Errorf("plan score = %+v", s)
	}
	if deps := res.Root.Plans[0].Subtasks[1].DependsOn; len(deps) != 1 {
		t.Errorf("B depends on %v", deps)
	}
}

func TestNegativeNotesInPromptShowsNone(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B", DependsOn: []string{"a"}}))).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			last := req.Messages[len(req.Messages)-1]
			if ethantest.Task(req) == "A" && last.Parts[0].ToolResult == nil {
				return ethantest.ToolCalls(ethan.ToolCall{ID: "n1", Name: "note", Input: json.RawMessage(`{"text":"fact X"}`)}), nil
			}
			return echoTask(req)
		}).
		On(ethan.TagSynthesize, joinResults)
	if _, err := ethan.New(m, ethan.WithNotesInPrompt(-1)).Run(context.Background(), "goal"); err != nil {
		t.Fatal(err)
	}
	for _, c := range m.Calls(ethan.TagExecute) {
		if strings.Contains(ethantest.Prompt(c), "fact X") {
			t.Error("a prompt shows the note")
		}
	}
}

func TestEachObserverGetsItsOwnCopy(t *testing.T) {
	m := ethantest.New().On(ethan.TagPlan, ethantest.Atomic()).On(ethan.TagExecute, echoTask)
	first := ethan.ObserverFunc(func(e ethan.Event) {
		if e.Usage != nil {
			e.Usage.InputTokens = -1
		}
	})
	var seen []int
	second := ethan.ObserverFunc(func(e ethan.Event) {
		if e.Usage != nil {
			seen = append(seen, e.Usage.InputTokens)
		}
	})
	res, err := ethan.New(m, ethan.WithObserver(first), ethan.WithObserver(second)).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(seen, -1) {
		t.Errorf("the second observer saw the first one's change: %v", seen)
	}
	for _, e := range res.Events {
		if e.Usage != nil && e.Usage.InputTokens < 0 {
			t.Errorf("a recorded %s event was changed", e.Kind)
		}
	}
}

// judgingModel asks a judge about each answer within its own call.
type judgingModel struct{ inner, judge ethan.Model }

func (m judgingModel) Generate(ctx context.Context, req ethan.Request) (*ethan.Response, error) {
	resp, err := m.inner.Generate(ctx, req)
	if err != nil || req.Tag != ethan.TagExecute {
		return resp, err
	}
	if _, err := (&ethan.LLMEvaluator{Model: m.judge}).ScoreResult(ctx, ethan.Brief{}, resp.Text()); err != nil {
		return nil, err
	}
	return resp, nil
}

func TestModelCallingAComponentWithinItsCallDoesNotDeadlock(t *testing.T) {
	judge := ethantest.New().On(ethan.TagScoreResult, ethantest.ReplyJSON(ethan.Score{Value: 1}))
	inner := ethantest.New().On(ethan.TagPlan, ethantest.Atomic()).On(ethan.TagExecute, echoTask)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := ethan.New(judgingModel{inner, judge}, ethan.WithLimits(ethan.Limits{MaxConcurrency: 1})).Run(ctx, "goal")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(judge.Calls(ethan.TagScoreResult)); n != 1 || res.Usage.Total() != 3*ethantest.DefaultUsage.Total() {
		t.Errorf("judged %d times, spent %d", n, res.Usage.Total())
	}
}

// nestedRunModel runs another engine within each of its execute calls.
type nestedRunModel struct {
	inner ethan.Model
	other *ethan.Engine
}

func (m nestedRunModel) Generate(ctx context.Context, req ethan.Request) (*ethan.Response, error) {
	if req.Tag == ethan.TagExecute {
		if _, err := m.other.Run(ctx, "nested"); err != nil {
			return nil, err
		}
	}
	return m.inner.Generate(ctx, req)
}

func TestARunWithinAModelCallKeepsItsOwnConcurrencyLimit(t *testing.T) {
	var mu sync.Mutex
	inFlight, peak := 0, 0
	slow := func(req ethan.Request) (*ethan.Response, error) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return echoTask(req)
	}
	nested := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}))).
		On(ethan.TagExecute, slow).
		On(ethan.TagSynthesize, joinResults)
	other := ethan.New(nested, ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxConcurrency: 1}))
	outer := ethantest.New().On(ethan.TagPlan, ethantest.Atomic()).On(ethan.TagExecute, echoTask)
	if _, err := ethan.New(nestedRunModel{outer, other}).Run(context.Background(), "goal"); err != nil {
		t.Fatal(err)
	}
	if peak != 1 {
		t.Errorf("the nested run had %d calls in flight, want 1", peak)
	}
}
