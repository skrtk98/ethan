package ethan

import (
	"context"
	"encoding/json"
	"text/template"
)

// Evaluator scores plans before execution and results after it.
//
// The engine calls it concurrently for the tasks and plans it explores in
// parallel, so implementations must be safe for concurrent use.
type Evaluator interface {
	ScorePlan(ctx context.Context, b Brief, p PlanProposal) (Score, error)
	ScoreResult(ctx context.Context, b Brief, output string) (Score, error)
}

var scoreSchema = &OutputSchema{
	Name:        "score",
	Description: "An evaluation score with its justification.",
	Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "reason": {"type": "string", "description": "brief justification, written before deciding the value"},
    "value": {"type": "number", "description": "from 0 to 1"}
  },
  "required": ["reason", "value"],
  "additionalProperties": false
}`),
}

// LLMEvaluator is the default Evaluator: an LLM acting as a judge.
type LLMEvaluator struct {
	// Model is called for the scores; nil means the engine's model. Within
	// a Run, the calls count against its Limits whichever model makes them.
	Model Model
	// PlanTemplate and ResultTemplate override DefaultTemplate("score_plan")
	// and DefaultTemplate("score_result").
	PlanTemplate   *template.Template
	ResultTemplate *template.Template
	// MaxOutputTokens bounds the output tokens, thinking included, of each
	// call; zero means the engine's WithMaxOutputTokens within a Run.
	MaxOutputTokens int
}

// ScorePlan implements Evaluator.
func (e *LLMEvaluator) ScorePlan(ctx context.Context, b Brief, p PlanProposal) (Score, error) {
	var s Score
	err := generateJSON(ctx, e.Model, TagScorePlan, templateFor(ctx, e.PlanTemplate, "score_plan"),
		struct {
			Brief Brief
			Plan  PlanProposal
		}{b, p}, scoreSchema, e.MaxOutputTokens, &s)
	return clamp(s), err
}

// ScoreResult implements Evaluator.
func (e *LLMEvaluator) ScoreResult(ctx context.Context, b Brief, output string) (Score, error) {
	var s Score
	err := generateJSON(ctx, e.Model, TagScoreResult, templateFor(ctx, e.ResultTemplate, "score_result"),
		struct {
			Brief  Brief
			Output string
		}{b, output}, scoreSchema, e.MaxOutputTokens, &s)
	return clamp(s), err
}

func clamp(s Score) Score {
	s.Value = min(max(s.Value, 0), 1)
	return s
}
