package ethan

import (
	"errors"
	"fmt"
	"slices"
)

// Replay rebuilds the Result of a run from its journaled events, without
// calling any model. The events may span several runs: a Run and the
// Resumes that continued it.
func Replay(events []Event) (*Result, error) {
	s, err := replay(events)
	if err != nil {
		return nil, err
	}
	return &Result{Goal: s.root.Description, Output: s.root.Output, Value: s.value, Score: s.root.Score, Error: s.err,
		Root: s.root, Usage: s.usage(), Notes: s.notes, Events: events}, nil
}

// replayed is the state rebuilt from a journal.
type replayed struct {
	root  *Task
	notes []Note
	err   string // the last run's error, if it failed
	value []byte // the output converted for Into, if the last run did
	total Usage  // of every model call journaled
}

// ErrInvalidJournal is returned by Replay and Engine.Resume for events that
// do not describe a run, such as a journal of another program or one
// edited by hand.
var ErrInvalidJournal = errors.New("ethan: invalid journal")

func replay(events []Event) (*replayed, error) {
	if len(events) == 0 || events[0].Kind != EventRunStarted {
		return nil, fmt.Errorf("%w: it does not start with run_started", ErrInvalidJournal)
	}
	if f := events[0].Format; f != JournalFormat {
		return nil, fmt.Errorf("%w: %d (this version reads %d)", ErrJournalFormat, f, JournalFormat)
	}
	s := &replayed{root: &Task{ID: "t0", Description: events[0].Text, Agent: events[0].Agent, Status: StatusPending}, notes: []Note{}}
	tasks := map[string]*Task{"t0": s.root}
	plans := map[string]*Plan{}
	for i, e := range events {
		invalid := func(format string, args ...any) error {
			return fmt.Errorf("%w: event %d (%s): %s", ErrInvalidJournal, i+1, e.Kind, fmt.Sprintf(format, args...))
		}
		if i > 0 && e.Kind == EventRunStarted {
			return nil, invalid("a second run starts; a journal holds one run")
		}
		var (
			t *Task
			p *Plan
		)
		if e.TaskID != "" && e.Kind != EventNoteAdded && e.Kind != EventUsage {
			if t = tasks[e.TaskID]; t == nil {
				return nil, invalid("unknown task %s", e.TaskID)
			}
		}
		if e.PlanID != "" {
			if p = plans[e.PlanID]; p == nil {
				return nil, invalid("unknown plan %s", e.PlanID)
			}
			if t != nil && !slices.Contains(t.Plans, p) {
				return nil, invalid("plan %s is not one of task %s's", p.ID, t.ID)
			}
		}
		switch e.Kind {
		case EventTaskStarted, EventTaskDecided, EventTaskDone, EventTaskFailed, EventTaskPruned, EventPlanChosen:
			if t == nil {
				return nil, invalid("no task")
			}
		}
		switch e.Kind {
		case EventPlanScored, EventPlanStarted, EventPlanDone, EventPlanFailed, EventPlanPruned, EventResultScored, EventPlanChosen:
			if p == nil {
				return nil, invalid("no plan")
			}
		}
		switch e.Kind {
		case EventRunResumed:
			reopen(s.root) // as Resume did before continuing
			s.err = ""
		case EventRunDone:
			s.err, s.value = "", e.Value
		case EventRunFailed:
			s.err, s.value = e.Error, nil
		case EventTaskStarted:
			t.Status = StatusRunning
		case EventTaskDecided:
			if t.Atomic || t.Plans != nil {
				return nil, invalid("task %s is decided twice", t.ID)
			}
			if len(e.Plans) == 0 {
				t.Atomic = true
				break
			}
			for _, pp := range e.Plans {
				if !validProposal(pp, func(string) bool { return true }) {
					return nil, invalid("a plan has no subtasks, repeats an ID or has dependencies that are unknown or cyclic")
				}
			}
			t.Plans = newPlans(t, e.Plans, func(name string) string { return name })
			for _, p := range t.Plans {
				plans[p.ID] = p
				for _, st := range p.Subtasks {
					tasks[st.ID] = st
				}
			}
		case EventTaskDone:
			t.Status, t.Output, t.Score, t.Error = StatusDone, e.Text, e.Score, ""
		case EventTaskFailed:
			t.Status, t.Error = StatusFailed, e.Error
		case EventTaskPruned:
			t.Status, t.Error = StatusPruned, e.Reason
		case EventUsage:
			u := usageOf(e)
			s.total = s.total.Add(u)
			if t := tasks[e.TaskID]; t != nil {
				t.Usage = t.Usage.Add(u)
			}
		case EventPlanScored:
			p.PlanScore = e.Score
		case EventPlanStarted:
			p.Status = StatusRunning
		case EventPlanDone:
			p.Status, p.Output, p.Error = StatusDone, e.Text, ""
		case EventPlanFailed:
			p.Status, p.Error = StatusFailed, e.Error
		case EventPlanPruned:
			p.Status = StatusPruned
		case EventResultScored:
			p.ResultScore = e.Score
		case EventPlanChosen:
			if p.Status != StatusDone {
				return nil, invalid("plan %s is chosen without having finished", p.ID)
			}
			// The choice finishes the task, even if the run stopped before
			// recording it done.
			t.Chosen, t.Output, t.Score, t.Status, t.Error = p.ID, p.Output, p.ResultScore, StatusDone, ""
		case EventNoteAdded:
			s.notes = append(s.notes, Note{Author: e.TaskID, Text: e.Text, Time: e.Time})
		}
	}
	return s, nil
}

func usageOf(e Event) Usage {
	if e.Usage == nil {
		return Usage{}
	}
	return *e.Usage
}

// usage is the total of the journaled model calls.
func (s *replayed) usage() Usage { return s.total }

// reopen prepares t for a resumed run: whatever did not finish is pending
// again, while finished tasks and plans, decisions and scores are kept.
func reopen(t *Task) {
	if t.Status == StatusDone {
		return
	}
	t.Status, t.Error, t.Chosen = StatusPending, "", ""
	for _, p := range t.Plans {
		if p.Status == StatusDone {
			continue
		}
		p.Status, p.Error = StatusPending, ""
		for _, st := range p.Subtasks {
			reopen(st)
		}
	}
}
