package ethantest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/skrtk98/ethan"
)

// Turn is one answer of a fake provider API, described independently of
// its wire format.
type Turn struct {
	Text      string
	ToolCalls []ethan.ToolCall
	Usage     ethan.Usage
	// Truncated reports that the answer hit the output token limit.
	Truncated bool
	// Fail answers with an HTTP 500 error instead.
	Fail bool
}

// Seen is what a fake provider API received in one request, decoded from
// its wire format.
type Seen struct {
	System      string
	UserTexts   []string
	ToolCalls   []ethan.ToolCall // replayed from earlier answers
	ToolResults []ethan.ToolResult
	Tools       []string // names of the client-side tools offered
	Structured  bool     // whether structured output was requested
}

// Fake starts a fake provider API answering turns in order, and returns a
// Model talking to it and a function returning the requests it received.
type Fake func(t *testing.T, turns []Turn) (m ethan.Model, seen func() []Seen)

// TestModel checks that a Model adapter maps requests and responses
// faithfully. Each adapter runs it against a fake of its provider's API.
func TestModel(t *testing.T, fake Fake) {
	usage := ethan.Usage{InputTokens: 50, OutputTokens: 7, CacheReadTokens: 30}

	t.Run("text", func(t *testing.T) {
		m, seen := fake(t, []Turn{{Text: "hello", Usage: usage}})
		resp, err := m.Generate(context.Background(), ethan.Request{
			System: "be brief", Messages: []ethan.Message{ethan.UserText("hi")}, MaxOutputTokens: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Text() != "hello" || resp.Usage != usage {
			t.Errorf("text %q usage %+v, want hello %+v", resp.Text(), resp.Usage, usage)
		}
		s := seen()[0]
		if s.System != "be brief" || len(s.UserTexts) != 1 || s.UserTexts[0] != "hi" {
			t.Errorf("seen %+v", s)
		}
	})

	t.Run("structured output", func(t *testing.T) {
		m, seen := fake(t, []Turn{{Text: `{"value":1}`, Usage: usage}})
		resp, err := m.Generate(context.Background(), ethan.Request{
			Messages: []ethan.Message{ethan.UserText("rate")}, MaxOutputTokens: 100,
			Output: &ethan.OutputSchema{Name: "score", Schema: json.RawMessage(
				`{"type":"object","properties":{"value":{"type":"number"}},"required":["value"],"additionalProperties":false}`)},
		})
		if err != nil {
			t.Fatal(err)
		}
		if string(resp.Output) != `{"value":1}` {
			t.Errorf("Output = %s", resp.Output)
		}
		if !seen()[0].Structured {
			t.Error("structured output was not requested")
		}
	})

	t.Run("tool round trip", func(t *testing.T) {
		call := ethan.ToolCall{ID: "call-1", Name: "lookup", Input: json.RawMessage(`{"q":"x"}`)}
		m, seen := fake(t, []Turn{{ToolCalls: []ethan.ToolCall{call}, Usage: usage}, {Text: "found", Usage: usage}})
		req := ethan.Request{
			Messages:        []ethan.Message{ethan.UserText("find x")},
			Tools:           []ethan.ToolSpec{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}},
			MaxOutputTokens: 100,
		}
		resp, err := m.Generate(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		calls := resp.ToolCalls()
		if len(calls) != 1 || calls[0].ID != call.ID || calls[0].Name != call.Name {
			t.Fatalf("tool calls %+v", calls)
		}
		var input map[string]string
		if err := json.Unmarshal(calls[0].Input, &input); err != nil || input["q"] != "x" {
			t.Errorf("input %s", calls[0].Input)
		}

		req.Messages = append(req.Messages,
			ethan.Message{Role: ethan.RoleAssistant, Parts: resp.Parts},
			ethan.Message{Role: ethan.RoleUser, Parts: []ethan.Part{{ToolResult: &ethan.ToolResult{CallID: call.ID, Content: "x is 42"}}}},
		)
		resp, err = m.Generate(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Text() != "found" {
			t.Errorf("text %q", resp.Text())
		}
		s := seen()
		if len(s[0].Tools) != 1 || s[0].Tools[0] != "lookup" {
			t.Errorf("tools offered %v", s[0].Tools)
		}
		if len(s[1].ToolCalls) != 1 || s[1].ToolCalls[0].ID != call.ID {
			t.Errorf("replayed tool calls %+v", s[1].ToolCalls)
		}
		if len(s[1].ToolResults) != 1 || s[1].ToolResults[0].CallID != call.ID || s[1].ToolResults[0].Content != "x is 42" {
			t.Errorf("tool results %+v", s[1].ToolResults)
		}
	})

	t.Run("truncated text is an error", func(t *testing.T) {
		m, _ := fake(t, []Turn{{Text: "the report beg", Usage: usage, Truncated: true}})
		resp, err := m.Generate(context.Background(), ethan.Request{Messages: []ethan.Message{ethan.UserText("report")}, MaxOutputTokens: 5})
		if !errors.Is(err, ethan.ErrTruncated) {
			t.Errorf("err = %v, want ErrTruncated", err)
		}
		if resp == nil || resp.Usage != usage {
			t.Errorf("response %+v, want usage %+v", resp, usage)
		}
	})

	t.Run("truncated structured output keeps usage", func(t *testing.T) {
		m, _ := fake(t, []Turn{{Text: `{"val`, Usage: usage, Truncated: true}})
		resp, err := m.Generate(context.Background(), ethan.Request{
			Messages: []ethan.Message{ethan.UserText("rate")}, MaxOutputTokens: 5,
			Output: &ethan.OutputSchema{Name: "score", Schema: json.RawMessage(`{"type":"object"}`)},
		})
		if !errors.Is(err, ethan.ErrTruncated) {
			t.Errorf("err = %v, want ErrTruncated", err)
		}
		if resp == nil || resp.Usage != usage {
			t.Errorf("response %+v, want usage %+v", resp, usage)
		}
	})

	t.Run("truncated tool call is not returned", func(t *testing.T) {
		// A call cut off at the output limit may have partial arguments.
		call := ethan.ToolCall{ID: "call-1", Name: "lookup", Input: json.RawMessage(`{"q":"x"}`)}
		m, _ := fake(t, []Turn{{ToolCalls: []ethan.ToolCall{call}, Usage: usage, Truncated: true}})
		resp, err := m.Generate(context.Background(), ethan.Request{
			Messages: []ethan.Message{ethan.UserText("find x")}, MaxOutputTokens: 5,
			Tools: []ethan.ToolSpec{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		})
		if !errors.Is(err, ethan.ErrTruncated) {
			t.Errorf("err = %v, want ErrTruncated", err)
		}
		if resp == nil || resp.Usage != usage {
			t.Errorf("response %+v, want usage %+v", resp, usage)
		}
	})

	t.Run("API error", func(t *testing.T) {
		m, _ := fake(t, []Turn{{Fail: true}})
		if _, err := m.Generate(context.Background(), ethan.Request{Messages: []ethan.Message{ethan.UserText("hi")}, MaxOutputTokens: 10}); err == nil {
			t.Error("no error")
		}
	})

	t.Run("canceled", func(t *testing.T) {
		m, _ := fake(t, []Turn{{Text: "too late"}})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := m.Generate(ctx, ethan.Request{Messages: []ethan.Message{ethan.UserText("hi")}, MaxOutputTokens: 10}); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}
