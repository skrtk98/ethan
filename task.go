package ethan

// Status is the lifecycle state of a Task or Plan.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	// StatusPartial means the budget ran out before every subtask finished;
	// the output is assembled from those that did and names the rest.
	StatusPartial Status = "partial"
	StatusFailed  Status = "failed"
	StatusPruned  Status = "pruned"
)

func (s Status) usable() bool { return s == StatusDone || s == StatusPartial }

// Task is an OR node of the exploration tree: it is solved either directly
// by an agent or by exactly one of its candidate Plans.
//
// A Task is owned by the goroutine solving it; read it only after Run returns.
type Task struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Agent       string   `json:"agent"`
	Depth       int      `json:"depth"`
	DependsOn   []string `json:"depends_on,omitempty"`
	Status      Status   `json:"status"`
	// Atomic reports whether the task was executed directly.
	Atomic bool    `json:"atomic"`
	Plans  []*Plan `json:"plans,omitempty"`
	// Chosen is the ID of the plan whose output became the task's output.
	Chosen string `json:"chosen,omitempty"`
	Output string `json:"output,omitempty"`
	Score  *Score `json:"score,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Plan is an AND node: all of its subtasks must succeed.
type Plan struct {
	ID          string  `json:"id"`
	Rationale   string  `json:"rationale"`
	Subtasks    []*Task `json:"subtasks"`
	Status      Status  `json:"status"`
	PlanScore   *Score  `json:"plan_score,omitempty"`
	ResultScore *Score  `json:"result_score,omitempty"`
	Output      string  `json:"output,omitempty"`
	Error       string  `json:"error,omitempty"`
}

// Score is an evaluation in [0, 1] with its justification.
type Score struct {
	Value  float64 `json:"value"`
	Reason string  `json:"reason"`
}

// Brief is everything a component needs to know about the task at hand.
type Brief struct {
	// Goal is the root task's description.
	Goal string
	// Path lists ancestor task descriptions from the root down to the parent.
	Path []string
	Task string
	// Inputs are the outputs of the subtasks this task depends on.
	Inputs []SubtaskResult
	Notes  []Note
}

// SubtaskResult is a finished subtask's description and output.
type SubtaskResult struct {
	Description string
	Output      string
}
