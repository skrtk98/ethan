package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openai/openai-go/v3/option"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/ethantest"
	"github.com/skrtk98/ethan/openai"
)

// fakeAPI answers /responses with canned responses in order and records the
// request bodies.
type fakeAPI struct {
	mu        sync.Mutex
	responses []string
	bodies    []map[string]any
	paths     []string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, body)
	f.paths = append(f.paths, r.URL.Path)
	if len(f.responses) == 0 {
		http.Error(w, `{"error":{"message":"no more responses","type":"server_error"}}`, 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, f.responses[0])
	f.responses = f.responses[1:]
}

func newModel(t *testing.T, f *fakeAPI, opts ...openai.Option) *openai.Model {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	opts = append([]openai.Option{openai.WithBaseURL(srv.URL + "/v1/")}, opts...)
	opts = append(opts, openai.WithClientOptions(option.WithAPIKey("test"), option.WithMaxRetries(0)))
	return openai.New(opts...)
}

// response builds a Responses API response from output items.
func response(status, incomplete string, usage ethan.Usage, output ...map[string]any) string {
	r := map[string]any{
		"id": "resp_1", "object": "response", "created_at": 0, "status": status, "model": "m",
		"output": output, "error": nil, "incomplete_details": nil,
		"usage": map[string]any{
			"input_tokens":          usage.InputTokens,
			"input_tokens_details":  map[string]any{"cached_tokens": usage.CacheReadTokens, "cache_write_tokens": usage.CacheWriteTokens},
			"output_tokens":         usage.OutputTokens,
			"output_tokens_details": map[string]any{"reasoning_tokens": 0},
			"total_tokens":          usage.Total(),
		},
	}
	if incomplete != "" {
		r["incomplete_details"] = map[string]any{"reason": incomplete}
	}
	b, _ := json.Marshal(r)
	return string(b)
}

func textItem(text string) map[string]any {
	return map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func callItem(c ethan.ToolCall) map[string]any {
	return map[string]any{"type": "function_call", "id": "fc_" + c.ID, "call_id": c.ID, "name": c.Name,
		"arguments": string(c.Input), "status": "completed"}
}

func TestConformance(t *testing.T) {
	ethantest.TestModel(t, func(t *testing.T, turns []ethantest.Turn) (ethan.Model, func() []ethantest.Seen) {
		f := &fakeAPI{}
		for _, turn := range turns {
			if turn.Fail {
				continue
			}
			var items []map[string]any
			if turn.Text != "" {
				items = append(items, textItem(turn.Text))
			}
			for _, c := range turn.ToolCalls {
				items = append(items, callItem(c))
			}
			status, incomplete := "completed", ""
			if turn.Truncated {
				status, incomplete = "incomplete", "max_output_tokens"
			}
			f.responses = append(f.responses, response(status, incomplete, turn.Usage, items...))
		}
		return newModel(t, f), func() []ethantest.Seen {
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

// seenIn decodes a Responses API request body.
func seenIn(body map[string]any) ethantest.Seen {
	var s ethantest.Seen
	s.System, _ = body["instructions"].(string)
	for _, it := range asList(body["input"]) {
		item := asMap(it)
		switch {
		case item["type"] == "function_call":
			s.ToolCalls = append(s.ToolCalls, ethan.ToolCall{
				ID: item["call_id"].(string), Name: item["name"].(string), Input: json.RawMessage(item["arguments"].(string)),
			})
		case item["type"] == "function_call_output":
			s.ToolResults = append(s.ToolResults, ethan.ToolResult{CallID: item["call_id"].(string), Content: item["output"].(string)})
		case item["role"] == "user":
			if text, ok := item["content"].(string); ok {
				s.UserTexts = append(s.UserTexts, text)
			}
		}
	}
	for _, tl := range asList(body["tools"]) {
		if tool := asMap(tl); tool["type"] == "function" {
			s.Tools = append(s.Tools, tool["name"].(string))
		}
	}
	s.Structured = asMap(asMap(body["text"])["format"])["type"] == "json_schema"
	return s
}

func TestRequestIsStatelessAndKeepsReasoning(t *testing.T) {
	reasoning := map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}, "encrypted_content": "secret"}
	call := ethan.ToolCall{ID: "call-1", Name: "lookup", Input: json.RawMessage(`{}`)}
	f := &fakeAPI{responses: []string{
		response("completed", "", ethantest.DefaultUsage, reasoning, callItem(call)),
		response("completed", "", ethantest.DefaultUsage, textItem("done")),
	}}
	m := newModel(t, f, openai.WithEffort("low"))
	req := ethan.Request{Messages: []ethan.Message{ethan.UserText("go")}, MaxTokens: 100,
		Tools: []ethan.ToolSpec{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}}}

	first, err := m.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Messages = append(req.Messages,
		ethan.Message{Role: ethan.RoleAssistant, Parts: first.Parts},
		ethan.Message{Role: ethan.RoleUser, Parts: []ethan.Part{{ToolResult: &ethan.ToolResult{CallID: "call-1", Content: "no such thing", IsError: true}}}},
	)
	if _, err := m.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	body := f.bodies[0]
	if body["store"] != false || body["model"] != openai.DefaultModel || f.paths[0] != "/v1/responses" {
		t.Errorf("store %v model %v path %s", body["store"], body["model"], f.paths[0])
	}
	if inc := asList(body["include"]); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v", body["include"])
	}
	if asMap(body["reasoning"])["effort"] != "low" || body["max_output_tokens"] != 100.0 {
		t.Errorf("reasoning %v max_output_tokens %v", body["reasoning"], body["max_output_tokens"])
	}
	if tool := asMap(asList(body["tools"])[0]); tool["strict"] != false {
		t.Errorf("tool = %v", tool)
	}
	input := asList(f.bodies[1]["input"])
	if r := asMap(input[1]); r["type"] != "reasoning" || r["encrypted_content"] != "secret" {
		t.Errorf("reasoning not replayed: %v", r)
	}
	if out := asMap(input[3]); out["output"] != "error: no such thing" {
		t.Errorf("failed tool result = %v", out)
	}
}

func TestStructuredOutputIsStrictAndNotSearched(t *testing.T) {
	f := &fakeAPI{responses: []string{response("completed", "", ethantest.DefaultUsage, textItem(`{}`))}}
	_, err := newModel(t, f, openai.WithWebSearch(3)).Generate(context.Background(), ethan.Request{
		Messages: []ethan.Message{ethan.UserText("rate")}, MaxTokens: 100,
		Output: &ethan.OutputSchema{Name: "score", Schema: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	format := asMap(asMap(f.bodies[0]["text"])["format"])
	if format["name"] != "score" || format["strict"] != true {
		t.Errorf("format = %v", format)
	}
	if _, ok := f.bodies[0]["tools"]; ok {
		t.Error("web search must not be offered on structured-output calls")
	}
}

func TestWebSearch(t *testing.T) {
	f := &fakeAPI{responses: []string{response("completed", "", ethantest.DefaultUsage,
		textItem("Searching."),
		map[string]any{"type": "web_search_call", "id": "ws_1", "status": "completed", "action": map[string]any{"type": "search", "query": "q"}},
		textItem("answer"),
	)}}
	resp, err := newModel(t, f, openai.WithWebSearch(3)).Generate(context.Background(), ethan.Request{
		Messages: []ethan.Message{ethan.UserText("search")}, MaxTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "answer" {
		t.Errorf("Text = %q", resp.Text())
	}
	tools := asList(f.bodies[0]["tools"])
	if len(tools) != 1 || asMap(tools[0])["type"] != "web_search" || f.bodies[0]["max_tool_calls"] != 3.0 {
		t.Errorf("tools %v max_tool_calls %v", tools, f.bodies[0]["max_tool_calls"])
	}
}

func TestRefusal(t *testing.T) {
	refusal := map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "refusal", "refusal": "I can't help with that."}}}
	f := &fakeAPI{responses: []string{response("completed", "", ethantest.DefaultUsage, refusal)}}
	resp, err := newModel(t, f).Generate(context.Background(), ethan.Request{Messages: []ethan.Message{ethan.UserText("x")}, MaxTokens: 10})
	if !errors.Is(err, openai.ErrRefused) || !strings.Contains(err.Error(), "can't help") {
		t.Errorf("err = %v", err)
	}
	if resp == nil || resp.Usage != ethantest.DefaultUsage {
		t.Errorf("usage of the refused call was lost: %+v", resp)
	}
}

func TestReplayingAnotherProvidersPartFails(t *testing.T) {
	f := &fakeAPI{}
	_, err := newModel(t, f).Generate(context.Background(), ethan.Request{
		Messages:  []ethan.Message{{Role: ethan.RoleAssistant, Parts: []ethan.Part{{Raw: struct{}{}}}}},
		MaxTokens: 10,
	})
	if err == nil || len(f.bodies) != 0 {
		t.Errorf("err = %v after %d requests", err, len(f.bodies))
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
