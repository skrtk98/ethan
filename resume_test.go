package ethan_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
)

// chain decomposes the root into A, B after A, and C after B.
func chain() *ethantest.Model {
	return ethantest.New().
		On(ethan.TagPlan, decomposeRoot(plan("p",
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
	_, err = ethan.New(chain(), ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTokens: 40}), ethan.WithJournal(j)).
		Run(context.Background(), "goal")
	if !errors.Is(err, ethan.ErrTokenLimit) {
		t.Fatalf("first run: err = %v, want ErrTokenLimit", err)
	}

	events, err := ethan.ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	m := chain()
	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1}), ethan.WithJournal(j)).Resume(context.Background(), events)
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
		executed = append(executed, taskOf(req))
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
	if _, err := ethan.Replay([]ethan.Event{{Kind: ethan.EventRunStarted, Text: "g"}, {Kind: ethan.EventTaskDone, TaskID: "t9"}}); err == nil {
		t.Error("an unknown task was accepted")
	}
}
