package ethan_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
)

func atomicPlan() ethantest.Handler {
	return ethantest.ReplyJSON(map[string]any{"atomic": true})
}

func TestRunExecutesAtomicGoalDirectly(t *testing.T) {
	m := ethantest.New().
		On(ethan.TagPlan, atomicPlan()).
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
		On(ethan.TagPlan, atomicPlan()).
		On(ethan.TagScoreResult, ethantest.ReplyJSON(ethan.Score{Value: 1})).
		On(ethan.TagExecute, func(req ethan.Request) (*ethan.Response, error) {
			last := req.Messages[len(req.Messages)-1]
			if r := last.Parts[0].ToolResult; r != nil {
				return ethantest.Text("got " + r.Content), nil
			}
			return &ethan.Response{Parts: []ethan.Part{{ToolCall: &ethan.ToolCall{
				ID: "c1", Name: "echo", Input: json.RawMessage(`"hi"`),
			}}}}, nil
		})

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
	m := ethantest.New().On(ethan.TagPlan, atomicPlan()) // no execute handler

	res, err := ethan.New(m).Run(context.Background(), "goal")
	if err == nil {
		t.Fatal("want error")
	}
	if res == nil || res.Root.Status != ethan.StatusFailed {
		t.Errorf("want failed root in result, got %+v", res)
	}
}
