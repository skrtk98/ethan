package ethan

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
)

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

// start counts t against Limits.MaxTasks and solves it, unless it was
// already done in the run being resumed.
func (r *run) start(ctx context.Context, t *Task, path []string, inputs []SubtaskResult) error {
	if t.Status == StatusDone {
		return nil
	}
	if err := r.takeTask(); err != nil {
		r.prune(t, "not started: "+err.Error())
		return err
	}
	return r.solve(ctx, t, path, inputs)
}

// prune records that t was not started, and why.
func (r *run) prune(t *Task, reason string) {
	t.Status, t.Error = StatusPruned, reason
	r.emit(Event{Kind: EventTaskPruned, TaskID: t.ID, Text: reason})
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
			if err == nil {
				return ctx.Err() // the decision came too late to be used
			}
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
				if !slices.Contains(st.DependsOn, ids[d]) {
					st.DependsOn = append(st.DependsOn, ids[d])
				}
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
		var ds []string
		for _, d := range s.DependsOn {
			if !slices.Contains(ds, d) {
				ds = append(ds, d)
			}
		}
		deps[s.ID] = ds
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
			if st.Status == StatusDone {
				return // finished in an earlier run
			}
			var inputs []SubtaskResult
			for _, d := range st.DependsOn {
				<-done[d] // every subtask closes its channel, even when skipped
				dep := byID[d]
				if dep.Status != StatusDone {
					r.prune(st, "dependency "+dep.ID+" did not finish")
					return
				}
				inputs = append(inputs, SubtaskResult{Description: dep.Description, Output: dep.Output})
			}
			if ctx.Err() != nil {
				r.prune(st, "canceled")
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
