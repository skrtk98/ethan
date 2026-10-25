package ethan

import (
	"context"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed prompts/*.tmpl
var promptFS embed.FS

var promptFuncs = template.FuncMap{"join": strings.Join}

// templateNames are the names of the built-in templates.
var templateNames = []string{"plan", "execute", "score_plan", "score_result", "synthesize", "format"}

// DefaultTemplate returns a fresh copy of a built-in prompt template. It
// panics if name is not one of these, listed with the data each is
// executed with:
//
//   - "plan": a PlanRequest
//   - "execute": .Brief (a Brief), .Instructions (the agent's) and
//     .HasNoteTool (whether the agent can post to the blackboard)
//   - "score_plan": .Brief and .Plan (a PlanProposal)
//   - "score_result": .Brief and .Output (the plan's synthesized output)
//   - "synthesize": .Brief and .Results (a []SubtaskResult)
//   - "format": .Goal and .Result (the goal's output to convert for Into)
//
// Every template defines "system" and "user". To customize a prompt,
// redefine either of them on the returned template and pass it to
// WithTemplate, or to the component that uses it.
func DefaultTemplate(name string) *template.Template {
	return template.Must(template.New(name).Funcs(promptFuncs).
		ParseFS(promptFS, "prompts/brief.tmpl", "prompts/"+name+".tmpl"))
}

// templateFor returns t, or the template the Run's engine has for name, or
// the built-in one.
func templateFor(ctx context.Context, t *template.Template, name string) *template.Template {
	if t != nil {
		return t
	}
	if r, _ := ctx.Value(runKey{}).(*run); r != nil {
		if t := r.engine.templates[name]; t != nil {
			return t
		}
	}
	return orDefault(nil, name)
}

func render(t *template.Template, data any) (system, user string, err error) {
	var s, u strings.Builder
	if err := t.ExecuteTemplate(&s, "system", data); err != nil {
		return "", "", fmt.Errorf("render system prompt: %w", err)
	}
	if err := t.ExecuteTemplate(&u, "user", data); err != nil {
		return "", "", fmt.Errorf("render user prompt: %w", err)
	}
	return strings.TrimSpace(s.String()), strings.TrimSpace(u.String()), nil
}
