package ethan

import "time"

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
	EventPlanScored   EventKind = "plan_scored"
	EventPlanStarted  EventKind = "plan_started"
	EventPlanDone     EventKind = "plan_done"
	EventPlanFailed   EventKind = "plan_failed"
	EventPlanPruned   EventKind = "plan_pruned"
	EventResultScored EventKind = "result_scored"
	EventPlanChosen   EventKind = "plan_chosen"
	EventNoteAdded    EventKind = "note_added"
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
	// Plans are the plans a task was decomposed into, on task_decided. Their
	// subtask IDs and dependencies are the IDs of the tree.
	Plans []PlanProposal `json:"plans,omitempty"`
	// Usage is the tokens used by a task's own model calls so far, on
	// task_done and task_failed, and by the Run, on run_done and run_failed.
	Usage *Usage `json:"usage,omitempty"`
}

// Observer receives the events of a Run. The engine delivers events one at
// a time, so implementations need not be safe for concurrent use, but they
// should return quickly.
type Observer interface {
	OnEvent(Event)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Event)

// OnEvent implements Observer.
func (f ObserverFunc) OnEvent(e Event) { f(e) }
