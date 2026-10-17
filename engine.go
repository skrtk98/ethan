package ethan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"text/template"
)

// Engine explores a goal by building and solving an AND-OR task tree.
// An Engine is safe for concurrent Runs.
type Engine struct {
	model           Model
	agents          []Agent
	planner         Planner
	executeTemplate *template.Template
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

// WithExecuteTemplate replaces the agents' default prompt template.
func WithExecuteTemplate(t *template.Template) Option {
	return func(e *Engine) { e.executeTemplate = t }
}

// WithMaxPlans sets how many alternative plans the planner may propose. Default 3.
func WithMaxPlans(n int) Option {
	return func(e *Engine) { e.maxPlans = n }
}

// WithMaxOutputTokens bounds the output tokens of each model call. Default 4096.
func WithMaxOutputTokens(n int) Option {
	return func(e *Engine) { e.maxTokens = n }
}

// New returns an Engine whose components default to LLM-backed
// implementations using m.
func New(m Model, opts ...Option) *Engine {
	e := &Engine{model: m, maxPlans: 3}
	for _, o := range opts {
		o(e)
	}
	if len(e.agents) == 0 {
		e.agents = []Agent{DefaultAgent}
	}
	if e.executeTemplate == nil {
		e.executeTemplate = DefaultTemplate("execute")
	}
	return e
}

// Result is the outcome of a Run.
type Result struct {
	Output string
	// Score is the evaluation of the root output, if it was evaluated.
	Score *Score
	Root  *Task
	Usage Usage
}

// Run solves goal and returns the root task's output. On failure the
// returned Result still holds the explored tree.
func (e *Engine) Run(ctx context.Context, goal string) (*Result, error) {
	r := &run{engine: e, goal: goal}
	r.model = r.meter(e.model)
	r.planner = e.planner
	if r.planner == nil {
		r.planner = &LLMPlanner{Model: r.model, MaxTokens: e.maxTokens}
	}

	root := &Task{ID: "t0", Description: goal, Agent: e.agents[0].Name, Status: StatusPending}
	err := r.solve(ctx, root, nil, nil)
	res := &Result{Output: root.Output, Score: root.Score, Root: root, Usage: r.usage()}
	return res, err
}

// run holds the state of one Run.
type run struct {
	engine  *Engine
	goal    string
	model   Model
	planner Planner

	mu    sync.Mutex
	spent Usage
}

func (r *run) usage() Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spent
}

// meter wraps m so that its usage is accounted to the run.
func (r *run) meter(m Model) Model {
	return meteredModel{run: r, model: m}
}

type meteredModel struct {
	run   *run
	model Model
}

func (mm meteredModel) Generate(ctx context.Context, req Request) (*Response, error) {
	resp, err := mm.model.Generate(ctx, req)
	if resp != nil {
		mm.run.mu.Lock()
		mm.run.spent = mm.run.spent.add(resp.Usage)
		mm.run.mu.Unlock()
	}
	return resp, err
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

// solve solves t, recording its outcome on t. path holds the ancestors'
// descriptions; inputs are the outputs of t's dependencies.
func (r *run) solve(ctx context.Context, t *Task, path []string, inputs []SubtaskResult) error {
	t.Status = StatusRunning
	err := r.solveTask(ctx, t, path, inputs)
	if err != nil {
		t.Status = StatusFailed
		t.Error = err.Error()
		return fmt.Errorf("task %s: %w", t.ID, err)
	}
	t.Status = StatusDone
	return nil
}

func (r *run) solveTask(ctx context.Context, t *Task, path []string, inputs []SubtaskResult) error {
	b := Brief{Goal: r.goal, Path: path, Task: t.Description, Inputs: inputs}
	d, err := r.planner.Plan(ctx, PlanRequest{Brief: b, Agents: r.agentInfos(), MaxPlans: r.engine.maxPlans})
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if !d.Atomic {
		return errors.New("decomposition is not implemented yet")
	}
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
	out, err := execute(ctx, m, r.engine.executeTemplate, agent, agent.Tools, b, r.engine.maxTokens)
	if err != nil {
		return fmt.Errorf("execute: %w", err)
	}
	t.Output = out
	return nil
}
