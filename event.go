package ethan

import (
	"encoding/json"
	"time"
)

// EventKind classifies an Event.
type EventKind string

const (
	EventRunStarted   EventKind = "run_started" // Text is the goal
	EventRunResumed   EventKind = "run_resumed"
	EventRunDone      EventKind = "run_done"
	EventRunFailed    EventKind = "run_failed"
	EventTaskStarted  EventKind = "task_started"
	EventTaskDecided  EventKind = "task_decided" // Text is "atomic" or the number of plans
	EventTaskDone     EventKind = "task_done"
	EventTaskFailed   EventKind = "task_failed"
	EventTaskPruned   EventKind = "task_pruned" // Text is why the task was not started
	EventPlanScored   EventKind = "plan_scored" // without Score if scoring failed
	EventPlanStarted  EventKind = "plan_started"
	EventPlanDone     EventKind = "plan_done"
	EventPlanFailed   EventKind = "plan_failed"
	EventPlanPruned   EventKind = "plan_pruned"
	EventResultScored EventKind = "result_scored" // without Score if scoring failed
	EventPlanChosen   EventKind = "plan_chosen"
	EventNoteAdded    EventKind = "note_added"
	EventUsage        EventKind = "usage" // a model call's usage; Text is the request's tag
)

// Event reports progress of a Run. The events of a Run, in order, are
// enough to rebuild its tree: see Replay and Engine.Resume.
type Event struct {
	Kind   EventKind `json:"kind"`
	Time   time.Time `json:"time"`
	TaskID string    `json:"task_id,omitempty"`
	PlanID string    `json:"plan_id,omitempty"`
	Score  *Score    `json:"score,omitempty"`
	// Text carries the goal, task description, error, note or output,
	// depending on Kind.
	Text string `json:"text,omitempty"`
	// Reason is why planning failed, on an atomic task_decided, or why
	// scoring failed, on plan_scored and result_scored without a Score.
	Reason string `json:"reason,omitempty"`
	// Plans are the plans a task was decomposed into, on task_decided. Their
	// subtask IDs and dependencies are the IDs of the tree.
	Plans []PlanProposal `json:"plans,omitempty"`
	// Value is the output converted as Into asked, on run_done.
	Value json.RawMessage `json:"value,omitempty"`
	// Agent is the root task's agent, on run_started.
	Agent string `json:"agent,omitempty"`
	// Usage is the tokens used by one model call made for the task, on
	// usage; by the task's own model calls so far, on task_done and
	// task_failed; and by the Run and those it resumed, on run_done and
	// run_failed. Replay counts the usage events.
	Usage *Usage `json:"usage,omitempty"`
}

// Observer receives the events of a Run. The engine delivers events one at
// a time and in journal order, so implementations need not be safe for
// concurrent use. The Run waits while an observer runs: one doing slow work,
// such as writing to the network, should hand events to its own goroutine,
// e.g. through a channel. Observers cannot emit events, so delivery cannot
// deadlock.
type Observer interface {
	OnEvent(Event)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Event)

// OnEvent implements Observer.
func (f ObserverFunc) OnEvent(e Event) { f(e) }
