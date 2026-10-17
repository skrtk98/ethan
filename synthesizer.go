package ethan

import (
	"context"
	"strings"
	"text/template"
)

// Synthesizer combines the results of a plan's subtasks into the output of
// the task the plan decomposes.
type Synthesizer interface {
	Synthesize(ctx context.Context, b Brief, results []SubtaskResult) (string, error)
}

// LLMSynthesizer is the default Synthesizer.
type LLMSynthesizer struct {
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

// joinResults is the fallback synthesis used when the budget is exhausted.
func joinResults(results []SubtaskResult) string {
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("## " + r.Description + "\n\n" + r.Output)
	}
	return b.String()
}
