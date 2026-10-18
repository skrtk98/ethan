package anthropic_test

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

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/anthropic"
)

// fakeAPI answers /v1/messages with canned responses in order and records
// the request bodies and beta headers.
type fakeAPI struct {
	mu        sync.Mutex
	responses []string
	bodies    []map[string]any
	betas     []string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, body)
	f.betas = append(f.betas, r.Header.Get("anthropic-beta"))
	if len(f.responses) == 0 {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"no more responses"}}`, 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, f.responses[0])
	f.responses = f.responses[1:]
}

func message(stop, content string) string {
	return `{"id":"msg","type":"message","role":"assistant","model":"m","stop_reason":"` + stop +
		`","content":[` + content + `],"usage":{"input_tokens":5,"output_tokens":3}}`
}

func newModel(t *testing.T, f *fakeAPI, opts ...anthropic.Option) *anthropic.Model {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	opts = append(opts, anthropic.WithClientOptions(
		option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0)))
	return anthropic.New(opts...)
}

func TestStructuredOutput(t *testing.T) {
	f := &fakeAPI{responses: []string{message("end_turn", `{"type":"text","text":"{\"value\":0.7,\"reason\":\"ok\"}"}`)}}
	m := newModel(t, f, anthropic.WithWebSearch(3))

	resp, err := m.Generate(context.Background(), ethan.Request{
		System:    "judge",
		Messages:  []ethan.Message{ethan.UserText("rate this")},
		MaxTokens: 100,
		Output:    &ethan.OutputSchema{Name: "score", Schema: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Output) != `{"value":0.7,"reason":"ok"}` {
		t.Errorf("Output = %s", resp.Output)
	}
	body := f.bodies[0]
	if body["model"] != anthropic.DefaultModel || body["fallbacks"] != "default" {
		t.Errorf("model/fallbacks = %v/%v", body["model"], body["fallbacks"])
	}
	if !strings.Contains(f.betas[0], "server-side-fallback-2026-07-01") {
		t.Errorf("betas = %q", f.betas[0])
	}
	format := body["output_config"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"] == nil {
		t.Errorf("format = %v", format)
	}
	if _, ok := body["tools"]; ok {
		t.Error("web search must not be offered on structured-output calls")
	}
}

func TestToolLoopReplaysThinkingBlocks(t *testing.T) {
	f := &fakeAPI{responses: []string{
		message("tool_use", `{"type":"thinking","thinking":"","signature":"sig-1"},
			{"type":"tool_use","id":"call-1","name":"echo","input":{"x":1}}`),
		message("end_turn", `{"type":"text","text":"done"}`),
	}}
	m := newModel(t, f, anthropic.WithWebSearch(0))
	tools := []ethan.ToolSpec{{Name: "echo", Description: "echo", InputSchema: json.RawMessage(
		`{"type":"object","properties":{"x":{"type":"integer"}},"required":["x"],"additionalProperties":false}`)}}

	msgs := []ethan.Message{ethan.UserText("go")}
	first, err := m.Generate(context.Background(), ethan.Request{Messages: msgs, Tools: tools, MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	calls := first.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "echo" || string(calls[0].Input) != `{"x":1}` {
		t.Fatalf("calls = %+v", calls)
	}
	msgs = append(msgs,
		ethan.Message{Role: ethan.RoleAssistant, Parts: first.Parts},
		ethan.Message{Role: ethan.RoleUser, Parts: []ethan.Part{{ToolResult: &ethan.ToolResult{CallID: "call-1", Content: "1"}}}},
	)
	second, err := m.Generate(context.Background(), ethan.Request{Messages: msgs, Tools: tools, MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if second.Text() != "done" {
		t.Errorf("Text = %q", second.Text())
	}

	tl := f.bodies[0]["tools"].([]any)
	if len(tl) != 2 || tl[1].(map[string]any)["type"] != "web_search_20260209" {
		t.Errorf("tools = %v", tl)
	}
	if tl[0].(map[string]any)["input_schema"].(map[string]any)["additionalProperties"] != false {
		t.Errorf("extra schema fields were dropped: %v", tl[0])
	}
	replayed := f.bodies[1]["messages"].([]any)
	assistant := replayed[1].(map[string]any)["content"].([]any)
	if b := assistant[0].(map[string]any); b["type"] != "thinking" || b["signature"] != "sig-1" {
		t.Errorf("thinking block not replayed: %v", b)
	}
	result := replayed[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "call-1" {
		t.Errorf("tool result = %v", result)
	}
}

func TestPausedTurnIsResumed(t *testing.T) {
	f := &fakeAPI{responses: []string{
		message("pause_turn", `{"type":"text","text":"Searching."},
			{"type":"server_tool_use","id":"srv-1","name":"web_search","input":{"query":"q"}}`),
		message("end_turn", `{"type":"text","text":"answer"}`),
	}}
	m := newModel(t, f, anthropic.WithoutFallbacks())

	resp, err := m.Generate(context.Background(), ethan.Request{Messages: []ethan.Message{ethan.UserText("search")}, MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "answer" || resp.Usage.Total() != 16 {
		t.Errorf("text %q usage %+v", resp.Text(), resp.Usage)
	}
	if len(f.bodies) != 2 || len(f.bodies[1]["messages"].([]any)) != 2 {
		t.Errorf("paused turn was not resent: %d requests", len(f.bodies))
	}
	if _, ok := f.bodies[0]["fallbacks"]; ok {
		t.Error("fallbacks sent despite WithoutFallbacks")
	}
}

func TestRefusal(t *testing.T) {
	f := &fakeAPI{responses: []string{`{"id":"msg","type":"message","role":"assistant","model":"m","stop_reason":"refusal",
		"stop_details":{"type":"refusal","category":"cyber"},"content":[],"usage":{"input_tokens":5,"output_tokens":0}}`}}
	resp, err := newModel(t, f).Generate(context.Background(), ethan.Request{Messages: []ethan.Message{ethan.UserText("x")}, MaxTokens: 10})
	if !errors.Is(err, anthropic.ErrRefused) || !strings.Contains(err.Error(), "cyber") {
		t.Errorf("err = %v", err)
	}
	if resp == nil || resp.Usage.InputTokens != 5 {
		t.Errorf("usage of the refused call was lost: %+v", resp)
	}
}

func TestFailedContinuationKeepsUsage(t *testing.T) {
	// The paused turn is resumed, and the resumed call fails.
	f := &fakeAPI{responses: []string{
		message("pause_turn", `{"type":"server_tool_use","id":"srv-1","name":"web_search","input":{"query":"q"}}`),
	}}
	resp, err := newModel(t, f).Generate(context.Background(), ethan.Request{
		Messages: []ethan.Message{ethan.UserText("search")}, MaxTokens: 100,
	})
	if err == nil {
		t.Fatal("the failed continuation returned no error")
	}
	if resp == nil || resp.Usage.Total() != 8 {
		t.Errorf("usage of the first call was lost: %+v", resp)
	}
}

func TestPromptCaching(t *testing.T) {
	cached := `{"id":"msg","type":"message","role":"assistant","model":"m","stop_reason":"end_turn",` +
		`"content":[{"type":"text","text":"ok"}],` +
		`"usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":3}}`
	f := &fakeAPI{responses: []string{cached, cached}}
	req := ethan.Request{Messages: []ethan.Message{ethan.UserText("hi")}, MaxTokens: 10}

	resp, err := newModel(t, f).Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if cc, _ := f.bodies[0]["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("cache_control = %v", f.bodies[0]["cache_control"])
	}
	want := ethan.Usage{InputTokens: 125, OutputTokens: 3, CacheReadTokens: 100, CacheWriteTokens: 20}
	if resp.Usage != want {
		t.Errorf("Usage = %+v, want %+v", resp.Usage, want)
	}

	if _, err := newModel(t, f, anthropic.WithoutCache()).Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.bodies[1]["cache_control"]; ok {
		t.Error("cache_control sent with WithoutCache")
	}
}
