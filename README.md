# ethan

**E.T.H.A.N.** — *Exploratory Task Hierarchy and Agent Network* — is a Go framework for exploratory orchestration of AI agents.

Given a goal, ethan grows an AND-OR task tree at runtime:

- A **task** (OR node) is either executed directly by an agent or solved by one of several alternative **plans** proposed by an LLM planner.
- A **plan** (AND node) splits the task into subtasks with dependencies. Independent subtasks run in parallel. When all of them finish, their results are synthesized into the task's result.
- A **strategy** decides which plans to try. The default beam strategy scores the candidates, executes the best ones in parallel, adopts the best-scoring result and backtracks to the remaining plans on failure.
- **Agents** with their own instructions, models and tools execute the leaves, and share findings through a per-run **blackboard**.
- **Limits** bound depth, the number of tasks, tokens and concurrent model calls. They are checked mechanically: going beyond the task or token limit cancels the whole run with an error wrapping `ErrLimitExceeded`. The token limit defaults to `DefaultMaxTokens` (300,000), so that a run left unconfigured cannot spend without bound; a negative `MaxTokens` lifts it. How to spend a budget is left to the caller.
- Every step is reported as an **event**. A **journal** records the events as they happen; `Replay` rebuilds the explored tree from it, and `Engine.Resume` continues a run that stopped, reusing every finished task, decision and score.

### Scope

ethan is for exploratory tasks: tasks whose decomposition is not known in advance, so plans must be tried, compared and abandoned. It encapsulates planning, execution and cancellation on failure, so that this part stays maintainable. Human approval will be requested on conditions, such as a tool judging its input risky, not at fixed places.

Fixed workflows, chat with tools, and approval at fixed steps are out of scope: write them in plain Go, calling `Run` as one step if needed. See [ADR 0007](docs/adr/0007-scope.md).

> Status: v0. The API may still change between minor versions; [CHANGELOG.md](CHANGELOG.md) lists what changed. Journals are versioned separately (see below).

## Install

```sh
go get github.com/skrtk98/ethan
go get github.com/skrtk98/ethan/anthropic   # or /openai
```

Requires Go 1.26 or later. The core module depends only on the standard library. Each model adapter is a module of its own, so you only download the SDK of the provider you use:

| Provider | Module | Notes |
|---|---|---|
| Claude | `github.com/skrtk98/ethan/anthropic` | reads `ANTHROPIC_API_KEY` |
| OpenAI | `github.com/skrtk98/ethan/openai` | Responses API; reads `OPENAI_API_KEY` |
| Ollama and other Responses API servers | `github.com/skrtk98/ethan/openai` | `openai.WithBaseURL("http://localhost:11434/v1/")`, `openai.WithAPIKey("ollama")` |

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/anthropic"
)

func main() {
	model := anthropic.New() // reads ANTHROPIC_API_KEY
	engine := ethan.New(model,
		ethan.WithLimits(ethan.Limits{MaxDepth: 2, MaxTokens: 500_000}),
	)
	res, err := engine.Run(context.Background(), "Compare the trade-offs of three approaches to rate limiting in Go services.")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Output)
}
```

To receive the result as a Go value, pass `Into`. The exploration still works in text; once it finishes, one more model call converts the output with the schema `SchemaFor` derives from the type:

```go
var report struct {
	Summary string   `json:"summary"`
	Sources []string `json:"sources" desc:"URLs the summary relies on"`
}
res, err := engine.Run(ctx, goal, ethan.Into(&report))
```

The value is journaled too, so `Replay` and `Resume` keep it; a Resume after a failed conversion only converts again.

## Research demo

[`examples/research`](examples/research) runs a web-searching researcher agent and an analyst agent on a question:

```sh
export ANTHROPIC_API_KEY=...
cd examples
go run ./research -o report.md -trace trace.json "What changed in Go's generics between 1.18 and 1.26?"
```

Add `-provider openai` to use OpenAI, or `-provider ollama` to use a local Ollama server (without web search). Progress is printed to stderr. `trace.json` contains the whole explored tree, including the rejected plans, scores, notes, events and token usage. Run `go run ./research -h` in `examples` for the limit and model flags.

## Journal and resume

```go
j, _ := ethan.CreateJournal("run.jsonl") // fails if the file exists; OpenJournal appends
engine := ethan.New(model, ethan.WithLimits(ethan.Limits{MaxTokens: 300_000}))
res, err := engine.Run(ctx, goal, ethan.JournalTo(j))
if errors.Is(err, ethan.ErrLimitExceeded) {
	events, _ := ethan.ReadJournal("run.jsonl")
	res, err = engine.Resume(ctx, events, ethan.JournalTo(j)) // appends to the same file; limits apply to the resumed run alone
}
```

A journal holds one run and the resumes that continue it. `ethan.Replay(events)` rebuilds the `Result` of a journal without calling any model. The research demo takes `-journal run.jsonl` and `-resume`.

The first event records the journal's format. `Replay` and `Resume` reject a journal written in a format other than `JournalFormat` with an error wrapping `ErrJournalFormat`, and events that do not describe a run with `ErrInvalidJournal`. A failed run ends with a `run_failed` event whose `Code` (`"token_limit"`, `"task_limit"`, `"canceled"`, ...) tells whether resuming makes sense without parsing the message.

## Observing

Every step is an `Event`, delivered in journal order. `WithObserver` registers an observer for every run of an engine, which must then be safe for concurrent use if runs overlap; `ObserveWith` registers one for a single run:

```go
progress := ethan.ObserverFunc(func(e ethan.Event) { log.Println(e.Kind, e.TaskID, e.Error) })
res, err := engine.Run(ctx, goal, ethan.ObserveWith(progress))
```

`Result.WriteTrace` writes the explored tree, events included, as JSON for inspection. Unlike the journal, its form is not versioned.

## Customizing

| Component     | Interface     | Default                                | Option              |
|---------------|---------------|----------------------------------------|---------------------|
| Model         | `Model`       | (required)                             | `New(model, ...)`   |
| Planner       | `Planner`     | `LLMPlanner`                           | `WithPlanner`       |
| Evaluator     | `Evaluator`   | `LLMEvaluator` (LLM as judge)          | `WithEvaluator`     |
| Synthesizer   | `Synthesizer` | `LLMSynthesizer`                       | `WithSynthesizer`   |
| Strategy      | `Strategy`    | `BeamStrategy` (2 plans at the root, 1 below) | `WithStrategy` |
| Agents        | `Agent`       | one generalist                         | `WithAgents`        |
| Observer      | `Observer`    | none                                   | `WithObserver`      |
| Journal       | `Journal`     | none (`FileJournal` for files)         | `JournalTo` (per run) |

The default beam runs two plans in parallel at the root, so a run costs roughly twice what one plan would; `WithStrategy(ethan.BeamStrategy{Width: 1})` trades that for less exploration. Every model call, those of your own `LLMPlanner` and agents' models included, counts against the run's limits.

A `Model` reports a response cut off at the output limit with an error wrapping `ethan.ErrTruncated`, and a refusal with `ethan.ErrRefused`; the engine never takes either for an answer.

Prompts are embedded Go templates. `ethan.DefaultTemplate(name)` returns a copy you can modify and pass to `ethan.WithTemplate(name, t)`; its documentation lists the data each template receives.

Each agent can have a model of its own, from any provider:

```go
engine := ethan.New(anthropic.New(), // plans, evaluates and synthesizes
	ethan.WithAgents(
		ethan.Agent{Name: "researcher", Description: "Searches the web.", Model: openai.New(openai.WithWebSearch(5))},
		ethan.Agent{Name: "analyst", Description: "Analyzes material it is given."}, // uses the engine's model
	),
)
```

Tools implement `Tool` (`Spec` and `Call`). `FuncTool` makes one from a function, deriving the input schema from its input struct with `SchemaFor`:

```go
type query struct {
	Text string `json:"text" desc:"what to search for"`
}

search := ethan.FuncTool("search", "Searches the web.", func(ctx context.Context, q query) (string, error) {
	return lookup(ctx, q.Text)
})
```

`New` panics on misconfigured agents: repeated names, and tools that are nil, unnamed, repeated or named `note`, which the engine adds for the blackboard. A tool that returns an error or panics fails that call only; the model sees the error as the tool's result.

Server-side tools such as web search are adapter options (`anthropic.WithWebSearch`, `openai.WithWebSearch`). The Claude adapter asks for prompt caching on every request (`anthropic.WithoutCache` turns it off); `Usage` reports cached input tokens in `CacheReadTokens` and `CacheWriteTokens`, which are part of `InputTokens`.

## Testing

`ethantest.Model` is a scripted model that routes each request by its tag (`plan`, `execute`, `score_plan`, `score_result`, `synthesize`). Tests built on it are deterministic and need no network:

```go
m := ethantest.New().
	On(ethan.TagPlan, ethantest.Decompose(ethan.PlanProposal{Subtasks: []ethan.SubtaskProposal{
		{ID: "a", Description: "find A"}, {ID: "b", Description: "find B"},
	}})).
	On(ethan.TagExecute, ethantest.ByTask(map[string]ethantest.Handler{
		"find A": ethantest.Sequence( // a tool loop: a call, then the answer
			ethantest.Reply(ethantest.ToolCalls(ethan.ToolCall{ID: "c1", Name: "search", Input: json.RawMessage(`{"q":"A"}`)})),
			ethantest.ReplyText("A"),
		),
		"find B": ethantest.ReplyText("B"),
	})).
	On(ethan.TagSynthesize, ethantest.ReplyText("A and B"))
```

```sh
go test -race ./...                       # the core module
(cd anthropic && go test -race ./...)     # each adapter is a module of its own
(cd openai && go test -race ./...)
(cd anthropic && go test -tags live .)    # calls the real API; likewise in openai, where OLLAMA_MODEL=gpt-oss:20b adds Ollama
```

`ethantest.TestModel` checks that a model adapter maps requests and responses faithfully; each adapter runs it against a fake of its provider's API.

## Security

Task descriptions, tool results and blackboard notes go into prompts as they are, so a web page or document an agent reads can try to steer it (prompt injection). Give agents only the tools their tasks need, and check inputs of tools with side effects in the tool itself. Condition-based human approval of tool calls is planned ([ADR 0007](docs/adr/0007-scope.md)).

## Documentation

Design notes are written in Japanese: [Concepts.md](Concepts.md), [GLOSSARY.md](GLOSSARY.md) and [docs/adr](docs/adr).

## License

MIT
