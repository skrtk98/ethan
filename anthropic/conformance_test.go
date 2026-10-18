package anthropic_test

import (
	"encoding/json"
	"testing"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/anthropic"
	"github.com/skrtk98/ethan/ethantest"
)

func TestConformance(t *testing.T) {
	ethantest.TestModel(t, func(t *testing.T, turns []ethantest.Turn) (ethan.Model, func() []ethantest.Seen) {
		f := &fakeAPI{}
		for _, turn := range turns {
			if !turn.Fail {
				f.responses = append(f.responses, messageOf(turn))
			}
		}
		return newModel(t, f, anthropic.WithoutFallbacks()), func() []ethantest.Seen {
			f.mu.Lock()
			defer f.mu.Unlock()
			seen := make([]ethantest.Seen, len(f.bodies))
			for i, b := range f.bodies {
				seen[i] = seenIn(b)
			}
			return seen
		}
	})
}

// messageOf encodes a turn as a Messages API response.
func messageOf(turn ethantest.Turn) string {
	var content []map[string]any
	if turn.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": turn.Text})
	}
	stop := "end_turn"
	for _, c := range turn.ToolCalls {
		content = append(content, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": c.Input})
		stop = "tool_use"
	}
	if turn.Truncated {
		stop = "max_tokens"
	}
	u := turn.Usage
	b, _ := json.Marshal(map[string]any{
		"id": "msg", "type": "message", "role": "assistant", "model": "m", "stop_reason": stop, "content": content,
		"usage": map[string]any{
			"input_tokens":                u.InputTokens - u.CacheReadTokens - u.CacheWriteTokens,
			"cache_read_input_tokens":     u.CacheReadTokens,
			"cache_creation_input_tokens": u.CacheWriteTokens,
			"output_tokens":               u.OutputTokens,
		},
	})
	return string(b)
}

// seenIn decodes a Messages API request body.
func seenIn(body map[string]any) ethantest.Seen {
	var s ethantest.Seen
	for _, b := range asList(body["system"]) {
		s.System += asMap(b)["text"].(string)
	}
	for _, m := range asList(body["messages"]) {
		msg := asMap(m)
		for _, c := range asList(msg["content"]) {
			block := asMap(c)
			switch block["type"] {
			case "text":
				if msg["role"] == "user" {
					s.UserTexts = append(s.UserTexts, block["text"].(string))
				}
			case "tool_use":
				input, _ := json.Marshal(block["input"])
				s.ToolCalls = append(s.ToolCalls, ethan.ToolCall{ID: block["id"].(string), Name: block["name"].(string), Input: input})
			case "tool_result":
				r := ethan.ToolResult{CallID: block["tool_use_id"].(string)}
				for _, rc := range asList(block["content"]) {
					r.Content += asMap(rc)["text"].(string)
				}
				if is, ok := block["is_error"].(bool); ok {
					r.IsError = is
				}
				s.ToolResults = append(s.ToolResults, r)
			}
		}
	}
	for _, tl := range asList(body["tools"]) {
		if tool := asMap(tl); tool["input_schema"] != nil {
			s.Tools = append(s.Tools, tool["name"].(string))
		}
	}
	s.Structured = asMap(body["output_config"])["format"] != nil
	return s
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
