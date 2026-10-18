# ethan

**E.T.H.A.N.** — *Exploratory Task Hierarchy and Agent Network* — is a Go framework for exploratory orchestration of AI agents.

Given a goal, ethan grows an AND-OR task tree at runtime:

- A **task** (OR node) is either executed directly by an agent or solved by one of several alternative **plans** proposed by an LLM planner.
- A **plan** (AND node) splits the task into subtasks with dependencies. Independent subtasks run in parallel. When all of them finish, their results are synthesized into the task's result.
- A **strategy** decides which plans to try. The default beam strategy scores the candidates, executes the best ones in parallel, adopts the best-scoring result and backtracks to the remaining plans on failure.
- **Agents** with their own instructions, models and tools execute the leaves, and share findings through a per-run **blackboard**.
- **Limits** bound depth, the number of tasks, tokens and concurrent model calls. They are checked mechanically: going beyond the task or token limit cancels the whole run with an error wrapping `ErrLimitExceeded`. How to spend a budget is left to the caller.
- Every step is reported as an **event**. A **journal** records the events as they happen; `Replay` rebuilds the explored tree from it, and `Engine.Resume` continues a run that stopped, reusing every finished task, decision and score.

> Status: experimental. The API may change.

## Install

```sh
go get github.com/skrtk98/ethan
```

Requires Go 1.26 or later. The core package depends only on the standard library. The Claude adapter lives in `github.com/skrtk98/ethan/anthropic`.

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

## Research demo

[`examples/research`](examples/research) runs a web-searching researcher agent and an analyst agent on a question:

```sh
export ANTHROPIC_API_KEY=...
go run ./examples/research -o report.md -trace trace.json "What changed in Go's generics between 1.18 and 1.26?"
```

Progress is printed to stderr. `trace.json` contains the whole explored tree, including the rejected plans, scores, notes, events and token usage. Run `go run ./examples/research -h` for the limit and model flags.

## Journal and resume

```go
j, _ := ethan.NewJournal("run.jsonl") // fails if the file exists; CreateJournal appends
engine := ethan.New(model, ethan.WithLimits(ethan.Limits{MaxTokens: 300_000}))
res, err := engine.Run(ctx, goal, ethan.JournalTo(j))
if errors.Is(err, ethan.ErrLimitExceeded) {
	events, _ := ethan.ReadJournal("run.jsonl")
	res, err = engine.Resume(ctx, events, ethan.JournalTo(j)) // appends to the same file; limits apply to the resumed run alone
}
```

A journal holds one run and the resumes that continue it. `ethan.Replay(events)` rebuilds the `Result` of a journal without calling any model. The research demo takes `-journal run.jsonl` and `-resume`.

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

Prompts are embedded Go templates. `ethan.DefaultTemplate(name)` returns a copy you can modify and pass to a component.

Tools implement `Tool` (`Spec` and `Call`). Server-side tools such as web search are adapter options (`anthropic.WithWebSearch`).

## Testing

`ethantest.Model` is a scripted model that routes each request by its tag (`plan`, `execute`, `score_plan`, `score_result`, `synthesize`). Tests built on it are deterministic and need no network:

```sh
go test -race ./...
go test -tags live ./anthropic   # calls the real API
```

## Documentation

Design notes are written in Japanese: [Concepts.md](Concepts.md), [GLOSSARY.md](GLOSSARY.md) and [docs/adr](docs/adr).

## License

MIT
