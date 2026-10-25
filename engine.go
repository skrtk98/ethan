package ethan

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"text/template"
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
	notesInPrompt   int
	maxPlans        int
	maxOutputTokens int
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

// WithNotesInPrompt sets how many of the latest blackboard notes are shown
// in prompts; zero or less shows none. Default 20.
func WithNotesInPrompt(n int) Option {
	return func(e *Engine) { e.notesInPrompt = n }
}

// WithMaxPlans sets how many alternative plans the planner may propose.
// Default 3, also used when n < 1.
func WithMaxPlans(n int) Option {
	return func(e *Engine) { e.maxPlans = n }
}

// WithMaxOutputTokens bounds the output tokens, including thinking, of each
// model call. Default 16000. It applies to LLMPlanner, LLMEvaluator and
// LLMSynthesizer values whose own limit is zero.
func WithMaxOutputTokens(n int) Option {
	return func(e *Engine) { e.maxOutputTokens = n }
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
	if e.maxPlans < 1 {
		e.maxPlans = 3
	}
	e.limits = e.limits.withDefaults()
	return e
}

// Result is the outcome of a Run. It marshals to JSON as the Run's trace.
type Result struct {
	Goal   string `json:"goal"`
	Output string `json:"output"`
	// Value is the output converted as Into asked, if it did.
	Value json.RawMessage `json:"value,omitempty"`
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

// Run solves goal and returns the root task's output. The Result is never
// nil: on failure it still holds the explored tree. When a limit is exceeded
// the whole Run is canceled and the error wraps ErrLimitExceeded.
func (e *Engine) Run(ctx context.Context, goal string, opts ...RunOption) (*Result, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	r := e.newRun(goal, cancel, opts)
	ctx = context.WithValue(ctx, runKey{}, r)
	root := &Task{ID: "t0", Description: goal, Agent: e.agents[0].Name, Status: StatusPending}
	r.emit(Event{Kind: EventRunStarted, Text: goal, Agent: root.Agent, Format: JournalFormat})
	return r.finish(ctx, root, r.solveGoal(ctx, root))
}

// Resume continues the run recorded in events, as read from its journal
// with ReadJournal. Finished tasks and plans, decisions, scores and notes
// are reused without calling any model; everything else, including tasks
// that were running when the run stopped, is solved again. Limits apply to
// the resumed run alone, while Result.Usage includes the earlier runs.
//
// Events that cannot be rebuilt into a run are rejected, with a nil Result
// and an error wrapping ErrJournalFormat or ErrInvalidJournal; otherwise
// the Result is never nil. To keep the journal complete, pass JournalTo with the same journal to
// append to. The returned Result holds the events of the earlier runs too.
func (e *Engine) Resume(ctx context.Context, events []Event, opts ...RunOption) (*Result, error) {
	s, err := replay(events)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	root := s.root
	reopen(root)
	r := e.newRun(root.Description, cancel, opts)
	ctx = context.WithValue(ctx, runKey{}, r)
	r.prior = s.usage()
	r.board.notes = s.notes
	r.events = slices.Clone(events)
	r.value = s.value
	r.emit(Event{Kind: EventRunResumed})
	return r.finish(ctx, root, r.solveGoal(ctx, root))
}

// RunOption configures one Run or Resume.
type RunOption func(*run)

// ObserveWith delivers the events of the run, and of no other, to o.
func ObserveWith(o Observer) RunOption {
	return func(r *run) { r.observers = append(r.observers, o) }
}

// JournalTo records the events of the run in j as they happen, so that it
// can be traced and resumed after it stops. A journal holds one run and the
// Resumes that continue it: give each Run a journal of its own. The run is
// canceled if j fails.
func JournalTo(j Journal) RunOption {
	return func(r *run) { r.journal = j }
}
