package ethan

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"text/template"
)

// Planner decides whether a task is executed directly or decomposed, and
// proposes alternative plans for decomposition.
type Planner interface {
	Plan(ctx context.Context, req PlanRequest) (Decision, error)
}

// PlanRequest is the input to a Planner.
type PlanRequest struct {
	Brief    Brief
	Agents   []AgentInfo
	MaxPlans int
	// Depth is the task's depth; tasks at MaxDepth are always executed directly.
	Depth    int
	MaxDepth int
}

// AgentInfo is what the planner knows about an agent.
type AgentInfo struct {
	Name        string
	Description string
}

// Decision is a Planner's answer.
type Decision struct {
	Atomic bool           `json:"atomic"`
	Plans  []PlanProposal `json:"plans,omitempty"`
}

// PlanProposal is a candidate decomposition of a task.
type PlanProposal struct {
	Rationale string            `json:"rationale"`
	Subtasks  []SubtaskProposal `json:"subtasks"`
}

// SubtaskProposal is one subtask of a PlanProposal. ID is local to the plan
// and is what DependsOn refers to. Agent names one of the Engine's agents;
// empty means the first. A plan naming an unknown agent is dropped.
type SubtaskProposal struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Agent       string   `json:"agent"`
	DependsOn   []string `json:"depends_on,omitempty"`
}

var decisionSchema = &OutputSchema{
	Name:        "decision",
	Description: "Whether to execute the task directly, and if not, alternative plans.",
	Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "atomic": {"type": "boolean", "description": "true if a single agent can complete the task directly; plans is then empty"},
    "plans": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "rationale": {"type": "string"},
          "subtasks": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "id": {"type": "string"},
                "description": {"type": "string"},
                "agent": {"type": "string"},
                "depends_on": {"type": "array", "items": {"type": "string"}}
              },
              "required": ["id", "description", "agent", "depends_on"],
              "additionalProperties": false
            }
          }
        },
        "required": ["rationale", "subtasks"],
        "additionalProperties": false
      }
    }
  },
  "required": ["atomic", "plans"],
  "additionalProperties": false
}`),
}

// LLMPlanner is the default Planner. It asks Model for a structured decision.
type LLMPlanner struct {
	// Model is called for the decisions; nil means the engine's model. Within
	// a Run, the calls count against its Limits whichever model makes them.
	Model Model
	// Template overrides DefaultTemplate("plan").
	Template *template.Template
	// MaxOutputTokens bounds the output tokens, thinking included, of each
	// call; zero means the engine's WithMaxOutputTokens within a Run.
	MaxOutputTokens int
}

// Plan implements Planner.
func (p *LLMPlanner) Plan(ctx context.Context, req PlanRequest) (Decision, error) {
	var d Decision
	err := generateJSON(ctx, p.Model, TagPlan, orDefault(p.Template, "plan"), req, decisionSchema, p.MaxOutputTokens, &d)
	return d, err
}

// generateJSON renders t with data, requests structured output and decodes it into out.
func generateJSON(ctx context.Context, m Model, tag string, t *template.Template, data any, schema *OutputSchema, maxTokens int, out any) error {
	system, user, err := render(t, data)
	if err != nil {
		return err
	}
	m, err = modelFor(ctx, m)
	if err != nil {
		return err
	}
	resp, err := m.Generate(ctx, Request{
		Tag:             tag,
		System:          system,
		Messages:        []Message{UserText(user)},
		MaxOutputTokens: maxOutputFor(ctx, maxTokens),
		Output:          schema,
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resp.Output, out); err != nil {
		return fmt.Errorf("%s: decode %s: %w", tag, schema.Name, err)
	}
	return nil
}

func generateText(ctx context.Context, m Model, tag string, t *template.Template, data any, maxTokens int) (string, error) {
	system, user, err := render(t, data)
	if err != nil {
		return "", err
	}
	m, err = modelFor(ctx, m)
	if err != nil {
		return "", err
	}
	resp, err := m.Generate(ctx, Request{
		Tag:             tag,
		System:          system,
		Messages:        []Message{UserText(user)},
		MaxOutputTokens: maxOutputFor(ctx, maxTokens),
	})
	if err != nil {
		return "", err
	}
	return resp.Text(), nil
}

var defaultTemplates sync.Map // name -> *template.Template

func orDefault(t *template.Template, name string) *template.Template {
	if t != nil {
		return t
	}
	if v, ok := defaultTemplates.Load(name); ok {
		return v.(*template.Template)
	}
	v, _ := defaultTemplates.LoadOrStore(name, DefaultTemplate(name))
	return v.(*template.Template)
}

func maxTokensOr(n int) int {
	if n > 0 {
		return n
	}
	return 16000
}
