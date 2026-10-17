// Package anthropic implements ethan.Model with the Claude API.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/skrtk98/ethan"
)

// DefaultModel is the model used when none is configured.
const DefaultModel = "claude-sonnet-5-5"

// ErrRefused is returned when the model declines a request.
var ErrRefused = errors.New("anthropic: request refused")

// maxContinuations bounds how often a paused server-side tool loop is resumed.
const maxContinuations = 5

// Model is an ethan.Model backed by the Claude Messages API.
// It is safe for concurrent use.
type Model struct {
	client        sdk.Client
	model         string
	effort        string
	webSearch     bool
	webSearchUses int64
	fallbacks     bool
}

// Option configures a Model.
type Option func(*Model)

// WithModel selects the Claude model id. Default DefaultModel.
func WithModel(id string) Option { return func(m *Model) { m.model = id } }

// WithEffort sets output_config.effort: "low", "medium", "high", "xhigh" or "max".
func WithEffort(effort string) Option { return func(m *Model) { m.effort = effort } }

// WithWebSearch enables the server-side web search tool, allowing at most
// maxUses searches per request (0 means the API default).
func WithWebSearch(maxUses int) Option {
	return func(m *Model) { m.webSearch, m.webSearchUses = true, int64(maxUses) }
}

// WithoutFallbacks disables server-side refusal fallbacks. They are only
// available on the Claude API; disable them when targeting other platforms.
func WithoutFallbacks() Option { return func(m *Model) { m.fallbacks = false } }

// WithClientOptions configures the underlying SDK client, e.g. the API key,
// base URL or retry count. By default the client reads ANTHROPIC_API_KEY.
func WithClientOptions(opts ...option.RequestOption) Option {
	return func(m *Model) { m.client = sdk.NewClient(opts...) }
}

// New returns a Model.
func New(opts ...Option) *Model {
	m := &Model{model: DefaultModel, fallbacks: true}
	for _, o := range opts {
		o(m)
	}
	if m.client.Options == nil {
		m.client = sdk.NewClient()
	}
	return m
}

// Generate implements ethan.Model.
func (m *Model) Generate(ctx context.Context, req ethan.Request) (*ethan.Response, error) {
	params, err := m.params(req)
	if err != nil {
		return nil, err
	}
	out := &ethan.Response{}
	for range maxContinuations {
		msg, err := m.client.Beta.Messages.New(ctx, params)
		if err != nil {
			return nil, err
		}
		out.Usage = out.Usage.Add(usage(msg.Usage))
		out.Parts = append(out.Parts, parts(msg.Content)...)

		switch msg.StopReason {
		case sdk.BetaStopReasonPauseTurn:
			// Resend the paused turn as is; the server resumes its tool loop.
			params.Messages = append(params.Messages, msg.ToParam())
			continue
		case sdk.BetaStopReasonRefusal:
			return nil, fmt.Errorf("%w: %s", ErrRefused, msg.StopDetails.Category)
		case sdk.BetaStopReasonMaxTokens:
			if req.Output != nil {
				return nil, fmt.Errorf("anthropic: structured output truncated at max_tokens (%d)", req.MaxTokens)
			}
		}
		if req.Output != nil {
			out.Output = json.RawMessage(out.Text())
		}
		return out, nil
	}
	return nil, fmt.Errorf("anthropic: turn still paused after %d continuations", maxContinuations)
}

func (m *Model) params(req ethan.Request) (sdk.BetaMessageNewParams, error) {
	p := sdk.BetaMessageNewParams{
		Model:     m.model,
		MaxTokens: int64(req.MaxTokens),
	}
	if req.System != "" {
		p.System = []sdk.BetaTextBlockParam{{Text: req.System}}
	}
	if m.fallbacks {
		p.Betas = append(p.Betas, sdk.AnthropicBetaServerSideFallback2026_07_01)
		p.Fallbacks = sdk.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}
	if m.effort != "" {
		p.OutputConfig.Effort = sdk.BetaOutputConfigEffort(m.effort)
	}
	if req.Output != nil {
		p.OutputConfig.Format = sdk.BetaJSONOutputFormatParam{Schema: req.Output.Schema}
	}
	for _, msg := range req.Messages {
		mp, err := messageParam(msg)
		if err != nil {
			return p, err
		}
		p.Messages = append(p.Messages, mp)
	}
	for _, spec := range req.Tools {
		t, err := toolParam(spec)
		if err != nil {
			return p, err
		}
		p.Tools = append(p.Tools, t)
	}
	// Web search would let structured-output calls wander off; only tool-using
	// calls (agents) get it.
	if m.webSearch && req.Output == nil {
		ws := &sdk.BetaWebSearchTool20260209Param{}
		if m.webSearchUses > 0 {
			ws.MaxUses = param.NewOpt(m.webSearchUses)
		}
		p.Tools = append(p.Tools, sdk.BetaToolUnionParam{OfWebSearchTool20260209: ws})
	}
	return p, nil
}

func messageParam(msg ethan.Message) (sdk.BetaMessageParam, error) {
	var blocks []sdk.BetaContentBlockParamUnion
	for _, part := range msg.Parts {
		switch {
		case part.Raw != nil:
			b, ok := part.Raw.(sdk.BetaContentBlockParamUnion)
			if !ok {
				return sdk.BetaMessageParam{}, fmt.Errorf("anthropic: cannot replay part produced by another provider (%T)", part.Raw)
			}
			blocks = append(blocks, b)
		case part.ToolCall != nil:
			blocks = append(blocks, sdk.NewBetaToolUseBlock(part.ToolCall.ID, part.ToolCall.Input, part.ToolCall.Name))
		case part.ToolResult != nil:
			blocks = append(blocks, sdk.NewBetaToolResultBlock(part.ToolResult.CallID, part.ToolResult.Content, part.ToolResult.IsError))
		case part.Text != "":
			blocks = append(blocks, sdk.NewBetaTextBlock(part.Text))
		}
	}
	role := sdk.BetaMessageParamRoleUser
	if msg.Role == ethan.RoleAssistant {
		role = sdk.BetaMessageParamRoleAssistant
	}
	return sdk.BetaMessageParam{Role: role, Content: blocks}, nil
}

func toolParam(spec ethan.ToolSpec) (sdk.BetaToolUnionParam, error) {
	var schema map[string]any
	if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
		return sdk.BetaToolUnionParam{}, fmt.Errorf("anthropic: tool %s: input schema: %w", spec.Name, err)
	}
	in := sdk.BetaToolInputSchemaParam{Properties: schema["properties"], ExtraFields: map[string]any{}}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				in.Required = append(in.Required, s)
			}
		}
	}
	for k, v := range schema {
		if k != "type" && k != "properties" && k != "required" {
			in.ExtraFields[k] = v
		}
	}
	t := &sdk.BetaToolParam{Name: spec.Name, InputSchema: in}
	if spec.Description != "" {
		t.Description = param.NewOpt(spec.Description)
	}
	return sdk.BetaToolUnionParam{OfTool: t}, nil
}

// parts converts response content, keeping every block for replay.
func parts(content []sdk.BetaContentBlockUnion) []ethan.Part {
	out := make([]ethan.Part, 0, len(content))
	for _, block := range content {
		p := ethan.Part{Raw: block.ToParam()}
		switch b := block.AsAny().(type) {
		case sdk.BetaTextBlock:
			p.Text = b.Text
		case sdk.BetaToolUseBlock:
			p.ToolCall = &ethan.ToolCall{ID: b.ID, Name: b.Name, Input: json.RawMessage(b.JSON.Input.Raw())}
		}
		out = append(out, p)
	}
	return out
}

func usage(u sdk.BetaUsage) ethan.Usage {
	return ethan.Usage{
		InputTokens:  int(u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens),
		OutputTokens: int(u.OutputTokens),
	}
}
