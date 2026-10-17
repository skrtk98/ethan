package ethan

import (
	"context"
	"encoding/json"
	"strings"
)

// Model is the abstraction over an LLM provider.
// Implementations must be safe for concurrent use.
type Model interface {
	Generate(ctx context.Context, req Request) (*Response, error)
}

// Tags identify which component issued a Request. Adapters may ignore them;
// they exist for usage accounting, tracing and scripted test models.
const (
	TagPlan        = "plan"
	TagExecute     = "execute"
	TagScorePlan   = "score_plan"
	TagScoreResult = "score_result"
	TagSynthesize  = "synthesize"
)

// Request is a single model invocation.
type Request struct {
	Tag       string
	System    string
	Messages  []Message
	Tools     []ToolSpec
	MaxTokens int
	// Output, when set, asks the model for a JSON value matching the schema
	// instead of free text. The value is returned in Response.Output.
	Output *OutputSchema
}

// OutputSchema describes a structured output.
type OutputSchema struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Role is the author of a Message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of a conversation.
type Message struct {
	Role  Role
	Parts []Part
}

// Part is one element of a Message. Exactly one field is set.
type Part struct {
	Text       string
	ToolCall   *ToolCall
	ToolResult *ToolResult
}

// ToolCall is a request from the model to run a client-side tool.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult is the outcome of a ToolCall, sent back to the model.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// UserText builds a user message holding a single text part.
func UserText(s string) Message {
	return Message{Role: RoleUser, Parts: []Part{{Text: s}}}
}

// Response is the model's answer to a Request.
type Response struct {
	Parts  []Part
	Output json.RawMessage
	Usage  Usage
}

// Text concatenates the text parts of the response.
func (r *Response) Text() string {
	var b strings.Builder
	for _, p := range r.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// ToolCalls returns the tool calls in the response.
func (r *Response) ToolCalls() []*ToolCall {
	var calls []*ToolCall
	for _, p := range r.Parts {
		if p.ToolCall != nil {
			calls = append(calls, p.ToolCall)
		}
	}
	return calls
}

// Usage counts tokens consumed by model calls.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Total returns input plus output tokens.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

func (u Usage) add(o Usage) Usage {
	return Usage{InputTokens: u.InputTokens + o.InputTokens, OutputTokens: u.OutputTokens + o.OutputTokens}
}
