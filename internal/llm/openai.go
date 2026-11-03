package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIConfig configures an OpenAI-compatible Chat Completions endpoint.
type OpenAIConfig struct {
	// Name is the provider name used in model ids ("openai", "ollama"...).
	Name    string
	BaseURL string // e.g. https://api.openai.com/v1
	APIKey  string // empty for endpoints without auth (Ollama)
	// RequireKey makes resolving a model fail when APIKey is empty.
	RequireKey bool
	// KeyEnv names the API key variable, for error messages.
	KeyEnv string
	// LegacyMaxTokens sends max_tokens instead of max_completion_tokens.
	LegacyMaxTokens bool
	// Effort maps the effort scale to reasoning_effort; nil uses OpenAI's
	// low/medium/high. Empty results are not sent.
	Effort func(string) string
	Client *http.Client
}

// OpenAICompatible returns a provider for an OpenAI-compatible endpoint.
func OpenAICompatible(cfg OpenAIConfig) Provider {
	return func(model string) (Model, error) {
		if cfg.RequireKey && cfg.APIKey == "" {
			return nil, fmt.Errorf("%s is not set", cfg.KeyEnv)
		}
		if cfg.BaseURL == "" {
			return nil, fmt.Errorf("no base URL configured for provider %q", cfg.Name)
		}
		c := cfg
		if c.Client == nil {
			c.Client = &http.Client{Timeout: 10 * time.Minute}
		}
		c.BaseURL = strings.TrimRight(c.BaseURL, "/")
		return &openaiModel{cfg: c, model: model}, nil
	}
}

type openaiModel struct {
	cfg   OpenAIConfig
	model string
}

func (m *openaiModel) ID() string { return m.cfg.Name + ":" + m.model }

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	Refusal    string       `json:"refusal,omitempty"`
	// ReasoningContent is the chain of thought some providers (DeepSeek)
	// return and require back in later requests of a tool loop.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// ReasoningDetails is the structured reasoning (with signatures and
	// encrypted content) Vercel AI Gateway and OpenRouter return; sending it
	// back lets the model resume its reasoning after tool results.
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
}

// openaiNative is what an assistant message keeps for its own provider.
type openaiNative struct {
	reasoning string
	details   json.RawMessage
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model               string      `json:"model"`
	Messages            []oaMessage `json:"messages"`
	Tools               []oaTool    `json:"tools,omitempty"`
	MaxTokens           int         `json:"max_tokens,omitempty"`
	MaxCompletionTokens int         `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     string      `json:"reasoning_effort,omitempty"`
}

type oaResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      oaMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func strPtr(s string) *string { return &s }

func (m *openaiModel) Generate(ctx context.Context, req Request) (*Response, error) {
	effort := openaiEffort
	if m.cfg.Effort != nil {
		effort = m.cfg.Effort
	}
	body := oaRequest{Model: m.model, ReasoningEffort: effort(req.Effort)}
	if m.cfg.LegacyMaxTokens {
		body.MaxTokens = req.MaxTokens
	} else {
		body.MaxCompletionTokens = req.MaxTokens
	}
	if req.System != "" {
		body.Messages = append(body.Messages, oaMessage{Role: "system", Content: strPtr(req.System)})
	}
	for _, msg := range req.Messages {
		body.Messages = append(body.Messages, openaiMessages(msg, m.cfg.Name)...)
	}
	for _, t := range req.Tools {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name, ot.Function.Description = t.Name, t.Description
		ot.Function.Parameters = t.Schema
		// strict is not sent: OpenAI's strict mode needs every property
		// required, which optional tool arguments do not satisfy.
		body.Tools = append(body.Tools, ot)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	var resp oaResponse
	if err := m.post(ctx, raw, &resp); err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("response has no choices")
	}
	choice := resp.Choices[0]
	cached := resp.Usage.PromptTokensDetails.CachedTokens
	out := &Response{
		Message: Message{Role: Assistant, provider: m.cfg.Name, native: openaiNative{reasoning: choice.Message.ReasoningContent, details: choice.Message.ReasoningDetails}},
		RawStop: choice.FinishReason,
		Model:   resp.Model,
		Usage: Usage{
			InputTokens:     resp.Usage.PromptTokens - cached,
			CacheReadTokens: cached,
			OutputTokens:    resp.Usage.CompletionTokens,
		},
	}
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		out.Message.Parts = append(out.Message.Parts, Part{Text: *choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		out.Message.Parts = append(out.Message.Parts, Part{ToolCall: &ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Input: json.RawMessage(args),
		}})
	}
	switch {
	case choice.Message.Refusal != "" || choice.FinishReason == "content_filter":
		out.Stop = StopRefusal
	case choice.FinishReason == "length":
		out.Stop = StopMaxTokens
	case len(choice.Message.ToolCalls) > 0:
		out.Stop = StopToolUse
	case choice.FinishReason == "stop":
		out.Stop = StopEnd
	default:
		out.Stop = StopOther
	}
	return out, nil
}

// post sends the request, retrying rate limits and server errors.
func (m *openaiModel) post(ctx context.Context, body []byte, out any) error {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if m.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)
		}
		resp, err := m.cfg.Client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			lastErr = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("%s: %s: %s", m.cfg.Name, resp.Status, truncate(data, 500))
			continue
		}
		if resp.StatusCode >= 300 {
			return fmt.Errorf("%s: %s: %s", m.cfg.Name, resp.Status, truncate(data, 500))
		}
		return json.Unmarshal(data, out)
	}
	return lastErr
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}

// openaiEffort maps the effort scale to reasoning_effort. Empty stays empty:
// models without reasoning reject the field.
func openaiEffort(e string) string {
	switch e {
	case "":
		return ""
	case "xhigh", "max":
		return "high"
	default:
		return e
	}
}

// openaiMessages converts one neutral message for the named provider. Tool
// results become separate "tool" messages, which must directly follow the
// assistant tool calls. Reasoning goes back only to the provider that wrote it.
func openaiMessages(msg Message, provider string) []oaMessage {
	if msg.Role == Assistant {
		out := oaMessage{Role: "assistant"}
		if n, ok := msg.native.(openaiNative); ok && msg.provider == provider {
			out.ReasoningContent, out.ReasoningDetails = n.reasoning, n.details
		}
		var texts []string
		for _, p := range msg.Parts {
			switch {
			case p.ToolCall != nil:
				var tc oaToolCall
				tc.ID, tc.Type = p.ToolCall.ID, "function"
				tc.Function.Name = p.ToolCall.Name
				tc.Function.Arguments = string(p.ToolCall.Input)
				if tc.Function.Arguments == "" {
					tc.Function.Arguments = "{}"
				}
				out.ToolCalls = append(out.ToolCalls, tc)
			case p.Text != "":
				texts = append(texts, p.Text)
			}
		}
		if len(texts) > 0 {
			out.Content = strPtr(strings.Join(texts, "\n"))
		}
		return []oaMessage{out}
	}
	var out []oaMessage
	var texts []string
	for _, p := range msg.Parts {
		switch {
		case p.ToolResult != nil:
			content := p.ToolResult.Content
			if p.ToolResult.IsError && !strings.HasPrefix(content, "error") {
				content = "error: " + content
			}
			out = append(out, oaMessage{Role: "tool", ToolCallID: p.ToolResult.CallID, Content: strPtr(content)})
		case p.Text != "":
			texts = append(texts, p.Text)
		}
	}
	if len(texts) > 0 {
		out = append(out, oaMessage{Role: "user", Content: strPtr(strings.Join(texts, "\n\n"))})
	}
	return out
}
