package ethan_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
)

func TestJournalRecordsEveryEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	j, err := ethan.CreateJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B", DependsOn: []string{"a"}}))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1})).Run(context.Background(), "goal", ethan.JournalTo(j))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := ethan.ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(res.Events) || events[0].Kind != ethan.EventRunStarted || events[0].Text != "goal" {
		t.Fatalf("journal has %d events starting with %+v; the run emitted %d", len(events), events[0], len(res.Events))
	}
	for _, e := range events {
		if e.Kind != ethan.EventTaskDecided || e.TaskID != "t0" {
			continue
		}
		b := e.Plans[0].Subtasks[1]
		if b.ID != "t0.p1.t2" || b.Description != "B" || b.DependsOn[0] != "t0.p1.t1" {
			t.Errorf("decision records subtask %+v, want the tree's IDs", b)
		}
	}
	last := events[len(events)-1]
	if last.Kind != ethan.EventRunDone || last.Usage == nil || last.Usage.Total() != res.Usage.Total() {
		t.Errorf("last event = %+v, want run_done with the usage %d", last, res.Usage.Total())
	}
}

func TestTaskUsageCountsItsOwnCalls(t *testing.T) {
	// The root plans and synthesizes (2 calls); A and B execute (1 call each).
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B"}))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)

	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1})).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	call := ethantest.DefaultUsage.Total()
	if got := res.Root.Usage.Total(); got != 2*call {
		t.Errorf("root used %d, want %d", got, 2*call)
	}
	if got := res.Root.Plans[0].Subtasks[0].Usage.Total(); got != call {
		t.Errorf("A used %d, want %d", got, call)
	}
}

func TestTornLastLineIsDroppedOnReadAndAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	torn := `{"kind":"run_started","time":"2026-10-18T00:00:00Z","text":"goal"}` + "\n" + `{"kind":"task_sta`
	if err := os.WriteFile(path, []byte(torn), 0o644); err != nil {
		t.Fatal(err)
	}
	if events, err := ethan.ReadJournal(path); err != nil || len(events) != 1 {
		t.Fatalf("read %d events, err %v; want the whole first line only", len(events), err)
	}
	j, err := ethan.CreateJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Append(ethan.Event{Kind: ethan.EventRunResumed}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	events, err := ethan.ReadJournal(path)
	if err != nil || len(events) != 2 || events[1].Kind != ethan.EventRunResumed {
		t.Errorf("after appending: %d events, err %v", len(events), err)
	}
}

// failingJournal fails from its nth event on.
type failingJournal struct{ n int }

func (j *failingJournal) Append(ethan.Event) error {
	if j.n--; j.n < 0 {
		return errors.New("disk full")
	}
	return nil
}

func TestJournalFailureCancelsTheRun(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(plan("p", sub{ID: "a", Description: "A"}, sub{ID: "b", Description: "B", DependsOn: []string{"a"}}))).
		On(ethan.TagExecute, echoTask).
		On(ethan.TagSynthesize, joinResults)

	_, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1})).
		Run(context.Background(), "goal", ethan.JournalTo(&failingJournal{n: 3}))
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want the journal's failure", err)
	}
	if n := len(m.Calls(ethan.TagExecute)); n > 1 {
		t.Errorf("executed %d subtasks after the journal failed", n)
	}
}

func TestNewJournalRefusesAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	j, err := ethan.NewJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	if _, err := ethan.NewJournal(path); !errors.Is(err, os.ErrExist) {
		t.Errorf("err = %v, want os.ErrExist", err)
	}
}
