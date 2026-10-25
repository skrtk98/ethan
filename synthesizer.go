package ethan

import (
	"context"
	"text/template"
)

// Synthesizer combines the results of a plan's subtasks into the output of
// the task the plan decomposes.
type Synthesizer interface {
	Synthesize(ctx context.Context, b Brief, results []SubtaskResult) (string, error)
}

// LLMSynthesizer is the default Synthesizer.
type LLMSynthesizer struct {
	// Model is called for the synthesis; nil means the engine's model. Within
	// a Run, the calls count against its Limits whichever model makes them.
	Model Model
	// Template overrides DefaultTemplate("synthesize").
	Template  *template.Template
	MaxTokens int
}

// Synthesize implements Synthesizer.
func (s *LLMSynthesizer) Synthesize(ctx context.Context, b Brief, results []SubtaskResult) (string, error) {
	return generateText(ctx, s.Model, TagSynthesize, orDefault(s.Template, "synthesize"),
		struct {
			Brief   Brief
			Results []SubtaskResult
		}{b, results}, s.MaxTokens)
}
