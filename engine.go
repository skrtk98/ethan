package ethan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"text/template"
	"time"
)

// Engine explores a goal by building and solving an AND-OR task tree.
// An Engine is safe for concurrent Runs.
type Engine struct {
	model           Model
	agents          []Agent
	planner         Planner
	evaluator       Evaluator
	synthesizer     Synthesizer
	strategy        Strategy
	limits          Limits
	executeTemplate *template.Template
	observers       []Observer
	journal         Journal
	notesInPrompt   int
	maxPlans        int
	maxTokens       int
}

// Option configures an Engine.
type Option func(*Engine)

// WithAgents registers the agents subtasks can be assigned to. The first
// agent is the default for unassigned or unknown names.
func WithAgents(agents ...Agent) Option {
	return func(e *Engine) { e.agents = agents }
}

// WithPlanner replaces the default LLMPlanner.
func WithPlanner(p Planner) Option {
	return func(e *Engine) { e.planner = p }
}

// WithEvaluator replaces the default LLMEvaluator.
func WithEvaluator(ev Evaluator) Option {
	return func(e *Engine) { e.evaluator = ev }
}

// WithSynthesizer replaces the default LLMSynthesizer.
func WithSynthesizer(s Synthesizer) Option {
	return func(e *Engine) { e.synthesizer = s }
}

// WithStrategy replaces the default BeamStrategy.
func WithStrategy(s Strategy) Option {
	return func(e *Engine) { e.strategy = s }
}

// WithLimits bounds each Run.
func WithLimits(l Limits) Option {
	return func(e *Engine) { e.limits = l }
}

// WithExecuteTemplate replaces the agents' default prompt template.
func WithExecuteTemplate(t *template.Template) Option {
	return func(e *Engine) { e.executeTemplate = t }
}

// WithObserver adds an observer that receives the events of every Run.
func WithObserver(o Observer) Option {
	return func(e *Engine) { e.observers = append(e.observers, o) }
}

// WithJournal records the events of every Run in j, so that a Run can be
// traced and resumed after it stops. A Run whose journal fails is canceled.
func WithJournal(j Journal) Option {
	return func(e *Engine) { e.journal = j }
}

// WithNotesInPrompt sets how many of the latest blackboard notes are shown
// in prompts. Default 20.
func WithNotesInPrompt(n int) Option {
	return func(e *Engine) { e.notesInPrompt = n }
}

// WithMaxPlans sets how many alternative plans the planner may propose. Default 3.
func WithMaxPlans(n int) Option {
	return func(e *Engine) { e.maxPlans = n }
}

// WithMaxOutputTokens bounds the output tokens, including thinking, of each
// model call. Default 16000.
func WithMaxOutputTokens(n int) Option {
	return func(e *Engine) { e.maxTokens = n }
}

// New returns an Engine whose components default to LLM-backed
// implementations using m.
func New(m Model, opts ...Option) *Engine {
	e := &Engine{model: m, maxPlans: 3, notesInPrompt: 20, strategy: BeamStrategy{}}
	for _, o := range opts {
		o(e)
	}
	if len(e.agents) == 0 {
		e.agents = []Agent{DefaultAgent}
	}
	if e.executeTemplate == nil {
		e.executeTemplate = DefaultTemplate("execute")
	}
	e.limits = e.limits.withDefaults()
	return e
}

// Result is the outcome of a Run. It marshals to JSON as the Run's trace.
type Result struct {
	Goal   string `json:"goal"`
	Output string `json:"output"`
	// Score is the evaluation of the root output, if it was evaluated.
	Score *Score `json:"score,omitempty"`
	// Error is the Run's error message, if it failed.
	Error  string  `json:"error,omitempty"`
	Root   *Task   `json:"root"`
	Usage  Usage   `json:"usage"`
	Notes  []Note  `json:"notes"`
	Events []Event `json:"events"`
}

// WriteTrace writes r as indented JSON.
func (r *Result) WriteTrace(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Run solves goal and returns the root task's output. On failure the
// returned Result still holds the explored tree. When a limit is exceeded
// the whole Run is canceled and the error wraps ErrLimitExceeded.
func (e *Engine) Run(ctx context.Context, goal string) (*Result, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	r := e.newRun(goal, cancel)
	root := &Task{ID: "t0", Description: goal, Agent: e.agents[0].Name, Status: StatusPending}
	r.emit(Event{Kind: EventRunStarted, Text: goal, Agent: root.Agent})
	return r.finish(ctx, root, r.start(ctx, root, nil, nil))
}

// Resume continues the run recorded in events, as read from its journal
// with ReadJournal. Finished tasks and plans, decisions, scores and notes
// are reused without calling any model; everything else, including tasks
// that were running when the run stopped, is solved again. Limits apply to
// the resumed run alone, while Result.Usage includes the earlier runs.
//
// To keep the journal complete, give the Engine the same journal to append
// to. The returned Result holds the events of the earlier runs too.
func (e *Engine) Resume(ctx context.Context, events []Event) (*Result, error) {
	s, err := replay(events)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	root := s.root
	reopen(root)
	r := e.newRun(root.Description, cancel)
	r.prior = s.usage()
	r.board.notes = s.notes
	r.events = slices.Clone(events)
	r.emit(Event{Kind: EventRunResumed})
	return r.finish(ctx, root, r.start(ctx, root, nil, nil))
}

func (e *Engine) newRun(goal string, cancel context.CancelCauseFunc) *run {
	r := &run{engine: e, goal: goal, cancel: cancel, sem: make(chan struct{}, e.limits.MaxConcurrency), board: &Blackboard{}}
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
	if resp != nil {
		r.mu.Lock()
		r.spent = r.spent.Add(resp.Usage)
		spent = r.spent.Total()
		if t, ok := ctx.Value(taskKey{}).(*Task); ok {
			t.Usage = t.Usage.Add(resp.Usage)
		}
		r.mu.Unlock()
		if limit > 0 && spent > limit {
			// The response is paid for and still used; the rest of the run stops.
			r.exceed(ErrTokenLimit)
		}
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
	if j := r.engine.journal; j != nil && r.journalErr == nil {
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

func (r *run) agent(name string) Agent {
	for _, a := range r.engine.agents {
		if a.Name == name {
			return a
		}
	}
	return r.engine.agents[0]
}

func (r *run) agentInfos() []AgentInfo {
	infos := make([]AgentInfo, len(r.engine.agents))
	for i, a := range r.engine.agents {
		infos[i] = AgentInfo{Name: a.Name, Description: a.Description}
	}
	return infos
}

func (r *run) planRequest(t *Task, b Brief) PlanRequest {
	return PlanRequest{Brief: b, Agents: r.agentInfos(), MaxPlans: r.engine.maxPlans,
		Depth: t.Depth, MaxDepth: r.engine.limits.MaxDepth}
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

// start counts t against Limits.MaxTasks and solves it, unless it was
// already done in the run being resumed.
func (r *run) start(ctx context.Context, t *Task, path []string, inputs []SubtaskResult) error {
	if t.Status == StatusDone {
		return nil
	}
	if err := r.takeTask(); err != nil {
		t.Status, t.Error = StatusPruned, "not started: "+err.Error()
		return err
	}
	return r.solve(ctx, t, path, inputs)
}

// solve solves t, recording its outcome on t. path holds the ancestors'
// descriptions; inputs are the outputs of t's dependencies.
func (r *run) solve(ctx context.Context, t *Task, path []string, inputs []SubtaskResult) error {
	t.Status = StatusRunning
	r.emit(Event{Kind: EventTaskStarted, TaskID: t.ID, Text: t.Description})
	ctx = context.WithValue(ctx, taskKey{}, t)
	err := r.solveTask(ctx, t, path, inputs)
	if err != nil {
		t.Status = StatusFailed
		t.Error = err.Error()
		r.emit(Event{Kind: EventTaskFailed, TaskID: t.ID, Text: t.Error, Usage: r.taskUsage(t)})
		return fmt.Errorf("task %s: %w", t.ID, err)
	}
	t.Status = StatusDone
	r.emit(Event{Kind: EventTaskDone, TaskID: t.ID, Score: t.Score, Text: t.Output, Usage: r.taskUsage(t)})
	return nil
}

func (r *run) solveTask(ctx context.Context, t *Task, path []string, inputs []SubtaskResult) error {
	b := Brief{Goal: r.goal, Path: path, Task: t.Description, Inputs: inputs, Notes: r.board.Recent(r.engine.notesInPrompt)}
	// A task resumed from a journal keeps the decision it had made.
	switch {
	case t.Atomic:
		return r.executeLeaf(ctx, t, b)
	case len(t.Plans) > 0:
		return r.explore(ctx, t, b, t.Plans)
	}
	if t.Depth < r.engine.limits.MaxDepth {
		d, err := r.planner.Plan(ctx, r.planRequest(t, b))
		switch {
		case ctx.Err() != nil:
			return fmt.Errorf("plan: %w", err)
		case err == nil && !d.Atomic:
			if plans := r.buildPlans(t, d.Plans); len(plans) > 0 {
				proposals := make([]PlanProposal, len(plans))
				for i, p := range plans {
					proposals[i] = proposalOf(p)
				}
				r.emit(Event{Kind: EventTaskDecided, TaskID: t.ID, Text: strconv.Itoa(len(plans)), Plans: proposals})
				return r.explore(ctx, t, b, plans)
			}
		}
		// A failed or unusable decision falls back to direct execution.
	}
	r.emit(Event{Kind: EventTaskDecided, TaskID: t.ID, Text: "atomic"})
	return r.executeLeaf(ctx, t, b)
}

func (r *run) executeLeaf(ctx context.Context, t *Task, b Brief) error {
	t.Atomic = true
	agent := r.agent(t.Agent)
	t.Agent = agent.Name
	m := r.model
	if agent.Model != nil {
		m = r.meter(agent.Model)
	}
	tools := append(slices.Clone(agent.Tools), noteTool{author: t.ID, post: r.post})
	out, err := execute(ctx, m, r.engine.executeTemplate, agent, tools, b, r.engine.maxTokens)
	if err != nil {
		return fmt.Errorf("execute: %w", err)
	}
	t.Output = out
	return nil
}

// buildPlans turns valid proposals into Plans under parent. Proposals with
// no subtasks, duplicate or dangling ids, or dependency cycles are dropped.
func (r *run) buildPlans(parent *Task, proposals []PlanProposal) []*Plan {
	var valid []PlanProposal
	for _, pp := range proposals {
		if len(valid) == r.engine.maxPlans {
			break
		}
		if validProposal(pp) {
			valid = append(valid, pp)
		}
	}
	return newPlans(parent, valid, func(name string) string { return r.agent(name).Name })
}

// newPlans builds the Plans of parent from proposals, giving the plans and
// subtasks their IDs in the tree. Proposals whose IDs are already the
// tree's, as journaled, get the same IDs back.
func newPlans(parent *Task, proposals []PlanProposal, agent func(string) string) []*Plan {
	plans := make([]*Plan, len(proposals))
	for i, pp := range proposals {
		p := &Plan{ID: parent.ID + ".p" + strconv.Itoa(i+1), Rationale: pp.Rationale, Status: StatusPending}
		ids := make(map[string]string, len(pp.Subtasks))
		for j, sp := range pp.Subtasks {
			ids[sp.ID] = p.ID + ".t" + strconv.Itoa(j+1)
		}
		for _, sp := range pp.Subtasks {
			st := &Task{ID: ids[sp.ID], Description: sp.Description, Agent: agent(sp.Agent), Depth: parent.Depth + 1, Status: StatusPending}
			for _, d := range sp.DependsOn {
				st.DependsOn = append(st.DependsOn, ids[d])
			}
			p.Subtasks = append(p.Subtasks, st)
		}
		plans[i] = p
	}
	return plans
}

func validProposal(pp PlanProposal) bool {
	if len(pp.Subtasks) == 0 {
		return false
	}
	deps := make(map[string][]string, len(pp.Subtasks))
	for _, s := range pp.Subtasks {
		if _, dup := deps[s.ID]; dup {
			return false
		}
		deps[s.ID] = s.DependsOn
	}
	for _, ds := range deps {
		for _, d := range ds {
			if _, ok := deps[d]; !ok {
				return false
			}
		}
	}
	// Kahn's algorithm: the graph is acyclic iff every node can be removed.
	indeg := make(map[string]int, len(deps))
	for id, ds := range deps {
		indeg[id] = len(ds)
	}
	removed := 0
	for progress := true; progress; {
		progress = false
		for id, n := range indeg {
			if n != 0 {
				continue
			}
			delete(indeg, id)
			removed++
			progress = true
			for other, ds := range deps {
				if _, live := indeg[other]; live && slices.Contains(ds, id) {
					indeg[other]--
				}
			}
		}
	}
	return removed == len(deps)
}

// explore lets the strategy pick one of plans as the solution of t.
func (r *run) explore(ctx context.Context, t *Task, b Brief, plans []*Plan) error {
	t.Plans = plans
	x := &exploration{run: r, task: t, brief: b, plans: plans}
	p, err := r.engine.strategy.Explore(ctx, x)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		// Plans canceled midway were not compared; finished ones are kept
		// for a resume to choose among.
		return err
	}
	if p == nil || p.Status != StatusDone {
		return errors.New("strategy returned no successful plan")
	}
	t.Chosen, t.Output, t.Score = p.ID, p.Output, p.ResultScore
	r.emit(Event{Kind: EventPlanChosen, TaskID: t.ID, PlanID: p.ID, Score: p.ResultScore})
	return nil
}

// exploration implements Exploration for one task.
type exploration struct {
	run   *run
	task  *Task
	brief Brief
	plans []*Plan
}

func (x *exploration) Plans() []*Plan { return x.plans }

func (x *exploration) Depth() int { return x.task.Depth }

func (x *exploration) ScorePlan(ctx context.Context, p *Plan) *Score {
	if p.PlanScore != nil {
		return p.PlanScore // resumed
	}
	s, err := x.run.evaluator.ScorePlan(ctx, x.brief, proposalOf(p))
	if err != nil {
		return nil
	}
	p.PlanScore = &s
	x.run.emit(Event{Kind: EventPlanScored, TaskID: x.task.ID, PlanID: p.ID, Score: &s})
	return p.PlanScore
}

func (x *exploration) ScoreResult(ctx context.Context, p *Plan) *Score {
	if p.ResultScore != nil {
		return p.ResultScore // resumed
	}
	s, err := x.run.evaluator.ScoreResult(ctx, x.brief, p.Output)
	if err != nil {
		return nil
	}
	p.ResultScore = &s
	x.run.emit(Event{Kind: EventResultScored, TaskID: x.task.ID, PlanID: p.ID, Score: &s})
	return p.ResultScore
}

func (x *exploration) Prune(p *Plan) {
	if p.Status == StatusDone {
		return // resumed
	}
	p.Status = StatusPruned
	x.run.emit(Event{Kind: EventPlanPruned, TaskID: x.task.ID, PlanID: p.ID})
}

func (x *exploration) Execute(ctx context.Context, p *Plan) error {
	if p.Status == StatusDone {
		return nil // resumed
	}
	p.Status = StatusRunning
	x.run.emit(Event{Kind: EventPlanStarted, TaskID: x.task.ID, PlanID: p.ID, Text: p.Rationale})
	if err := x.run.executePlan(ctx, x.task, x.brief, p); err != nil {
		p.Status = StatusFailed
		p.Error = err.Error()
		x.run.emit(Event{Kind: EventPlanFailed, TaskID: x.task.ID, PlanID: p.ID, Text: p.Error})
		return fmt.Errorf("plan %s: %w", p.ID, err)
	}
	p.Status = StatusDone
	x.run.emit(Event{Kind: EventPlanDone, TaskID: x.task.ID, PlanID: p.ID, Text: p.Output})
	return nil
}

func proposalOf(p *Plan) PlanProposal {
	pp := PlanProposal{Rationale: p.Rationale}
	for _, st := range p.Subtasks {
		pp.Subtasks = append(pp.Subtasks, SubtaskProposal{ID: st.ID, Description: st.Description, Agent: st.Agent, DependsOn: st.DependsOn})
	}
	return pp
}

// executePlan solves p's subtasks, each as soon as its dependencies are
// done, then synthesizes their outputs. The first failure cancels the
// remaining subtasks and is returned.
func (r *run) executePlan(parentCtx context.Context, parent *Task, b Brief, p *Plan) error {
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	done := make(map[string]chan struct{}, len(p.Subtasks))
	byID := make(map[string]*Task, len(p.Subtasks))
	for _, st := range p.Subtasks {
		done[st.ID] = make(chan struct{})
		byID[st.ID] = st
	}
	path := append(slices.Clone(b.Path), parent.Description)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for _, st := range p.Subtasks {
		wg.Go(func() {
			defer close(done[st.ID])
			var inputs []SubtaskResult
			for _, d := range st.DependsOn {
				<-done[d] // every subtask closes its channel, even when skipped
				dep := byID[d]
				if dep.Status != StatusDone {
					st.Status = StatusPruned
					return
				}
				inputs = append(inputs, SubtaskResult{Description: dep.Description, Output: dep.Output})
			}
			if ctx.Err() != nil {
				st.Status = StatusPruned
				return
			}
			if err := r.start(ctx, st, path, inputs); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if err := parentCtx.Err(); err != nil {
		return err
	}
	if firstErr != nil {
		return firstErr
	}
	results := make([]SubtaskResult, len(p.Subtasks))
	for i, st := range p.Subtasks {
		results[i] = SubtaskResult{Description: st.Description, Output: st.Output}
	}
	out, err := r.synthesizer.Synthesize(ctx, b, results)
	if err != nil {
		return fmt.Errorf("synthesize: %w", err)
	}
	p.Output = out
	return nil
}
