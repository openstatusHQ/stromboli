# 0016. Any LLM provider, one model per task

- **Status:** Accepted
- **Date:** 2026-11-03

## Context

Self-hosters bring their own LLM key ([0002](0002-self-hosted-one-instance-per-organisation.md)) and many have a provider already chosen for them. Triage and root-cause analysis have different needs: triage should be fast, investigation thorough. Using different models also reduces shared blind spots.

## Decision

- Models are named `provider:model`, as in the [AI SDK provider registry](https://ai-sdk.dev/docs/reference/ai-sdk-core/provider-registry). Supported: Anthropic, OpenAI, Google Gemini, OpenRouter, DeepSeek, Ollama, and any OpenAI-compatible API via `OPENSRE_PROVIDER_<NAME>_BASE_URL`. An id with a slash and no prefix (`openai/gpt-5`) goes through Vercel AI Gateway.
- Claude is the default (`anthropic:claude-opus-5-5`).
- Each task picks its own model and effort: `OPENSRE_TRIAGE_MODEL` until the mitigation is published, `OPENSRE_INVESTIGATE_MODEL` for the root cause and report, both defaulting to `OPENSRE_MODEL`. The investigate model takes over the triage conversation, so it sees everything triage saw.
- Two adapters: Anthropic Messages (prompt caching, effort, refusal fallbacks) and OpenAI Chat Completions for everything else (effort as `reasoning_effort`). Reasoning a provider returns is sent back to it on later turns.
- The model must support tool calling. Stable prompt parts come first so caching applies.

## Consequences

- No vendor lock-in; users can run fully local with Ollama.
- Behaviour and quality vary by model, which makes evaluation ([0019](0019-evaluation-harness.md)) more important.
- Switching models between tasks resets the prompt cache once.
