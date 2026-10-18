package ethan

import (
	"errors"
	"fmt"
)

// Replay rebuilds the Result of a run from its journaled events, without
// calling any model. The events may span several runs: a Run and the
// Resumes that continued it.
func Replay(events []Event) (*Result, error) {
	s, err := replay(events)
	if err != nil {
		return nil, err
	}
	return &Result{Goal: s.root.Description, Output: s.root.Output, Score: s.root.Score, Error: s.err,
		Root: s.root, Usage: s.usage(), Notes: s.notes, Events: events}, nil
}

// replayed is the state rebuilt from a journal.
type replayed struct {
	root  *Task
	notes []Note
	err   string // the last run's error, if it failed
}

func replay(events []Event) (*replayed, error) {
	if len(events) == 0 || events[0].Kind != EventRunStarted {
		return nil, errors.New("ethan: journal does not start with run_started")
	}
	s := &replayed{root: &Task{ID: "t0", Description: events[0].Text, Agent: events[0].Agent, Status: StatusPending}, notes: []Note{}}
	tasks := map[string]*Task{"t0": s.root}
	plans := map[string]*Plan{}
	for i, e := range events {
		var (
			t *Task
			p *Plan
		)
		if e.PlanID != "" {
			if p = plans[e.PlanID]; p == nil {
				return nil, fmt.Errorf("ethan: journal event %d: unknown plan %s", i+1, e.PlanID)
			}
		} else if e.TaskID != "" && e.Kind != EventNoteAdded {
			if t = tasks[e.TaskID]; t == nil {
				return nil, fmt.Errorf("ethan: journal event %d: unknown task %s", i+1, e.TaskID)
			}
		}
		switch e.Kind {
		case EventRunDone:
			s.err = ""
		case EventRunFailed:
			s.err = e.Text
		case EventTaskStarted:
			t.Status = StatusRunning
		case EventTaskDecided:
			if len(e.Plans) == 0 {
				t.Atomic = true
				break
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
			t.Usage = usageOf(e)
		case EventTaskFailed:
			t.Status, t.Error = StatusFailed, e.Text
			t.Usage = usageOf(e)
		case EventPlanScored:
			p.PlanScore = e.Score
		case EventPlanStarted:
			p.Status = StatusRunning
		case EventPlanDone:
			p.Status, p.Output, p.Error = StatusDone, e.Text, ""
		case EventPlanFailed:
			p.Status, p.Error = StatusFailed, e.Text
		case EventPlanPruned:
			p.Status = StatusPruned
		case EventResultScored:
			p.ResultScore = e.Score
		case EventPlanChosen:
			tasks[e.TaskID].Chosen = e.PlanID
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

// usage is the total of the tasks' recorded usage.
func (s *replayed) usage() Usage {
	var total Usage
	var walk func(t *Task)
	walk = func(t *Task) {
		total = total.Add(t.Usage)
		for _, p := range t.Plans {
			for _, st := range p.Subtasks {
				walk(st)
			}
		}
	}
	walk(s.root)
	return total
}

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
