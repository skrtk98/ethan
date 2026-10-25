package ethan_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
)

func TestRunExecutesAtomicGoalDirectly(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Atomic()).
		On(ethan.TagExecute, ethantest.ReplyText("42")).
		On(ethan.TagScoreResult, ethantest.ReplyJSON(ethan.Score{Value: 0.9, Reason: "ok"}))

	res, err := ethan.New(m).Run(context.Background(), "What is 6*7?")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "42" {
		t.Errorf("Output = %q, want 42", res.Output)
	}
	if !res.Root.Atomic || res.Root.Status != ethan.StatusDone {
		t.Errorf("root = %+v, want atomic and done", res.Root)
	}
	if got := ethantest.Prompt(m.Calls(ethan.TagExecute)[0]); !strings.Contains(got, "What is 6*7?") {
		t.Errorf("execute prompt %q does not contain the goal", got)
	}
	if res.Usage.Total() == 0 {
		t.Error("usage was not accounted")
	}
}

type echoTool struct{}

func (echoTool) Spec() ethan.ToolSpec {
	return ethan.ToolSpec{Name: "echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (echoTool) Call(_ context.Context, in json.RawMessage) (string, error) {
	return "echo:" + string(in), nil
}

func TestAgentRunsToolLoop(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Atomic()).
		On(ethan.TagScoreResult, ethantest.ReplyJSON(ethan.Score{Value: 1})).
		On(ethan.TagExecute, ethantest.Sequence(
			ethantest.Reply(ethantest.ToolCalls(ethan.ToolCall{ID: "c1", Name: "echo", Input: json.RawMessage(`"hi"`)})),
			func(req ethan.Request) (*ethan.Response, error) {
				last := req.Messages[len(req.Messages)-1]
				return ethantest.Text("got " + last.Parts[0].ToolResult.Content), nil
			},
		))

	agent := ethan.Agent{Name: "a", Tools: []ethan.Tool{echoTool{}}}
	res, err := ethan.New(m, ethan.WithAgents(agent)).Run(context.Background(), "say hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != `got echo:"hi"` {
		t.Errorf("Output = %q", res.Output)
	}
}

func TestRunFailsWhenExecutionFails(t *testing.T) {
	m := ethantest.New().On(ethan.TagPlan, ethantest.Atomic()) // no execute handler

	res, err := ethan.New(m).Run(context.Background(), "goal")
	if err == nil {
		t.Fatal("want error")
	}
	if res == nil || res.Root.Status != ethan.StatusFailed {
		t.Errorf("want failed root in result, got %+v", res)
	}
}

// nilModel answers every call with neither a response nor an error.
type nilModel struct{}

func (nilModel) Generate(context.Context, ethan.Request) (*ethan.Response, error) { return nil, nil }

func TestAModelReturningNothingFailsTheRun(t *testing.T) {
	res, err := ethan.New(nilModel{}).Run(context.Background(), "goal")
	if err == nil || !strings.Contains(err.Error(), "neither a response nor an error") {
		t.Fatalf("err = %v", err)
	}
	if res.Root.Status != ethan.StatusFailed {
		t.Errorf("root is %s", res.Root.Status)
	}
}

type namedTool struct{ name string }

func (t namedTool) Spec() ethan.ToolSpec { return ethan.ToolSpec{Name: t.name} }

func (namedTool) Call(context.Context, json.RawMessage) (string, error) { panic("boom") }

func TestNewPanicsOnMisconfiguration(t *testing.T) {
	m := ethantest.New()
	cases := map[string]func(){
		"nil model":      func() { ethan.New(nil) },
		"repeated agent": func() { ethan.New(m, ethan.WithAgents(ethan.Agent{Name: "a"}, ethan.Agent{Name: "a"})) },
		"nil tool":       func() { ethan.New(m, ethan.WithAgents(ethan.Agent{Name: "a", Tools: []ethan.Tool{nil}})) },
		"unnamed tool":   func() { ethan.New(m, ethan.WithAgents(ethan.Agent{Name: "a", Tools: []ethan.Tool{namedTool{}}})) },
		"reserved name":  func() { ethan.New(m, ethan.WithAgents(ethan.Agent{Name: "a", Tools: []ethan.Tool{namedTool{"note"}}})) },
		"repeated tool": func() {
			ethan.New(m, ethan.WithAgents(ethan.Agent{Name: "a", Tools: []ethan.Tool{namedTool{"x"}, namedTool{"x"}}}))
		},
	}
	for name, f := range cases {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}

func TestPanickingToolFailsItsCallOnly(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, ethantest.Atomic()).
		On(ethan.TagExecute, ethantest.Sequence(
			ethantest.Reply(ethantest.ToolCalls(ethan.ToolCall{ID: "c1", Name: "boom", Input: json.RawMessage(`{}`)})),
			func(req ethan.Request) (*ethan.Response, error) {
				r := req.Messages[len(req.Messages)-1].Parts[0].ToolResult
				return ethantest.Text(fmt.Sprintf("%v %s", r.IsError, r.Content)), nil
			},
		))
	agent := ethan.Agent{Name: "a", Tools: []ethan.Tool{namedTool{"boom"}}}
	res, err := ethan.New(m, ethan.WithAgents(agent)).Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "true tool panicked: boom" {
		t.Errorf("Output = %q", res.Output)
	}
}

func TestIntoNilFailsTheRun(t *testing.T) {
	m := ethantest.New()
	res, err := ethan.New(m).Run(context.Background(), "goal", ethan.Into[struct{ A string }](nil))
	if err == nil || res == nil || len(m.Calls(ethan.TagPlan)) != 0 {
		t.Errorf("err = %v, %d plan calls; want an error before any call", err, len(m.Calls(ethan.TagPlan)))
	}
}

// foreignStrategy adopts a plan that belongs to no task.
type foreignStrategy struct{}

func (foreignStrategy) Explore(ctx context.Context, x ethan.Exploration) (*ethan.Plan, error) {
	return &ethan.Plan{ID: "bogus", Status: ethan.StatusDone, Output: "x"}, nil
}

func TestStrategyCannotAdoptAForeignPlan(t *testing.T) {
	m := ethantest.New().On(ethan.TagPlan, ethantest.Decompose(plan("p", sub{ID: "a", Description: "A"})))
	res, err := ethan.New(m, ethan.WithStrategy(foreignStrategy{})).Run(context.Background(), "goal")
	if err == nil {
		t.Fatal("no error")
	}
	if _, err := ethan.Replay(res.Events); err != nil {
		t.Errorf("the journal cannot be replayed: %v", err)
	}
}
