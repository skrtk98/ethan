# Changelog

All notable changes are listed here. The project follows [Semantic Versioning](https://semver.org/); before v1, minor versions may break the API. The core module and the adapter modules (`anthropic`, `openai`) are tagged separately (`vX.Y.Z`, `anthropic/vX.Y.Z`, `openai/vX.Y.Z`).

## Unreleased

The first release.

- AND-OR exploration: an LLM planner proposes alternative plans; `BeamStrategy` scores them, runs the best in parallel and backtracks on failure; agents with their own models and tools execute the leaves and share notes on a blackboard.
- `Limits` on depth, tasks, tokens and concurrent model calls. Going beyond the task or token limit cancels the whole run with `ErrLimitExceeded`. `MaxTokens` defaults to `DefaultMaxTokens` (300,000); a negative value lifts it.
- Events, observers (`WithObserver`, `ObserveWith`) and a JSON Lines journal (`CreateJournal`, `OpenJournal`) with `Replay` and `Engine.Resume`. Journals record their format (`JournalFormat` 1); a failed run records a machine-readable `Code`.
- Typed I/O: `SchemaFor` derives strict JSON schemas from Go types, `FuncTool` makes a tool from a function, and `Into` converts the goal's output into a Go value.
- `WithTemplate` replaces any built-in prompt.
- Model adapters for Claude (`anthropic`) and the OpenAI Responses API (`openai`, also Ollama), each a module of its own. Truncated responses fail with `ErrTruncated`, refusals with `ErrRefused`.
- `ethantest`: a scripted model for deterministic tests and `TestModel`, a conformance test for adapters.
