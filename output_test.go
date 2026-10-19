package ethan_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
)

type report struct {
	Title  string   `json:"title"`
	Points []string `json:"points"`
}

func formatting(m *ethantest.Model) *ethantest.Model {
	return m.On(ethan.TagFormat, func(req ethan.Request) (*ethan.Response, error) {
		if req.Output == nil || !strings.Contains(ethantest.Prompt(req), "<result>\nout(goal)\n</result>") {
			return nil, errors.New("not asked to format the output")
		}
		return ethantest.JSON(report{Title: "T", Points: []string{"a"}}), nil
	})
}

func TestIntoConvertsTheOutput(t *testing.T) {
	m := formatting(ethantest.New().On(ethan.TagPlan, ethantest.Atomic()).On(ethan.TagExecute, echoTask))
	var got report
	res, err := ethan.New(m).Run(context.Background(), "goal", ethan.Into(&got))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "T" || len(got.Points) != 1 || string(res.Value) != `{"title":"T","points":["a"]}` {
		t.Errorf("value %+v, Result.Value %s", got, res.Value)
	}
	if res.Output != "out(goal)" {
		t.Errorf("Output = %q", res.Output)
	}
	replayed, err := ethan.Replay(res.Events)
	if err != nil || string(replayed.Value) != string(res.Value) {
		t.Errorf("replayed value %s, %v", replayed.Value, err)
	}
}

func TestResumeRetriesOnlyAFailedConversion(t *testing.T) {
	m := ethantest.New().On(ethan.TagPlan, ethantest.Atomic()).On(ethan.TagExecute, echoTask) // no format handler
	var got report
	first, err := ethan.New(m).Run(context.Background(), "goal", ethan.Into(&got))
	if err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("err = %v", err)
	}
	if first.Root.Status != ethan.StatusDone {
		t.Errorf("root is %s", first.Root.Status)
	}

	m2 := formatting(ethantest.New())
	res, err := ethan.New(m2).Resume(context.Background(), first.Events, ethan.Into(&got))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "T" || len(m2.Calls(ethan.TagFormat)) != 1 || len(m2.Calls(ethan.TagExecute)) != 0 {
		t.Errorf("value %+v after %d format calls", got, len(m2.Calls(ethan.TagFormat)))
	}
	if string(res.Value) == "" {
		t.Error("no Result.Value")
	}
}

func TestResumingAConvertedRunReusesTheValue(t *testing.T) {
	m := formatting(ethantest.New().On(ethan.TagPlan, ethantest.Atomic()).On(ethan.TagExecute, echoTask))
	var got report
	first, err := ethan.New(m).Run(context.Background(), "goal", ethan.Into(&got))
	if err != nil {
		t.Fatal(err)
	}

	var again report
	none := ethantest.New()
	if _, err := ethan.New(none).Resume(context.Background(), first.Events, ethan.Into(&again)); err != nil || again.Title != "T" {
		t.Errorf("value %+v, err %v", again, err)
	}

	// Another type does not decode the journaled value: convert again.
	var other struct {
		Summary string `json:"summary"`
	}
	m3 := ethantest.New().On(ethan.TagFormat, ethantest.ReplyJSON(map[string]string{"summary": "S"}))
	if _, err := ethan.New(m3).Resume(context.Background(), first.Events, ethan.Into(&other)); err != nil || other.Summary != "S" {
		t.Errorf("value %+v, err %v", other, err)
	}
	if len(none.Calls(ethan.TagFormat))+len(m3.Calls(ethan.TagExecute)) != 0 || len(m3.Calls(ethan.TagFormat)) != 1 {
		t.Error("unexpected model calls")
	}
}

func TestIntoRejectsANonStructBeforeCallingAnyModel(t *testing.T) {
	m := ethantest.New()
	var s string
	if _, err := ethan.New(m).Run(context.Background(), "goal", ethan.Into(&s)); err == nil {
		t.Error("no error")
	}
	if len(m.Calls(ethan.TagPlan)) != 0 {
		t.Error("a model was called")
	}
}
