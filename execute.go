package ethan

import (
	"context"
	"errors"
	"fmt"
	"text/template"
)

// ErrMaxTurns is returned when an agent does not finish within MaxTurns.
var ErrMaxTurns = errors.New("ethan: agent exceeded max turns")

type executeData struct {
	Brief        Brief
	Instructions string
	HasNoteTool  bool
}

// execute runs agent on the task described by b, looping over tool calls.
func execute(ctx context.Context, m Model, t *template.Template, agent Agent, tools []Tool, b Brief, maxTokens int) (string, error) {
	system, user, err := render(t, executeData{Brief: b, Instructions: agent.Instructions, HasNoteTool: hasTool(tools, noteToolName)})
	if err != nil {
		return "", err
	}
	byName := make(map[string]Tool, len(tools))
	specs := make([]ToolSpec, 0, len(tools))
	for _, tool := range tools {
		spec := tool.Spec()
		byName[spec.Name] = tool
		specs = append(specs, spec)
	}

	maxTurns := agent.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 10
	}
	msgs := []Message{UserText(user)}
	for range maxTurns {
		resp, err := m.Generate(ctx, Request{
			Tag:       TagExecute,
			System:    system,
			Messages:  msgs,
			Tools:     specs,
			MaxTokens: maxTokensOr(maxTokens),
		})
		if err != nil {
			return "", err
		}
		calls := resp.ToolCalls()
		if len(calls) == 0 {
			return resp.Text(), nil
		}
		msgs = append(msgs, Message{Role: RoleAssistant, Parts: resp.Parts})
		results := make([]Part, len(calls))
		for i, c := range calls {
			results[i] = Part{ToolResult: callTool(ctx, byName, c)}
		}
		msgs = append(msgs, Message{Role: RoleUser, Parts: results})
	}
	return "", fmt.Errorf("%w (%d)", ErrMaxTurns, maxTurns)
}

func callTool(ctx context.Context, tools map[string]Tool, c *ToolCall) *ToolResult {
	tool, ok := tools[c.Name]
	if !ok {
		return &ToolResult{CallID: c.ID, Content: "unknown tool " + c.Name, IsError: true}
	}
	out, err := tool.Call(ctx, c.Input)
	if err != nil {
		return &ToolResult{CallID: c.ID, Content: err.Error(), IsError: true}
	}
	return &ToolResult{CallID: c.ID, Content: out}
}

func hasTool(tools []Tool, name string) bool {
	for _, t := range tools {
		if t.Spec().Name == name {
			return true
		}
	}
	return false
}
