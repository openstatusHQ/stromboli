package llm

import (
	"errors"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// GatewayName is the provider of ids like "openai/gpt-5", as in the AI SDK
// where a plain "creator/model" string goes through Vercel AI Gateway.
const GatewayName = "gateway"

// GatewayConfig configures Vercel AI Gateway.
type GatewayConfig struct {
	BaseURL string // default https://ai-gateway.vercel.sh
	// APIKey is an AI Gateway API key; OIDCToken a Vercel OIDC token. The
	// API key wins when both are set.
	APIKey    string
	OIDCToken string
}

// Gateway returns a provider for Vercel AI Gateway. Model names are the
// gateway's "creator/model" ids (see https://vercel.com/ai-gateway/models).
// Anthropic models go through its Anthropic Messages API, which keeps prompt
// caching, effort and thinking blocks; every other model goes through its
// OpenAI Chat Completions API.
func Gateway(cfg GatewayConfig) Provider {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://ai-gateway.vercel.sh"
	}
	token := cfg.APIKey
	if token == "" {
		token = cfg.OIDCToken
	}
	var anthropicAPI AnthropicMessages
	if token != "" {
		// One credential, sent as a Bearer token, and nothing from the
		// environment: ANTHROPIC_API_KEY is for the direct Anthropic provider.
		c := anthropic.NewClient(option.WithoutEnvironmentDefaults(), option.WithBaseURL(base+"/"), option.WithAuthToken(token))
		anthropicAPI = &c.Beta.Messages
	}
	chat := OpenAICompatible(OpenAIConfig{
		Name: GatewayName, BaseURL: base + "/v1", APIKey: token, LegacyMaxTokens: true,
		// The gateway takes every level and maps it to the serving provider.
		Effort: func(e string) string { return e },
	})
	return func(model string) (Model, error) {
		if token == "" {
			return nil, errors.New("AI_GATEWAY_API_KEY (or VERCEL_OIDC_TOKEN) is not set")
		}
		if !strings.Contains(model, "/") {
			return nil, errors.New(`gateway models are "creator/model", e.g. "openai/gpt-5"`)
		}
		if strings.HasPrefix(model, "anthropic/") {
			return newAnthropicModel(anthropicAPI, GatewayName, model, false), nil
		}
		return chat(model)
	}
}
