package ethantest_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
)

// memoryModel is a faithful adapter of an in-memory provider: TestModel
// must accept it. It also shows what a Fake has to record.
type memoryModel struct {
	mu    sync.Mutex
	turns []ethantest.Turn
	seen  []ethantest.Seen
}

func (m *memoryModel) Generate(ctx context.Context, req ethan.Request) (*ethan.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := ethantest.Seen{System: req.System, Structured: req.Output != nil}
	for _, msg := range req.Messages {
		for _, p := range msg.Parts {
			switch {
			case p.ToolCall != nil:
				s.ToolCalls = append(s.ToolCalls, *p.ToolCall)
			case p.ToolResult != nil:
				s.ToolResults = append(s.ToolResults, *p.ToolResult)
			case msg.Role == ethan.RoleUser:
				s.UserTexts = append(s.UserTexts, p.Text)
			}
		}
	}
	for _, tool := range req.Tools {
		s.Tools = append(s.Tools, tool.Name)
	}
	m.mu.Lock()
	m.seen = append(m.seen, s)
	turn := m.turns[0]
	m.turns = m.turns[1:]
	m.mu.Unlock()

	if turn.Fail {
		return nil, errors.New("500 internal server error")
	}
	resp := &ethan.Response{Usage: turn.Usage}
	if turn.Truncated {
		return resp, errors.New("output token limit reached")
	}
	if turn.Text != "" {
		resp.Parts = append(resp.Parts, ethan.Part{Text: turn.Text})
		if req.Output != nil {
			resp.Output = []byte(turn.Text)
		}
	}
	for _, c := range turn.ToolCalls {
		resp.Parts = append(resp.Parts, ethan.Part{ToolCall: &c})
	}
	return resp, nil
}

func TestTestModelAcceptsAFaithfulAdapter(t *testing.T) {
	ethantest.TestModel(t, func(t *testing.T, turns []ethantest.Turn) (ethan.Model, func() []ethantest.Seen) {
		m := &memoryModel{turns: turns}
		return m, func() []ethantest.Seen {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.seen
		}
	})
}
