package ethan

import (
	"context"
	"encoding/json"
	"strings"
)

// Model is the abstraction over an LLM provider.
// Implementations must be safe for concurrent use.
//
// Generate may return a non-nil Response together with an error; its Usage
// then reports the tokens consumed before the failure, and nothing else in
// it is meaningful.
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

// Part is one element of a Message. At most one of Text, ToolCall and
// ToolResult is set.
type Part struct {
	Text       string
	ToolCall   *ToolCall
	ToolResult *ToolResult
	// Raw is the provider's own representation of the part. Adapters set it
	// on response parts and send it back unchanged when the part is replayed,
	// which preserves blocks the abstraction does not model (thinking,
	// server-side tool use). Everything else should ignore it.
	Raw any
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

// Text concatenates the text parts after the last part that is not text,
// such as a tool call or a provider block for a server-side tool. That is
// the model's final answer, without what it said between tool uses.
func (r *Response) Text() string {
	start := 0
	for i, p := range r.Parts {
		if p.Text == "" {
			start = i + 1
		}
	}
	var b strings.Builder
	for _, p := range r.Parts[start:] {
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

// Add returns the sum of u and o.
func (u Usage) Add(o Usage) Usage {
	return Usage{InputTokens: u.InputTokens + o.InputTokens, OutputTokens: u.OutputTokens + o.OutputTokens}
}
