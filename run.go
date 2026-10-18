package ethan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

func (e *Engine) newRun(goal string, cancel context.CancelCauseFunc, opts []RunOption) *run {
	r := &run{engine: e, goal: goal, cancel: cancel, sem: make(chan struct{}, e.limits.MaxConcurrency), board: &Blackboard{}}
	for _, o := range opts {
		o(r)
	}
	r.model = r.meter(e.model)
	r.planner = e.planner
	if r.planner == nil {
		r.planner = &LLMPlanner{Model: r.model, MaxTokens: e.maxTokens}
	}
	r.evaluator = e.evaluator
	if r.evaluator == nil {
		r.evaluator = &LLMEvaluator{Model: r.model, MaxTokens: e.maxTokens}
	}
	r.synthesizer = e.synthesizer
	if r.synthesizer == nil {
		r.synthesizer = &LLMSynthesizer{Model: r.model, MaxTokens: e.maxTokens}
	}
	return r
}

// finish records the end of the run and assembles its Result.
func (r *run) finish(ctx context.Context, root *Task, err error) (*Result, error) {
	r.mu.Lock()
	usage := r.prior.Add(r.spent)
	stopErr := r.stopErr
	r.mu.Unlock()
	switch cause := context.Cause(ctx); {
	case stopErr != nil:
		// A run stopped for a limit or its journal fails, even if what was
		// left running managed to finish.
		err = stopErr
	case err != nil && cause != nil:
		err = cause // rather than the cancellations it caused
	}
	if err != nil {
		r.emit(Event{Kind: EventRunFailed, Text: err.Error(), Usage: &usage})
	} else {
		r.emit(Event{Kind: EventRunDone, Usage: &usage})
	}
	r.emitMu.Lock()
	if err == nil && r.journalErr != nil {
		err = r.journalErr // the last events could not be journaled
	}
	events := r.events
	r.emitMu.Unlock()
	res := &Result{Goal: r.goal, Output: root.Output, Score: root.Score, Root: root, Usage: usage,
		Notes: r.board.Notes(), Events: events}
	if err != nil {
		res.Error = err.Error()
	}
	return res, err
}

// run holds the state of one Run.
type run struct {
	engine      *Engine
	goal        string
	model       Model
	planner     Planner
	evaluator   Evaluator
	synthesizer Synthesizer
	cancel      context.CancelCauseFunc
	sem         chan struct{}
	board       *Blackboard

	emitMu     sync.Mutex
	events     []Event
	journal    Journal
	journalErr error

	mu      sync.Mutex
	prior   Usage // spent by the runs this one resumes
	spent   Usage // spent by this run, which Limits.MaxTokens bounds
	tasks   int
	stopErr error // why the run was stopped, if it was
}

// meter wraps m so that its calls count against the run's limits.
func (r *run) meter(m Model) Model {
	return meteredModel{run: r, model: m}
}

type meteredModel struct {
	run   *run
	model Model
}

func (mm meteredModel) Generate(ctx context.Context, req Request) (*Response, error) {
	r := mm.run
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	limit := r.engine.limits.MaxTokens
	r.mu.Lock()
	spent := r.spent.Total()
	r.mu.Unlock()
	if limit > 0 && spent >= limit {
		return nil, r.exceed(ErrTokenLimit)
	}
	resp, err := mm.model.Generate(ctx, req)
	if resp == nil {
		return nil, err
	}
	t, _ := ctx.Value(taskKey{}).(*Task)
	r.mu.Lock()
	r.spent = r.spent.Add(resp.Usage)
	spent = r.spent.Total()
	if t != nil {
		t.Usage = t.Usage.Add(resp.Usage)
	}
	r.mu.Unlock()
	usage := resp.Usage
	e := Event{Kind: EventUsage, Text: req.Tag, Usage: &usage}
	if t != nil {
		e.TaskID = t.ID
	}
	r.emit(e)
	if limit > 0 && spent > limit {
		// The response is paid for but not used: the run stops here.
		return resp, r.exceed(ErrTokenLimit)
	}
	return resp, err
}

// taskKey is the context key of the task whose model calls are made; their
// usage is recorded on it.
type taskKey struct{}

// taskUsage returns a copy of t.Usage.
func (r *run) taskUsage(t *Task) *Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	u := t.Usage
	return &u
}

// exceed stops the run because err, a limit, was exceeded.
func (r *run) exceed(err error) error {
	r.stop(err)
	return err
}

// stop cancels the whole run for err; the first reason is kept.
func (r *run) stop(err error) {
	r.mu.Lock()
	if r.stopErr == nil {
		r.stopErr = err
	}
	r.mu.Unlock()
	r.cancel(err)
}

// emit records e and delivers it to the observers, one event at a time.
func (r *run) emit(e Event) {
	e.Time = time.Now()
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	r.events = append(r.events, e)
	if j := r.journal; j != nil && r.journalErr == nil {
		if err := j.Append(e); err != nil {
			r.journalErr = fmt.Errorf("ethan: journal: %w", err)
			r.stop(r.journalErr)
		}
	}
	for _, o := range r.engine.observers {
		o.OnEvent(e)
	}
}

func (r *run) post(author, text string) {
	n := r.board.Post(author, text)
	r.emit(Event{Kind: EventNoteAdded, TaskID: author, Text: n.Text})
}

// takeTask counts a task about to start against Limits.MaxTasks.
func (r *run) takeTask() error {
	r.mu.Lock()
	full := r.tasks >= r.engine.limits.MaxTasks
	if !full {
		r.tasks++
	}
	r.mu.Unlock()
	if full {
		return r.exceed(ErrTaskLimit)
	}
	return nil
}

const noteToolName = "note"

// noteTool lets an agent post to the blackboard on behalf of its task.
type noteTool struct {
	author string
	post   func(author, text string)
}

func (noteTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        noteToolName,
		Description: "Share a concise fact, finding or decision with the other agents working on the same goal.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}
}

func (t noteTool) Call(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	if in.Text == "" {
		return "", errors.New("text is empty")
	}
	t.post(t.author, in.Text)
	return "noted", nil
}
