// Package ethan (Exploratory Task Hierarchy and Agent Network) orchestrates
// AI agents on exploratory tasks: tasks whose decomposition is not known in
// advance, so plans must be tried, compared and abandoned.
//
// Given a goal, an Engine grows an AND-OR task tree at runtime. A Task is
// either executed directly by an Agent or solved by one of several
// alternative Plans that a Planner proposes; a Plan splits the task into
// subtasks with dependencies, runs the independent ones in parallel and
// has a Synthesizer merge their results. A Strategy decides which plans to
// try; the default BeamStrategy has an Evaluator score them, runs the best
// ones and backtracks to the others on failure.
//
// Limits bound the depth, the number of tasks, the tokens spent and the
// model calls in flight; going beyond the task or token limit cancels the
// whole Run with an error wrapping ErrLimitExceeded. Every step is reported
// as an Event. A Journal records the events, from which Replay rebuilds the
// explored tree and Engine.Resume continues a Run that stopped.
//
// The package depends only on the standard library and calls models
// through the Model interface. Adapters for providers are modules of their
// own: github.com/skrtk98/ethan/anthropic and github.com/skrtk98/ethan/openai.
// Package ethantest provides a scripted Model for deterministic tests.
package ethan
