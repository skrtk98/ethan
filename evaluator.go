package ethan

import (
	"context"
	"encoding/json"
	"text/template"
)

// Evaluator scores plans before execution and results after it.
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
	Model Model
	// PlanTemplate and ResultTemplate override DefaultTemplate("score_plan")
	// and DefaultTemplate("score_result").
	PlanTemplate   *template.Template
	ResultTemplate *template.Template
	MaxTokens      int
}

// ScorePlan implements Evaluator.
func (e *LLMEvaluator) ScorePlan(ctx context.Context, b Brief, p PlanProposal) (Score, error) {
	var s Score
	err := generateJSON(ctx, e.Model, TagScorePlan, orDefault(e.PlanTemplate, "score_plan"),
		struct {
			Brief Brief
			Plan  PlanProposal
		}{b, p}, scoreSchema, e.MaxTokens, &s)
	return clamp(s), err
}

// ScoreResult implements Evaluator.
func (e *LLMEvaluator) ScoreResult(ctx context.Context, b Brief, output string) (Score, error) {
	var s Score
	err := generateJSON(ctx, e.Model, TagScoreResult, orDefault(e.ResultTemplate, "score_result"),
		struct {
			Brief  Brief
			Output string
		}{b, output}, scoreSchema, e.MaxTokens, &s)
	return clamp(s), err
}

func clamp(s Score) Score {
	s.Value = min(max(s.Value, 0), 1)
	return s
}
