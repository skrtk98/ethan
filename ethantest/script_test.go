package ethantest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
)

func TestScriptedPlanAndExecution(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(ethan.PlanProposal{Subtasks: []ethan.SubtaskProposal{
			{ID: "a", Description: "find A"}, {ID: "b", Description: "find B"},
		}})).
		On(ethan.TagExecute, ethantest.ByTask(map[string]ethantest.Handler{
			"find A": ethantest.ReplyText("A"),
			"find B": ethantest.ReplyText("B"),
		})).
		On(ethan.TagSynthesize, func(req ethan.Request) (*ethan.Response, error) {
			if !ethantest.IsGoal(req) || ethantest.Task(req) != "goal" {
				t.Errorf("synthesis is for %q", ethantest.Task(req))
			}
			return ethantest.Text("A and B"), nil
		})

	res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1})).Run(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "A and B" || len(m.Calls(ethan.TagExecute)) != 2 {
		t.Errorf("output %q after %d executions", res.Output, len(m.Calls(ethan.TagExecute)))
	}
}

func TestSequenceAndByTaskFailOutsideTheScript(t *testing.T) {
	seq := ethantest.Sequence(ethantest.ReplyText("once"))
	if r, err := seq(ethan.Request{}); err != nil || r.Text() != "once" {
		t.Fatalf("first = %v, %v", r, err)
	}
	if _, err := seq(ethan.Request{}); err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Errorf("second err = %v", err)
	}
	if _, err := ethantest.ByTask(nil)(ethan.Request{}); err == nil {
		t.Error("ByTask answered an unknown task")
	}
}
