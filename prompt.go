package ethan

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed prompts/*.tmpl
var promptFS embed.FS

var promptFuncs = template.FuncMap{"join": strings.Join}

// DefaultTemplate returns a fresh copy of a built-in prompt template:
// "plan", "execute", "score_plan", "score_result" or "synthesize".
// Every template defines "system" and "user"; to customize a prompt,
// redefine either of them on the returned template and pass it to the
// corresponding component.
func DefaultTemplate(name string) *template.Template {
	return template.Must(template.New(name).Funcs(promptFuncs).
		ParseFS(promptFS, "prompts/brief.tmpl", "prompts/"+name+".tmpl"))
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
