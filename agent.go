package ethan

import (
	"context"
	"encoding/json"
)

// Tool is a client-side capability an Agent can invoke.
// Implementations must be safe for concurrent use.
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
	Tools []Tool
	// MaxTurns bounds the tool-use loop. Zero means 10.
	MaxTurns int
}

// DefaultAgent is used when no agents are registered.
var DefaultAgent = Agent{
	Name:        "generalist",
	Description: "General-purpose agent that can handle any task.",
}
