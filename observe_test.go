package ethan_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

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
