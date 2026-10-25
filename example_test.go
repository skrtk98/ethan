package ethan_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
)

// The examples run on ethantest's scripted model. In a program, pass a
// provider's adapter instead, such as anthropic.New().

func Example() {
	model := ethantest.New().
		On(ethan.TagPlan, ethantest.Decompose(ethan.PlanProposal{Subtasks: []ethan.SubtaskProposal{
			{ID: "pros", Description: "List the pros"},
			{ID: "cons", Description: "List the cons"},
		}})).
		On(ethan.TagExecute, ethantest.ByTask(map[string]ethantest.Handler{
			"List the pros": ethantest.ReplyText("cheap"),
			"List the cons": ethantest.ReplyText("slow"),
		})).
		On(ethan.TagSynthesize, ethantest.ReplyText("It is cheap but slow."))

	engine := ethan.New(model, ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTokens: 100_000}))
	res, err := engine.Run(context.Background(), "Weigh the option.")
	if errors.Is(err, ethan.ErrLimitExceeded) {
		log.Fatal("over budget; resume from the journal to continue")
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Output)
	// Output: It is cheap but slow.
}

func ExampleInto() {
	model := ethantest.New().
		On(ethan.TagPlan, ethantest.Atomic()).
		On(ethan.TagExecute, ethantest.ReplyText("Paris is the capital of France.")).
		On(ethan.TagFormat, ethantest.ReplyJSON(map[string]string{"city": "Paris", "country": "France"}))

	var answer struct {
		City    string `json:"city"`
		Country string `json:"country" desc:"the country whose capital the city is"`
	}
	if _, err := ethan.New(model).Run(context.Background(), "What is the capital of France?", ethan.Into(&answer)); err != nil {
		log.Fatal(err)
	}
	fmt.Println(answer.City, answer.Country)
	// Output: Paris France
}

func ExampleFuncTool() {
	type query struct {
		City string `json:"city" desc:"the city to look up"`
	}
	weather := ethan.FuncTool("weather", "Reports the weather in a city.", func(ctx context.Context, q query) (string, error) {
		return "sunny in " + q.City, nil
	})
	fmt.Println(weather.Spec().Name, string(weather.Spec().InputSchema))
	// Output: weather {"additionalProperties":false,"properties":{"city":{"description":"the city to look up","type":"string"}},"required":["city"],"type":"object"}
}

func ExampleObserveWith() {
	model := ethantest.New().On(ethan.TagPlan, ethantest.Atomic()).On(ethan.TagExecute, ethantest.ReplyText("done"))
	progress := ethan.ObserverFunc(func(e ethan.Event) {
		if e.Kind != ethan.EventUsage {
			fmt.Println(strings.TrimSpace(string(e.Kind) + " " + e.TaskID))
		}
	})
	if _, err := ethan.New(model).Run(context.Background(), "goal", ethan.ObserveWith(progress)); err != nil {
		log.Fatal(err)
	}
	// Output:
	// run_started
	// task_started t0
	// task_decided t0
	// task_done t0
	// run_done
}
