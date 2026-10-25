// Package openai implements ethan.Model with the OpenAI Responses API.
//
// Servers that implement the Responses API, such as Ollama, work too:
//
//	openai.New(openai.WithBaseURL("http://localhost:11434/v1/"), openai.WithModel("gpt-oss:20b"))
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"

	"github.com/skrtk98/ethan"
)

// DefaultModel is the model used when none is configured.
const DefaultModel = "gpt-5.5"

// Model is an ethan.Model backed by the Responses API. It keeps no state on
// the server: each request carries the whole conversation, and reasoning is
// passed back in its encrypted form. It is safe for concurrent use.
type Model struct {
	client        sdk.Client
	clientOpts    []option.RequestOption
	model         string
	effort        string
	webSearch     bool
	webSearchUses int64
}

// Option configures a Model.
type Option func(*Model)

// WithModel selects the model id. Default DefaultModel.
func WithModel(id string) Option { return func(m *Model) { m.model = id } }

// WithEffort sets reasoning.effort, e.g. "low", "medium" or "high"; the
// values a model accepts vary.
func WithEffort(effort string) Option { return func(m *Model) { m.effort = effort } }

// WithWebSearch enables the server-side web search tool, allowing at most
// maxUses calls to it per request (0 means no limit).
func WithWebSearch(maxUses int) Option {
	return func(m *Model) { m.webSearch, m.webSearchUses = true, int64(maxUses) }
}

// WithBaseURL sends requests to another server implementing the Responses
// API, such as Ollama at "http://localhost:11434/v1/".
func WithBaseURL(url string) Option {
	return func(m *Model) { m.clientOpts = append(m.clientOpts, option.WithBaseURL(url)) }
}

// WithClientOptions configures the underlying SDK client, e.g. the API key
// or retry count. By default the client reads OPENAI_API_KEY.
func WithClientOptions(opts ...option.RequestOption) Option {
	return func(m *Model) { m.clientOpts = append(m.clientOpts, opts...) }
}

// New returns a Model.
func New(opts ...Option) *Model {
	m := &Model{model: DefaultModel}
	for _, o := range opts {
		o(m)
	}
	m.client = sdk.NewClient(m.clientOpts...)
	return m
}

// Generate implements ethan.Model. When a call fails after tokens were
// spent (a refusal, a truncated response), the returned Response carries
// their Usage. A Request whose MaxOutputTokens is zero sets no limit.
func (m *Model) Generate(ctx context.Context, req ethan.Request) (*ethan.Response, error) {
	params, err := m.params(req)
	if err != nil {
		return nil, err
	}
	out := &ethan.Response{}
	resp, err := m.client.Responses.New(ctx, params)
	if err != nil {
		return out, err
	}
	out.Usage = usage(resp.Usage)
	out.Parts = parts(resp.Output)

	if refusal := refusalOf(resp.Output); refusal != "" {
		return out, fmt.Errorf("openai: %w: %s", ethan.ErrRefused, refusal)
	}
	switch resp.Status {
	case responses.ResponseStatusCompleted:
	case responses.ResponseStatusIncomplete:
		switch reason := resp.IncompleteDetails.Reason; reason {
		case "content_filter":
			return out, fmt.Errorf("openai: %w: content filter", ethan.ErrRefused)
		case "max_output_tokens":
			return out, fmt.Errorf("openai: %w (max_output_tokens %d)", ethan.ErrTruncated, req.MaxOutputTokens)
		default:
			return out, fmt.Errorf("openai: response incomplete: %s", reason)
		}
	default:
		return out, fmt.Errorf("openai: response %s: %s", resp.Status, resp.Error.Message)
	}
	if req.Output != nil {
		out.Output = json.RawMessage(out.Text())
	}
	return out, nil
}

func (m *Model) params(req ethan.Request) (responses.ResponseNewParams, error) {
	p := responses.ResponseNewParams{
		Model: m.model,
		Store: param.NewOpt(false),
		// Without stored responses, reasoning must travel with the
		// conversation to carry over between the turns of a tool loop.
		Include: []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
	}
	if req.MaxOutputTokens > 0 {
		p.MaxOutputTokens = param.NewOpt(int64(req.MaxOutputTokens))
	}
	if req.System != "" {
		p.Instructions = param.NewOpt(req.System)
	}
	if m.effort != "" {
		p.Reasoning.Effort = sdk.ReasoningEffort(m.effort)
	}
	if req.Output != nil {
		var schema map[string]any
		if err := json.Unmarshal(req.Output.Schema, &schema); err != nil {
			return p, fmt.Errorf("openai: output schema %s: %w", req.Output.Name, err)
		}
		f := &responses.ResponseFormatTextJSONSchemaConfigParam{Name: req.Output.Name, Schema: schema, Strict: param.NewOpt(true)}
		if req.Output.Description != "" {
			f.Description = param.NewOpt(req.Output.Description)
		}
		p.Text.Format = responses.ResponseFormatTextConfigUnionParam{OfJSONSchema: f}
	}
	var input responses.ResponseInputParam
	for _, msg := range req.Messages {
		items, err := inputItems(msg)
		if err != nil {
			return p, err
		}
		input = append(input, items...)
	}
	p.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: input}
	for _, spec := range req.Tools {
		var schema map[string]any
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			return p, fmt.Errorf("openai: tool %s: input schema: %w", spec.Name, err)
		}
		// Not strict: strict mode requires schemas that tools need not have.
		t := &responses.FunctionToolParam{Name: spec.Name, Parameters: schema, Strict: param.NewOpt(false)}
		if spec.Description != "" {
			t.Description = param.NewOpt(spec.Description)
		}
		p.Tools = append(p.Tools, responses.ToolUnionParam{OfFunction: t})
	}
	// Structured-output calls are not offered search: it would let them
	// wander off.
	if m.webSearch && req.Output == nil {
		p.Tools = append(p.Tools, responses.ToolUnionParam{OfWebSearch: &responses.WebSearchToolParam{Type: responses.WebSearchToolTypeWebSearch}})
		if m.webSearchUses > 0 {
			p.MaxToolCalls = param.NewOpt(m.webSearchUses)
		}
	}
	return p, nil
}

// rawItem is an output item as the API returned it, kept for replay.
type rawItem json.RawMessage

func inputItems(msg ethan.Message) ([]responses.ResponseInputItemUnionParam, error) {
	var items []responses.ResponseInputItemUnionParam
	for _, part := range msg.Parts {
		switch {
		case part.Raw != nil:
			raw, ok := part.Raw.(rawItem)
			if !ok {
				return nil, fmt.Errorf("openai: cannot replay part produced by another provider (%T)", part.Raw)
			}
			items = append(items, param.Override[responses.ResponseInputItemUnionParam](json.RawMessage(raw)))
		case part.ToolCall != nil:
			items = append(items, responses.ResponseInputItemParamOfFunctionCall(string(part.ToolCall.Input), part.ToolCall.ID, part.ToolCall.Name))
		case part.ToolResult != nil:
			content := part.ToolResult.Content
			if part.ToolResult.IsError {
				content = "error: " + content
			}
			item := responses.ResponseInputItemParamOfFunctionCallOutput(content)
			item.OfFunctionCallOutput.CallID = param.NewOpt(part.ToolResult.CallID)
			items = append(items, item)
		case part.Text != "":
			role := responses.EasyInputMessageRoleUser
			if msg.Role == ethan.RoleAssistant {
				role = responses.EasyInputMessageRoleAssistant
			}
			items = append(items, responses.ResponseInputItemParamOfMessage(part.Text, role))
		}
	}
	return items, nil
}

// parts converts the output items, keeping every item for replay.
func parts(output []responses.ResponseOutputItemUnion) []ethan.Part {
	out := make([]ethan.Part, 0, len(output))
	for _, item := range output {
		p := ethan.Part{Raw: rawItem(item.RawJSON())}
		switch item.Type {
		case "message":
			var b strings.Builder
			for _, c := range item.AsMessage().Content {
				if c.Type == "output_text" {
					b.WriteString(c.Text)
				}
			}
			if b.Len() == 0 {
				continue // nothing to show or to send back
			}
			p.Text = b.String()
		case "function_call":
			c := item.AsFunctionCall()
			p.ToolCall = &ethan.ToolCall{ID: c.CallID, Name: c.Name, Input: json.RawMessage(c.Arguments)}
		}
		out = append(out, p)
	}
	return out
}

func refusalOf(output []responses.ResponseOutputItemUnion) string {
	for _, item := range output {
		if item.Type != "message" {
			continue
		}
		for _, c := range item.AsMessage().Content {
			if c.Type == "refusal" {
				return c.Refusal
			}
		}
	}
	return ""
}

func usage(u responses.ResponseUsage) ethan.Usage {
	return ethan.Usage{
		InputTokens:      int(u.InputTokens), // cached tokens included
		OutputTokens:     int(u.OutputTokens),
		CacheReadTokens:  int(u.InputTokensDetails.CachedTokens),
		CacheWriteTokens: int(u.InputTokensDetails.CacheWriteTokens),
	}
}
