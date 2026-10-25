package ethan

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

func (e *Engine) newRun(goal string, cancel context.CancelCauseFunc, opts []RunOption) *run {
	r := &run{engine: e, goal: goal, cancel: cancel, sem: make(chan struct{}, e.limits.MaxConcurrency), board: &blackboard{}}
	for _, o := range opts {
		o(r)
	}
	r.model = r.meter(e.model)
	r.planner = cmp.Or[Planner](e.planner, &LLMPlanner{})
	r.evaluator = cmp.Or[Evaluator](e.evaluator, &LLMEvaluator{})
	r.synthesizer = cmp.Or[Synthesizer](e.synthesizer, &LLMSynthesizer{})
	return r
}

// runKey is the context key of the run whose model calls are made.
type runKey struct{}

// modelFor returns the model the LLM components call: m, or the engine's
// model if m is nil, metered when called within a Run so that every call
// counts against its limits.
func modelFor(ctx context.Context, m Model) (Model, error) {
	r, _ := ctx.Value(runKey{}).(*run)
	switch {
	case r == nil && m == nil:
		return nil, errors.New("ethan: no Model to call outside a Run")
	case r == nil:
		return m, nil
	case m == nil:
		return r.model, nil
	}
	if _, ok := m.(meteredModel); ok {
		return m, nil
	}
	return r.meter(m), nil
}

// maxOutputFor returns n, or within a Run the engine's WithMaxOutputTokens
// if n is zero.
func maxOutputFor(ctx context.Context, n int) int {
	if r, _ := ctx.Value(runKey{}).(*run); n <= 0 && r != nil {
		n = r.engine.maxTokens
	}
	return maxTokensOr(n)
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
		r.emit(Event{Kind: EventRunFailed, Error: err.Error(), Code: r.failureCode(err), Usage: &usage})
	} else {
		r.emit(Event{Kind: EventRunDone, Usage: &usage, Value: r.value})
	}
	r.emitMu.Lock()
	if err == nil && r.journalErr != nil {
		err = r.journalErr // the last events could not be journaled
	}
	events := r.events
	r.emitMu.Unlock()
	res := &Result{Goal: r.goal, Output: root.Output, Value: r.value, Score: root.Score, Root: root, Usage: usage,
		Notes: r.board.all(), Events: events}
	if err != nil {
		res.Error = err.Error()
	}
	return res, err
}

// failureCode classifies err, which the Run failed with, for Event.Code.
func (r *run) failureCode(err error) string {
	r.emitMu.Lock()
	journalErr := r.journalErr
	r.emitMu.Unlock()
	switch {
	case errors.Is(err, ErrTaskLimit):
		return "task_limit"
	case errors.Is(err, ErrTokenLimit):
		return "token_limit"
	case journalErr != nil && errors.Is(err, journalErr):
		return "journal"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	}
	return "error"
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
	board       *blackboard

	emitMu     sync.Mutex
	events     []Event
	journal    Journal
	journalErr error
	observers  []Observer // of this run alone

	into  *into
	value json.RawMessage // the output converted as into asks

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
		if err == nil {
			err = errors.New("ethan: model returned neither a response nor an error")
		}
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
	e := Event{Kind: EventUsage, Tag: req.Tag, Usage: &usage}
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
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	r.emitLocked(e)
}

// emitLocked is emit with emitMu held.
func (r *run) emitLocked(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	e = e.detached()
	if j := r.journal; j != nil && r.journalErr == nil {
		if err := j.Append(e); err != nil {
			r.journalErr = fmt.Errorf("ethan: journal: %w", err)
			r.stop(r.journalErr)
			if e.Kind == EventRunDone {
				// What is recorded and observed must agree with the error
				// the Run returns.
				e = Event{Kind: EventRunFailed, Time: e.Time, Error: r.journalErr.Error(), Code: "journal", Usage: e.Usage}
			}
		}
	}
	r.events = append(r.events, e)
	for _, o := range r.engine.observers {
		o.OnEvent(e)
	}
	for _, o := range r.observers {
		o.OnEvent(e)
	}
}

// post adds a note to the blackboard and records it, in the same order.
func (r *run) post(author, text string) {
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	n := r.board.post(author, text)
	r.emitLocked(Event{Kind: EventNoteAdded, Time: n.Time, TaskID: author, Text: n.Text})
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

// solveGoal solves root, the goal, and converts its output if Into asked.
func (r *run) solveGoal(ctx context.Context, root *Task) error {
	if r.into != nil && r.into.err != nil {
		return r.into.err
	}
	if err := r.start(ctx, root, nil, nil); err != nil {
		return err
	}
	return r.convert(ctx, root)
}
