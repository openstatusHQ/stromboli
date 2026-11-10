# How to choose models

A model is written `provider:model`. By default every phase uses `anthropic:claude-opus-5-5`. The model must support tool calling.

## Use one model for everything

Set `STROMBOLI_MODEL` and the provider's key:

```sh
STROMBOLI_MODEL=openai:gpt-5
OPENAI_API_KEY=sk-...
```

## Use a fast model for triage, a thorough one for the root cause

An investigation has two phases: **triage**, until the mitigation is published, where speed matters most, and **investigate**, for the root cause and the report. Give each its own model and effort:

```sh
STROMBOLI_TRIAGE_MODEL=anthropic:claude-haiku-5-5
STROMBOLI_TRIAGE_EFFORT=low
STROMBOLI_INVESTIGATE_MODEL=openai:gpt-5
STROMBOLI_INVESTIGATE_EFFORT=high
```

The investigate model takes over the triage conversation, so it sees everything triage saw. Unset values fall back to `STROMBOLI_MODEL` and `STROMBOLI_EFFORT`. Effort is `low`, `medium`, `high`, `xhigh` or `max`; leave it unset for the provider's default.

## Use every model with one key (Vercel AI Gateway)

An id with a slash and no `provider:` prefix goes through [Vercel AI Gateway](https://vercel.com/ai-gateway), as in the AI SDK:

```sh
AI_GATEWAY_API_KEY=...
STROMBOLI_TRIAGE_MODEL=anthropic/claude-haiku-5.5
STROMBOLI_INVESTIGATE_MODEL=openai/gpt-5
```

## Use a specific provider

| Provider | Model id | Set |
|---|---|---|
| Anthropic | `anthropic:claude-opus-5-5`, or a bare `claude-…` id | `ANTHROPIC_API_KEY` |
| OpenAI | `openai:gpt-5` | `OPENAI_API_KEY`; `OPENAI_BASE_URL` for Azure or a proxy |
| Google Gemini | `google:gemini-2.5-pro` | `GEMINI_API_KEY` |
| OpenRouter | `openrouter:meta-llama/llama-3.3-70b-instruct` | `OPENROUTER_API_KEY` |
| DeepSeek | `deepseek:deepseek-v4-pro` | `DEEPSEEK_API_KEY`; optional `DEEPSEEK_BASE_URL` |
| Vercel AI Gateway | `openai/gpt-5` or `gateway:<id>` | `AI_GATEWAY_API_KEY` or `VERCEL_OIDC_TOKEN`; optional `AI_GATEWAY_BASE_URL` |
| Ollama | `ollama:qwen3:32b` | `OLLAMA_BASE_URL` (default `http://localhost:11434/v1`) |

## Use any OpenAI-compatible API

Pick a provider name, and set its base URL (and key, if it needs one). The name is upper-cased, with `-` written `_`:

```sh
STROMBOLI_PROVIDER_GROQ_BASE_URL=https://api.groq.com/openai/v1
STROMBOLI_PROVIDER_GROQ_API_KEY=gsk_...
STROMBOLI_MODEL=groq:llama-3.3-70b-versatile
```

## Check it

On startup the server logs the models it resolved:

```text
level=INFO msg="stromboli listening" addr=:8080 triage_model=anthropic:claude-haiku-5-5 investigate_model=openai:gpt-5
```

If a model cannot be resolved (unknown provider, missing key), the server still starts, logs `LLM is not configured: briefs work, investigations are disabled`, and starting an investigation fails with `no LLM is configured`.

Each `llm` step of an investigation (`stromboli show <id> --json`) records the task, model and token counts of that turn.

## See also

- [Configuration reference](../reference/configuration.md#models-and-providers)
- [ADR 0016: any LLM provider, one model per task](../adr/0016-multi-provider-llm-per-task-models.md)
