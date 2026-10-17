package ethan

import (
	"cmp"
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
	budget          Budget
	executeTemplate *template.Template
	estimate        func(Request) int
	observers       []Observer
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

// WithBudget bounds each Run.
func WithBudget(b Budget) Option {
	return func(e *Engine) { e.budget = b }
}

// WithExecuteTemplate replaces the agents' default prompt template.
func WithExecuteTemplate(t *template.Template) Option {
	return func(e *Engine) { e.executeTemplate = t }
}

// WithTokenEstimator replaces the estimate a model call reserves against
// Budget.MaxTokens before it starts. The estimate should be an upper bound
// of the call's input plus output tokens.
func WithTokenEstimator(f func(Request) int) Option {
	return func(e *Engine) { e.estimate = f }
}

// WithObserver adds an observer that receives the events of every Run.
func WithObserver(o Observer) Option {
	return func(e *Engine) { e.observers = append(e.observers, o) }
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
	e := &Engine{model: m, maxPlans: 3, notesInPrompt: 20, strategy: BeamStrategy{}, estimate: estimateTokens}
	for _, o := range opts {
		o(e)
	}
	if len(e.agents) == 0 {
		e.agents = []Agent{DefaultAgent}
	}
	if e.executeTemplate == nil {
		e.executeTemplate = DefaultTemplate("execute")
	}
	e.budget = e.budget.withDefaults()
	return e
}

// Result is the outcome of a Run. It marshals to JSON as the Run's trace.
type Result struct {
	Goal   string `json:"goal"`
	Output string `json:"output"`
	// Score is the evaluation of the root output, if it was evaluated.
	Score *Score `json:"score,omitempty"`
	// Error is the Run's error message, if it failed.
	Error string `json:"error,omitempty"`
	Root  *Task  `json:"root"`
	Usage Usage  `json:"usage"`
	// Exhausted reports whether the budget ran out during the Run: a model
	// call or a task was refused, or the tokens spent reached
	// Budget.MaxTokens.
	Exhausted bool `json:"exhausted"`
	// Partial reports whether Output is incomplete because the budget ran
	// out before every subtask finished. Output then names what is missing.
	Partial bool    `json:"partial"`
	Notes   []Note  `json:"notes"`
	Events  []Event `json:"events"`
}

// WriteTrace writes r as indented JSON.
func (r *Result) WriteTrace(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Run solves goal and returns the root task's output. On failure the
// returned Result still holds the explored tree.
func (e *Engine) Run(ctx context.Context, goal string) (*Result, error) {
	r := &run{engine: e, goal: goal, sem: make(chan struct{}, e.budget.MaxConcurrency), board: &Blackboard{},
		released: make(chan struct{})}
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

	root := &Task{ID: "t0", Description: goal, Agent: e.agents[0].Name, Status: StatusPending}
	err := r.takeNode()
	if err == nil {
		err = r.solve(ctx, root, nil, nil)
	}
	res := &Result{Goal: goal, Output: root.Output, Score: root.Score, Root: root, Notes: r.board.Notes(),
		Partial: root.Status == StatusPartial}
	if err != nil {
		res.Error = err.Error()
	}
	r.mu.Lock()
	res.Usage, res.Exhausted = r.spent, r.exhausted
	r.mu.Unlock()
	r.emitMu.Lock()
	res.Events = r.events
	r.emitMu.Unlock()
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
	sem         chan struct{}
	board       *Blackboard

	emitMu sync.Mutex
	events []Event

	mu        sync.Mutex
	spent     Usage
	reserved  int
	waiters   int           // calls waiting in reserve
	waiting   int           // tokens held by those calls
	released  chan struct{} // closed and replaced whenever a reservation is released
	exhausted bool
	nodes     int
}

// meter wraps m so that its calls are bounded by the run's budget.
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
	held := 0 // tokens this call has reserved; guarded by r.mu
	if err := r.reserve(ctx, r.engine.estimate(req), &held); err != nil {
		return nil, err
	}
	req.Reserve = func(tokens int) error { return r.reserve(ctx, tokens, &held) }

	resp, err := mm.model.Generate(ctx, req)
	var used Usage
	if resp != nil {
		used = resp.Usage
	}
	r.settle(&held, used)
	return resp, err
}

// reserve sets aside tokens for a model call, adding them to *held.
//
// When the tokens fit only once other calls release their reservations,
// reserve waits for them: reservations are pessimistic, so running out of
// room while other calls are in flight is no reason to stop the run. It
// fails with ErrBudgetExhausted, and marks the run exhausted, when the
// tokens would not fit even if every other reservation were released, or
// when no running call could release anything, which happens when every
// holder is itself waiting here for more.
func (r *run) reserve(ctx context.Context, tokens int, held *int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	limit := r.engine.budget.MaxTokens
	for limit > 0 && r.spent.Total()+r.reserved+tokens > limit {
		if r.spent.Total()+*held+tokens > limit || r.reserved-r.waiting-*held <= 0 {
			r.exhausted = true
			return ErrBudgetExhausted
		}
		released := r.released
		r.waiters++
		r.waiting += *held
		r.mu.Unlock()
		select {
		case <-released:
		case <-ctx.Done():
		}
		r.mu.Lock()
		r.waiters--
		r.waiting -= *held
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	r.reserved += tokens
	*held += tokens
	return nil
}

// settle releases a call's reservation and records what it actually used.
func (r *run) settle(held *int, used Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserved -= *held
	*held = 0
	r.spent = r.spent.Add(used)
	if limit := r.engine.budget.MaxTokens; limit > 0 && r.spent.Total() >= limit {
		r.exhausted = true
	}
	close(r.released)
	r.released = make(chan struct{})
}

// emit records e and delivers it to the observers, one event at a time.
func (r *run) emit(e Event) {
	e.Time = time.Now()
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	r.events = append(r.events, e)
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
	budget := r.engine.budget
	req := PlanRequest{Brief: b, Agents: r.agentInfos(), MaxPlans: r.engine.maxPlans,
		Depth: t.Depth, MaxDepth: budget.MaxDepth, RemainingTokens: -1}
	r.mu.Lock()
	defer r.mu.Unlock()
	req.RemainingNodes = budget.MaxNodes - r.nodes
	if budget.MaxTokens > 0 {
		req.RemainingTokens = max(budget.MaxTokens-r.spent.Total()-r.reserved, 0)
	}
	return req
}

// takeNode counts a task about to start against Budget.MaxNodes.
func (r *run) takeNode() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nodes >= r.engine.budget.MaxNodes {
		r.exhausted = true
		return ErrNodeLimit
	}
	r.nodes++
	return nil
}

// stopped reports whether the budget has run out, after which no new work
// is started.
func (r *run) stopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exhausted
}

// canDecompose reports whether t may be split further: it is above the
// depth limit and at least two more tasks may start.
func (r *run) canDecompose(t *Task) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return t.Depth < r.engine.budget.MaxDepth && r.engine.budget.MaxNodes-r.nodes >= 2 && !r.exhausted
}

// solve solves t, recording its outcome on t. path holds the ancestors'
// descriptions; inputs are the outputs of t's dependencies.
func (r *run) solve(ctx context.Context, t *Task, path []string, inputs []SubtaskResult) error {
	t.Status = StatusRunning
	r.emit(Event{Kind: EventTaskStarted, TaskID: t.ID, Text: t.Description})
	err := r.solveTask(ctx, t, path, inputs)
	if err != nil {
		t.Status = StatusFailed
		t.Error = err.Error()
		r.emit(Event{Kind: EventTaskFailed, TaskID: t.ID, Text: t.Error})
		return fmt.Errorf("task %s: %w", t.ID, err)
	}
	if t.Status == StatusPartial {
		r.emit(Event{Kind: EventTaskPartial, TaskID: t.ID, Score: t.Score, Text: t.Output})
		return nil
	}
	t.Status = StatusDone
	r.emit(Event{Kind: EventTaskDone, TaskID: t.ID, Score: t.Score, Text: t.Output})
	return nil
}

func (r *run) solveTask(ctx context.Context, t *Task, path []string, inputs []SubtaskResult) error {
	b := Brief{Goal: r.goal, Path: path, Task: t.Description, Inputs: inputs, Notes: r.board.Recent(r.engine.notesInPrompt)}
	if r.canDecompose(t) {
		d, err := r.planner.Plan(ctx, r.planRequest(t, b))
		switch {
		case errors.Is(err, ErrBudgetExhausted) || ctx.Err() != nil:
			return fmt.Errorf("plan: %w", err)
		case err == nil && !d.Atomic:
			if plans := r.buildPlans(t, d.Plans); len(plans) > 0 {
				r.emit(Event{Kind: EventTaskDecided, TaskID: t.ID, Text: strconv.Itoa(len(plans))})
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
	var plans []*Plan
	for _, pp := range proposals {
		if len(plans) == r.engine.maxPlans {
			break
		}
		if !validProposal(pp) {
			continue
		}
		p := &Plan{ID: parent.ID + ".p" + strconv.Itoa(len(plans)+1), Rationale: pp.Rationale, Status: StatusPending}
		ids := make(map[string]string, len(pp.Subtasks))
		for i, sp := range pp.Subtasks {
			ids[sp.ID] = p.ID + ".t" + strconv.Itoa(i+1)
		}
		for _, sp := range pp.Subtasks {
			st := &Task{ID: ids[sp.ID], Description: sp.Description, Agent: r.agent(sp.Agent).Name, Depth: parent.Depth + 1, Status: StatusPending}
			for _, d := range sp.DependsOn {
				st.DependsOn = append(st.DependsOn, ids[d])
			}
			p.Subtasks = append(p.Subtasks, st)
		}
		plans = append(plans, p)
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
	if p == nil || !p.Status.usable() {
		return errors.New("strategy returned no successful plan")
	}
	t.Chosen, t.Output, t.Score = p.ID, p.Output, p.ResultScore
	if p.Status == StatusPartial {
		t.Status = StatusPartial
	}
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
	s, err := x.run.evaluator.ScorePlan(ctx, x.brief, proposalOf(p))
	if err != nil {
		return nil
	}
	p.PlanScore = &s
	x.run.emit(Event{Kind: EventPlanScored, TaskID: x.task.ID, PlanID: p.ID, Score: &s})
	return p.PlanScore
}

func (x *exploration) ScoreResult(ctx context.Context, p *Plan) *Score {
	s, err := x.run.evaluator.ScoreResult(ctx, x.brief, p.Output)
	if err != nil {
		return nil
	}
	p.ResultScore = &s
	x.run.emit(Event{Kind: EventResultScored, TaskID: x.task.ID, PlanID: p.ID, Score: &s})
	return p.ResultScore
}

func (x *exploration) Prune(p *Plan) {
	p.Status = StatusPruned
	x.run.emit(Event{Kind: EventPlanPruned, TaskID: x.task.ID, PlanID: p.ID})
}

func (x *exploration) Execute(ctx context.Context, p *Plan) error {
	p.Status = StatusRunning
	x.run.emit(Event{Kind: EventPlanStarted, TaskID: x.task.ID, PlanID: p.ID, Text: p.Rationale})
	partial, err := x.run.executePlan(ctx, x.task, x.brief, p)
	if err != nil {
		p.Status = StatusFailed
		p.Error = err.Error()
		x.run.emit(Event{Kind: EventPlanFailed, TaskID: x.task.ID, PlanID: p.ID, Text: p.Error})
		return fmt.Errorf("plan %s: %w", p.ID, err)
	}
	if partial {
		p.Status = StatusPartial
		x.run.emit(Event{Kind: EventPlanPartial, TaskID: x.task.ID, PlanID: p.ID, Text: p.Output})
		return nil
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

// notCompleted stands in for the output of a subtask the budget did not
// allow to finish.
const notCompleted = "[NOT COMPLETED: the budget ran out before this subtask was done]"

// executePlan solves p's subtasks, each as soon as its dependencies are
// done, then synthesizes their outputs.
//
// A failure other than the budget running out cancels the remaining
// subtasks and is returned. When the budget runs out, subtasks not yet
// started are skipped, those in flight finish, and if any subtask
// succeeded the plan's output is assembled from them and reported partial.
func (r *run) executePlan(parentCtx context.Context, parent *Task, b Brief, p *Plan) (partial bool, err error) {
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
		wg        sync.WaitGroup
		mu        sync.Mutex
		hardErr   error // first failure other than the budget; cancels the rest
		budgetErr error // first budget refusal
	)
	for _, st := range p.Subtasks {
		wg.Go(func() {
			defer close(done[st.ID])
			var inputs []SubtaskResult
			for _, d := range st.DependsOn {
				<-done[d] // every subtask closes its channel, even when skipped
				dep := byID[d]
				if !dep.Status.usable() {
					st.Status = StatusPruned
					return
				}
				inputs = append(inputs, SubtaskResult{Description: dep.Description, Output: dep.Output})
			}
			if ctx.Err() != nil {
				st.Status = StatusPruned
				return
			}
			err := ErrBudgetExhausted
			if !r.stopped() {
				err = r.takeNode()
			}
			if err != nil {
				st.Status, st.Error = StatusPruned, "not started: "+err.Error()
			} else {
				err = r.solve(ctx, st, path, inputs)
			}
			if err == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, ErrBudgetExhausted) {
				budgetErr = cmp.Or(budgetErr, err)
				return
			}
			if hardErr == nil {
				hardErr = err
				cancel()
			}
		})
	}
	wg.Wait()
	// A cancellation from the caller is a failure, never a partial result.
	if err := parentCtx.Err(); err != nil {
		return false, err
	}
	if hardErr != nil {
		return false, hardErr
	}

	results := make([]SubtaskResult, len(p.Subtasks))
	finished := 0
	for i, st := range p.Subtasks {
		results[i] = SubtaskResult{Description: st.Description, Output: st.Output}
		switch {
		case st.Status == StatusDone:
			finished++
		case st.Status == StatusPartial:
			finished++
			partial = true
		default:
			results[i].Output = notCompleted
			partial = true
		}
	}
	if finished == 0 {
		return false, cmp.Or(budgetErr, errors.New("no subtask finished"))
	}
	out, err := r.synthesizer.Synthesize(ctx, b, results)
	switch {
	case errors.Is(err, ErrBudgetExhausted):
		out = joinResults(results)
	case err != nil:
		return false, fmt.Errorf("synthesize: %w", err)
	}
	p.Output = out
	return partial, nil
}
