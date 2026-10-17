// Package ethantest provides a scripted Model for deterministic tests.
package ethantest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/skrtk98/ethan"
)

// Handler answers a request routed to it.
type Handler func(req ethan.Request) (*ethan.Response, error)

// Model routes each request to a Handler by its Tag and records every call.
// It is safe for concurrent use.
type Model struct {
	mu       sync.Mutex
	handlers map[string]Handler
	calls    []ethan.Request
}

// New returns a Model with no handlers.
func New() *Model {
	return &Model{handlers: map[string]Handler{}}
}

// On registers h for requests carrying tag and returns m for chaining.
func (m *Model) On(tag string, h Handler) *Model {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[tag] = h
	return m
}

// Generate implements ethan.Model.
func (m *Model) Generate(ctx context.Context, req ethan.Request) (*ethan.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.calls = append(m.calls, req)
	h, ok := m.handlers[req.Tag]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("ethantest: no handler for tag %q", req.Tag)
	}
	return h(req)
}

// Calls returns the recorded requests carrying tag.
func (m *Model) Calls(tag string) []ethan.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ethan.Request
	for _, c := range m.calls {
		if c.Tag == tag {
			out = append(out, c)
		}
	}
	return out
}

// DefaultUsage is attached to responses built by Text and JSON.
var DefaultUsage = ethan.Usage{InputTokens: 10, OutputTokens: 10}

// Text builds a free-text response.
func Text(s string) *ethan.Response {
	return &ethan.Response{Parts: []ethan.Part{{Text: s}}, Usage: DefaultUsage}
}

// JSON builds a structured-output response holding v.
func JSON(v any) *ethan.Response {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return &ethan.Response{Output: b, Usage: DefaultUsage}
}

// ReplyText always answers with s.
func ReplyText(s string) Handler {
	return func(ethan.Request) (*ethan.Response, error) { return Text(s), nil }
}

// ReplyJSON always answers with v as structured output.
func ReplyJSON(v any) Handler {
	return func(ethan.Request) (*ethan.Response, error) { return JSON(v), nil }
}

// Prompt returns the text of the first user message, which is where the
// engine puts the task description.
func Prompt(req ethan.Request) string {
	for _, m := range req.Messages {
		if m.Role == ethan.RoleUser {
			for _, p := range m.Parts {
				if p.Text != "" {
					return p.Text
				}
			}
		}
	}
	return ""
}
