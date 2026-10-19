package ethantest

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/skrtk98/ethan"
)

var taskRe = regexp.MustCompile(`(?s)<task>\n(.*?)\n</task>`)

// Task returns the description of the task a request is about, read from
// the prompt as the default templates write it, or "" if there is none. It
// is a test heuristic: with other templates, or a task description holding
// the template's tags, it may misread the prompt.
func Task(req ethan.Request) string {
	m := taskRe.FindStringSubmatch(Prompt(req))
	if m == nil {
		return ""
	}
	return m[1]
}

// IsGoal reports whether a request is about the goal itself rather than one
// of its subtasks, read from the prompt as Task is.
func IsGoal(req ethan.Request) bool {
	return !strings.Contains(Prompt(req), "<ancestors>")
}

// Decompose is a planner Handler that splits the goal into plans and has
// every subtask executed directly.
func Decompose(plans ...ethan.PlanProposal) Handler {
	return func(req ethan.Request) (*ethan.Response, error) {
		if !IsGoal(req) {
			return JSON(ethan.Decision{Atomic: true}), nil
		}
		return JSON(ethan.Decision{Plans: plans}), nil
	}
}

// Atomic is a planner Handler that has every task executed directly.
func Atomic() Handler {
	return ReplyJSON(ethan.Decision{Atomic: true})
}

// ByTask routes requests to handlers by the description of their task, as
// Task reads it. Requests about other tasks fail.
func ByTask(handlers map[string]Handler) Handler {
	return func(req ethan.Request) (*ethan.Response, error) {
		task := Task(req)
		h, ok := handlers[task]
		if !ok {
			return nil, fmt.Errorf("ethantest: no handler for task %q", task)
		}
		return h(req)
	}
}

// Sequence answers successive requests with successive handlers, e.g. a
// tool call and then the final text of an agent's tool loop. Requests after
// the last handler fail. Concurrent requests get handlers in the order they
// arrive, which varies, so give each task its own Sequence, e.g. under
// ByTask.
func Sequence(handlers ...Handler) Handler {
	var (
		mu sync.Mutex
		n  int
	)
	return func(req ethan.Request) (*ethan.Response, error) {
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		if i >= len(handlers) {
			return nil, fmt.Errorf("ethantest: sequence of %d handlers exhausted", len(handlers))
		}
		return handlers[i](req)
	}
}

// ToolCalls builds a response asking for calls.
func ToolCalls(calls ...ethan.ToolCall) *ethan.Response {
	parts := make([]ethan.Part, len(calls))
	for i, c := range calls {
		parts[i] = ethan.Part{ToolCall: &c}
	}
	return &ethan.Response{Parts: parts, Usage: DefaultUsage}
}

// Reply answers with resp. Each call gets resp itself, not a copy.
func Reply(resp *ethan.Response) Handler {
	return func(ethan.Request) (*ethan.Response, error) { return resp, nil }
}
