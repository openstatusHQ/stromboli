// Package llm is a small provider-neutral layer over chat models with tool
// use, in the spirit of the Vercel AI SDK: models are addressed as
// "provider:model" (for example "anthropic:claude-opus-5-5" or
// "openai:gpt-5"), resolved through a Registry, and called with one
// message format whatever the provider. A conversation can move between
// models, so different tasks can use different models.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Model generates the next assistant message of a conversation.
type Model interface {
	// ID is the model's "provider:model" id.
	ID() string
	Generate(ctx context.Context, req Request) (*Response, error)
}

type Role string

const (
	User      Role = "user"
	Assistant Role = "assistant"
)

// Message is one turn. A user message carries text and tool results; an
// assistant message carries text and tool calls.
type Message struct {
	Role  Role
	Parts []Part

	// provider and native keep the provider's own form of an assistant
	// message (thinking blocks, signatures...) so it is replayed unchanged
	// to the same provider. Other providers rebuild it from Parts.
	provider string
	native   any
}

// Part is one content block: exactly one field is set.
type Part struct {
	Text       string
	ToolCall   *ToolCall
	ToolResult *ToolResult
}

type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// UserText returns a user message with one text part.
func UserText(text string) Message {
	return Message{Role: User, Parts: []Part{{Text: text}}}
}

// UserMessage returns a user message with the given parts.
func UserMessage(parts ...Part) Message { return Message{Role: User, Parts: parts} }

func TextPart(text string) Part { return Part{Text: text} }

func ToolResultPart(callID, content string, isError bool) Part {
	return Part{ToolResult: &ToolResult{CallID: callID, Content: content, IsError: isError}}
}

// ToolDef describes a function the model may call.
type ToolDef struct {
	Name        string
	Description string
	// Schema is the JSON schema of the input object.
	Schema map[string]any
	// Strict asks providers that support it to enforce the schema.
	Strict bool
}

// Request is one generation call.
type Request struct {
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
	// Effort is "low", "medium", "high", "xhigh" or "max"; empty means the
	// provider's default. Providers map it to their own setting.
	Effort string
}

type StopReason string

const (
	StopEnd       StopReason = "end"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
	StopOther     StopReason = "other"
)

type Usage struct {
	// InputTokens excludes cached tokens.
	InputTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	OutputTokens     int64
}

// TotalInput is every input token, cached or not.
func (u Usage) TotalInput() int64 { return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens }

type Response struct {
	Message Message
	Stop    StopReason
	// RawStop is the provider's own stop reason, for the audit trail.
	RawStop string
	Usage   Usage
	// Model is the model that answered, as the provider reports it.
	Model string
}

// Text joins the text parts of the response.
func (r *Response) Text() string {
	var parts []string
	for _, p := range r.Message.Parts {
		if p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// ToolCalls returns the tool calls of the response, in order.
func (r *Response) ToolCalls() []ToolCall {
	var out []ToolCall
	for _, p := range r.Message.Parts {
		if p.ToolCall != nil {
			out = append(out, *p.ToolCall)
		}
	}
	return out
}

// Provider returns the model with the given provider-specific name.
type Provider func(model string) (Model, error)

// DefaultProvider is used for ids without a "provider:" prefix.
const DefaultProvider = "anthropic"

// Registry resolves "provider:model" ids.
type Registry struct {
	providers map[string]Provider
	// fallback resolves providers that are not registered (e.g. custom
	// OpenAI-compatible endpoints configured through the environment).
	fallback func(name string) (Provider, bool)
}

func NewRegistry() *Registry { return &Registry{providers: map[string]Provider{}} }

func (r *Registry) Register(name string, p Provider) { r.providers[name] = p }

// Providers lists the registered provider names.
func (r *Registry) Providers() []string {
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Model resolves an id such as "openai:gpt-5" or "ollama:llama3.1:8b". As in
// the AI SDK, an id without a prefix but with a slash ("openai/gpt-5") goes
// through the gateway; any other id without a prefix uses DefaultProvider,
// so "claude-opus-5-5" still works.
func (r *Registry) Model(id string) (Model, error) {
	name, model := DefaultProvider, id
	if i := strings.IndexByte(id, ':'); i >= 0 {
		name, model = id[:i], id[i+1:]
	} else if strings.Contains(id, "/") {
		name = GatewayName
	}
	if model == "" {
		return nil, fmt.Errorf("model %q: missing model name", id)
	}
	p, ok := r.providers[name]
	if !ok && r.fallback != nil {
		p, ok = r.fallback(name)
	}
	if !ok {
		return nil, fmt.Errorf("model %q: unknown provider %q (known: %s)", id, name, strings.Join(r.Providers(), ", "))
	}
	m, err := p(model)
	if err != nil {
		return nil, fmt.Errorf("model %q: %w", id, err)
	}
	return m, nil
}
