package ethan

import (
	"context"
	"encoding/json"
)

// Tool is a client-side capability an Agent can invoke.
// Implementations must be safe for concurrent use. An error from Call, or a
// panic in it, is reported to the model as the tool's result.
type Tool interface {
	Spec() ToolSpec
	Call(ctx context.Context, input json.RawMessage) (string, error)
}

// ToolSpec describes a tool to the model.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Agent is a role that executes leaf tasks.
type Agent struct {
	// Name identifies the agent; the planner assigns subtasks by name.
	Name string
	// Description tells the planner what the agent is good at.
	Description string
	// Instructions are prepended to the agent's system prompt.
	Instructions string
	// Model overrides the engine's model for this agent.
	Model Model
	// Tools are the agent's tools. Their names must be unique, and "note" is
	// taken: the engine adds a tool of that name for posting to the
	// blackboard.
	Tools []Tool
	// MaxTurns bounds the tool-use loop. Zero means 10. A task whose agent
	// has not answered after MaxTurns turns fails with ErrMaxTurns.
	MaxTurns int
}

// defaultAgent is used when no agents are registered.
var defaultAgent = Agent{
	Name:        "generalist",
	Description: "General-purpose agent that can handle any task.",
}
