package ethan

import (
	"encoding/json"
	"slices"
	"time"
)

// EventKind classifies an Event. New kinds may be added.
type EventKind string

const (
	EventRunStarted   EventKind = "run_started" // Text is the goal; Format is JournalFormat
	EventRunResumed   EventKind = "run_resumed"
	EventRunDone      EventKind = "run_done"
	EventRunFailed    EventKind = "run_failed"   // with Error and Code
	EventTaskStarted  EventKind = "task_started" // Text is the task's description
	EventTaskDecided  EventKind = "task_decided" // with Plans, or none and Reason if executed directly
	EventTaskDone     EventKind = "task_done"    // Text is the output
	EventTaskFailed   EventKind = "task_failed"  // with Error
	EventTaskPruned   EventKind = "task_pruned"  // Reason is why the task was not started
	EventPlanScored   EventKind = "plan_scored"  // without Score and with Reason if scoring failed
	EventPlanStarted  EventKind = "plan_started" // Text is the plan's rationale
	EventPlanDone     EventKind = "plan_done"    // Text is the synthesized output
	EventPlanFailed   EventKind = "plan_failed"  // with Error
	EventPlanPruned   EventKind = "plan_pruned"
	EventResultScored EventKind = "result_scored" // the plan's result; as plan_scored on failure
	EventPlanChosen   EventKind = "plan_chosen"
	EventNoteAdded    EventKind = "note_added" // Text is the note; TaskID its author
	EventUsage        EventKind = "usage"      // one model call; with Tag and Usage
)

// Event reports progress of a Run. The events of a Run, in order, are
// enough to rebuild its tree: see Replay and Engine.Resume. Which fields are
// set depends on Kind, as its constants say. New kinds and fields may be
// added; readers should ignore those they do not know.
type Event struct {
	Kind   EventKind `json:"kind"`
	Time   time.Time `json:"time"`
	TaskID string    `json:"task_id,omitempty"`
	PlanID string    `json:"plan_id,omitempty"`
	Score  *Score    `json:"score,omitempty"`
	// Text carries the goal, a task's description, a plan's rationale, an
	// output or a note, depending on Kind.
	Text string `json:"text,omitempty"`
	// Error is why a task, plan or Run failed.
	Error string `json:"error,omitempty"`
	// Code classifies the failure of a Run, on run_failed, so that the
	// journal tells what to do next without parsing Error: "task_limit",
	// "token_limit", "canceled", "deadline_exceeded", "journal" or "error".
	Code string `json:"code,omitempty"`
	// Reason is why a task was executed directly although it could be
	// decomposed, on task_decided; why it was not started, on task_pruned;
	// and why scoring failed, on plan_scored and result_scored.
	Reason string `json:"reason,omitempty"`
	// Tag is the tag of the model call's request, on usage.
	Tag string `json:"tag,omitempty"`
	// Plans are the plans a task was decomposed into, on task_decided. Their
	// subtask IDs and dependencies are the IDs of the tree.
	Plans []PlanProposal `json:"plans,omitempty"`
	// Value is the output converted as Into asked, on run_done.
	Value json.RawMessage `json:"value,omitempty"`
	// Format is the JournalFormat the events follow, on run_started.
	Format int `json:"format,omitempty"`
	// Agent is the root task's agent, on run_started.
	Agent string `json:"agent,omitempty"`
	// Usage is the tokens used by one model call, on usage; by the task's own
	// model calls so far, on task_done and task_failed; and by the Run and
	// those it resumed, on run_done and run_failed. Only the usage events
	// add up to a total; the others are running totals.
	Usage *Usage `json:"usage,omitempty"`
}

// Observer receives the events of a Run. The engine delivers the events of
// one Run one at a time and in journal order. An observer registered with
// WithObserver receives the events of every Run of the Engine, which arrive
// concurrently when Runs overlap: it must then be safe for concurrent use.
// ObserveWith registers an observer for one Run. The Run waits while an observer runs: one doing slow work,
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

// detached returns a copy of e sharing no memory with the explored tree or
// with other events, so that observers may keep or change it.
func (e Event) detached() Event {
	if e.Score != nil {
		s := *e.Score
		e.Score = &s
	}
	if e.Usage != nil {
		u := *e.Usage
		e.Usage = &u
	}
	e.Value = slices.Clone(e.Value)
	if e.Plans != nil {
		plans := make([]PlanProposal, len(e.Plans))
		for i, p := range e.Plans {
			p.Subtasks = slices.Clone(p.Subtasks)
			for j := range p.Subtasks {
				p.Subtasks[j].DependsOn = slices.Clone(p.Subtasks[j].DependsOn)
			}
			plans[i] = p
		}
		e.Plans = plans
	}
	return e
}
