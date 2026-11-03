package llm

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/anthropics/anthropic-sdk-go/v2/shared/constant"
)

// AnthropicMessages is the subset of the Anthropic client the adapter uses.
type AnthropicMessages interface {
	New(ctx context.Context, params anthropic.BetaMessageNewParams, opts ...option.RequestOption) (*anthropic.BetaMessage, error)
}

// Anthropic returns a provider backed by the Anthropic Messages API.
func Anthropic(msgs AnthropicMessages) Provider {
	return func(model string) (Model, error) {
		if msgs == nil {
			return nil, errors.New("ANTHROPIC_API_KEY is not set")
		}
		return newAnthropicModel(msgs, "anthropic", model, true), nil
	}
}

// NewAnthropicClient returns the Messages API for an API key; empty means none.
func NewAnthropicClient(apiKey string) AnthropicMessages {
	if apiKey == "" {
		return nil
	}
	c := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &c.Beta.Messages
}

// newAnthropicModel returns a model served through the Messages API by the
// named provider (Anthropic itself, or a gateway that speaks the same API).
// Server-side fallbacks are an Anthropic API feature, off for gateways.
func newAnthropicModel(msgs AnthropicMessages, provider, model string, fallbacks bool) *anthropicModel {
	return &anthropicModel{msgs: msgs, provider: provider, model: model, fallbacks: fallbacks}
}

type anthropicModel struct {
	msgs      AnthropicMessages
	provider  string
	model     string
	fallbacks bool
}

func (m *anthropicModel) ID() string { return m.provider + ":" + m.model }

// fallbackBeta enables server-side refusal fallbacks: a declined request is
// re-served by a fallback model inside the same call.
const fallbackBeta anthropic.AnthropicBeta = "server-side-fallback-2026-07-01"

// fallbackModels accept the "default" server-side fallback.
var fallbackModels = []string{"claude-fable-5-1", "claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5"}

func (m *anthropicModel) Generate(ctx context.Context, req Request) (*Response, error) {
	effort := req.Effort
	if effort == "" {
		effort = "high"
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(m.model),
		MaxTokens: int64(req.MaxTokens),
		Tools:     anthropicTools(req.Tools),
		Messages:  make([]anthropic.BetaMessageParam, 0, len(req.Messages)),
		// Automatic caching: the breakpoint moves to the last block each turn,
		// so every turn re-reads the previous turns from cache.
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(effort)},
	}
	if req.System != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: req.System}}
	}
	if m.fallbacks && slices.Contains(fallbackModels, m.model) {
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		params.Betas = []anthropic.AnthropicBeta{fallbackBeta}
	}
	for _, msg := range req.Messages {
		params.Messages = append(params.Messages, anthropicMessage(msg, m.provider))
	}

	resp, err := m.msgs.New(ctx, params)
	if err != nil {
		return nil, err
	}
	out := &Response{
		Message: Message{Role: Assistant, provider: m.provider, native: resp.ToParam()},
		RawStop: string(resp.StopReason),
		Model:   string(resp.Model),
		Usage: Usage{
			InputTokens:      resp.Usage.InputTokens,
			CacheReadTokens:  resp.Usage.CacheReadInputTokens,
			CacheWriteTokens: resp.Usage.CacheCreationInputTokens,
			OutputTokens:     resp.Usage.OutputTokens,
		},
	}
	switch resp.StopReason {
	case anthropic.BetaStopReasonEndTurn, anthropic.BetaStopReasonStopSequence:
		out.Stop = StopEnd
	case anthropic.BetaStopReasonToolUse:
		out.Stop = StopToolUse
	case anthropic.BetaStopReasonMaxTokens, anthropic.BetaStopReasonModelContextWindowExceeded:
		out.Stop = StopMaxTokens
	case anthropic.BetaStopReasonRefusal:
		out.Stop = StopRefusal
	default:
		out.Stop = StopOther
	}
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.BetaTextBlock:
			if b.Text != "" {
				out.Message.Parts = append(out.Message.Parts, Part{Text: b.Text})
			}
		case anthropic.BetaToolUseBlock:
			out.Message.Parts = append(out.Message.Parts, Part{ToolCall: &ToolCall{
				ID: b.ID, Name: b.Name, Input: json.RawMessage(b.JSON.Input.Raw()),
			}})
		}
	}
	return out, nil
}

func anthropicMessage(msg Message, provider string) anthropic.BetaMessageParam {
	if msg.provider == provider {
		// Replay unchanged: thinking blocks must come back as they were.
		if p, ok := msg.native.(anthropic.BetaMessageParam); ok {
			return p
		}
	}
	blocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		switch {
		case p.ToolCall != nil:
			input := p.ToolCall.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			blocks = append(blocks, anthropic.NewBetaToolUseBlock(p.ToolCall.ID, input, p.ToolCall.Name))
		case p.ToolResult != nil:
			blocks = append(blocks, anthropic.NewBetaToolResultBlock(p.ToolResult.CallID, p.ToolResult.Content, p.ToolResult.IsError))
		case p.Text != "":
			blocks = append(blocks, anthropic.NewBetaTextBlock(p.Text))
		}
	}
	if msg.Role == Assistant {
		return anthropic.BetaMessageParam{OfAssistant: &anthropic.BetaAssistantMessageParam{Content: blocks}}
	}
	return anthropic.NewBetaUserMessage(blocks...)
}

func anthropicTools(defs []ToolDef) []anthropic.BetaToolUnionParam {
	out := make([]anthropic.BetaToolUnionParam, 0, len(defs))
	for _, t := range defs {
		schema := anthropic.BetaToolInputSchemaParam{ExtraFields: map[string]any{}}
		for k, v := range t.Schema {
			switch k {
			case "type":
			case "properties":
				schema.Properties = v
			case "required":
				schema.Required, _ = v.([]string)
			default:
				schema.ExtraFields[k] = v
			}
		}
		p := anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: schema,
		}
		if t.Strict {
			p.Strict = anthropic.Bool(true)
		}
		out = append(out, anthropic.BetaToolUnionParam{OfTool: &p})
	}
	return out
}
