package llm

import (
	"os"
	"strings"
)

// FromEnv returns a registry with the built-in providers, configured from
// environment variables:
//
//	anthropic   ANTHROPIC_API_KEY
//	openai      OPENAI_API_KEY, OPENAI_BASE_URL
//	google      GEMINI_API_KEY (Gemini's OpenAI-compatible endpoint)
//	openrouter  OPENROUTER_API_KEY
//	deepseek    DEEPSEEK_API_KEY, DEEPSEEK_BASE_URL
//	gateway     AI_GATEWAY_API_KEY or VERCEL_OIDC_TOKEN, AI_GATEWAY_BASE_URL
//	            (Vercel AI Gateway; also any id like "openai/gpt-5")
//	ollama      OLLAMA_BASE_URL (default http://localhost:11434/v1)
//
// Any other provider name is an OpenAI-compatible endpoint configured with
// STROMBOLI_PROVIDER_<NAME>_BASE_URL and, if needed, STROMBOLI_PROVIDER_<NAME>_API_KEY
// (NAME upper-cased, "-" as "_"), so "groq:llama-3.3-70b" works once
// STROMBOLI_PROVIDER_GROQ_BASE_URL is set.
func FromEnv(getenv func(string) string) *Registry {
	if getenv == nil {
		getenv = os.Getenv
	}
	env := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	r := NewRegistry()
	r.Register("anthropic", Anthropic(NewAnthropicClient(getenv("ANTHROPIC_API_KEY"))))
	r.Register("openai", OpenAICompatible(OpenAIConfig{
		Name: "openai", BaseURL: env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		APIKey: getenv("OPENAI_API_KEY"), RequireKey: true, KeyEnv: "OPENAI_API_KEY",
	}))
	r.Register("google", OpenAICompatible(OpenAIConfig{
		Name: "google", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		APIKey: getenv("GEMINI_API_KEY"), RequireKey: true, KeyEnv: "GEMINI_API_KEY", LegacyMaxTokens: true,
	}))
	r.Register("openrouter", OpenAICompatible(OpenAIConfig{
		Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1",
		APIKey: getenv("OPENROUTER_API_KEY"), RequireKey: true, KeyEnv: "OPENROUTER_API_KEY", LegacyMaxTokens: true,
	}))
	r.Register("deepseek", OpenAICompatible(OpenAIConfig{
		Name: "deepseek", BaseURL: env("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
		APIKey: getenv("DEEPSEEK_API_KEY"), RequireKey: true, KeyEnv: "DEEPSEEK_API_KEY", LegacyMaxTokens: true,
		// DeepSeek takes low/high/max and maps the other levels itself.
		Effort: func(e string) string { return e },
	}))
	r.Register(GatewayName, Gateway(GatewayConfig{
		BaseURL: getenv("AI_GATEWAY_BASE_URL"), APIKey: getenv("AI_GATEWAY_API_KEY"), OIDCToken: getenv("VERCEL_OIDC_TOKEN"),
	}))
	r.Register("ollama", OpenAICompatible(OpenAIConfig{
		Name: "ollama", BaseURL: env("OLLAMA_BASE_URL", "http://localhost:11434/v1"), LegacyMaxTokens: true,
	}))
	r.fallback = func(name string) (Provider, bool) {
		prefix := "STROMBOLI_PROVIDER_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_"
		base := getenv(prefix + "BASE_URL")
		if base == "" {
			return nil, false
		}
		return OpenAICompatible(OpenAIConfig{
			Name: name, BaseURL: base, APIKey: getenv(prefix + "API_KEY"), LegacyMaxTokens: true,
		}), true
	}
	return r
}
