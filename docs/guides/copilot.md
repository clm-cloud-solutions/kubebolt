# KubeBolt AI Copilot — Configuration Guide

The AI Copilot is an in-app chatbot that combines deep Kubernetes knowledge with real-time
access to your cluster data via KubeBolt's API. It can answer questions, troubleshoot issues,
and explain insights — all from inside the KubeBolt UI.

**Important:** KubeBolt is not a managed AI service. You bring your own LLM provider API key.
KubeBolt has no AI billing — you pay your provider directly.

## Quick Start

### 1. Get an API key from your LLM provider

| Provider | Where to get a key |
|---|---|
| **Anthropic Claude** | https://console.anthropic.com/settings/keys |
| **OpenAI** | https://platform.openai.com/api-keys |
| **Self-hosted (Ollama, vLLM)** | Use any value as the key (only the endpoint matters) |

> See [copilot-providers.md](copilot-providers.md) for the **complete reference** of supported
> providers including Azure OpenAI, Groq, Together AI, OpenRouter, DeepSeek, Mistral,
> self-hosted models, and detailed configuration examples for each.

### 2. Configure KubeBolt

#### With Helm

```bash
helm upgrade --install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set copilot.enabled=true \
  --set copilot.provider=anthropic \
  --set copilot.apiKey=$ANTHROPIC_API_KEY
```

For production, store the API key in a Kubernetes Secret instead of inline:

```bash
kubectl create secret generic kubebolt-copilot-key \
  --from-literal=api-key=$ANTHROPIC_API_KEY

helm upgrade --install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set copilot.enabled=true \
  --set copilot.provider=anthropic \
  --set copilot.existingSecret=kubebolt-copilot-key
```

#### With Docker Compose

Copy `deploy/.env.example` to `deploy/.env` and fill in:

```bash
cp deploy/.env.example deploy/.env
# Edit deploy/.env and set KUBEBOLT_AI_API_KEY
cd deploy && docker compose up -d --build
```

### 3. Open the chat panel

Once KubeBolt restarts, click the sparkle icon in the bottom-right corner or press `Cmd+J`
(or `Ctrl+J` on Linux/Windows). The panel only appears if `KUBEBOLT_AI_API_KEY` is set on
the backend — otherwise the copilot is disabled.

## Configuration Reference

All configuration is done via environment variables on the API container.

### Primary provider (required)

| Variable | Required | Description | Default |
|---|---|---|---|
| `KUBEBOLT_AI_PROVIDER` | Yes | `anthropic`, `openai`, or `custom` | `anthropic` |
| `KUBEBOLT_AI_API_KEY` | Yes | Your provider API key. **Disables copilot if empty.** | — |
| `KUBEBOLT_AI_MODEL` | No | Model name | `claude-sonnet-4-6` (anthropic) / `gpt-4o` (openai) |
| `KUBEBOLT_AI_BASE_URL` | No | Custom endpoint URL | provider default |
| `KUBEBOLT_AI_MAX_TOKENS` | No | Max tokens per response | `4096` |

### Fallback model (optional)

When the primary provider fails (rate limits, 5xx errors, network issues), the backend
automatically retries with the fallback. The fallback can be a different provider, a
cheaper model from the same provider, or a self-hosted endpoint.

| Variable | Required | Description |
|---|---|---|
| `KUBEBOLT_AI_FALLBACK_PROVIDER` | No | Defaults to primary provider |
| `KUBEBOLT_AI_FALLBACK_API_KEY` | No | **Setting this enables the fallback** |
| `KUBEBOLT_AI_FALLBACK_MODEL` | No | Model name for the fallback |
| `KUBEBOLT_AI_FALLBACK_BASE_URL` | No | Custom endpoint for the fallback |

When the fallback is used, the chat UI shows a small "via fallback" badge so you know the
answer came from a different model.

## Recipes

### Recipe 1: Anthropic primary + OpenAI fallback (cross-provider HA)

```yaml
copilot:
  enabled: true
  provider: anthropic
  existingSecret: anthropic-key
  fallback:
    enabled: true
    provider: openai
    existingSecret: openai-key
    model: gpt-4o-mini
```

### Recipe 2: Premium primary + cheap fallback (cost optimization)

```yaml
copilot:
  enabled: true
  provider: anthropic
  model: claude-opus-4-6
  existingSecret: anthropic-key
  fallback:
    enabled: true
    provider: anthropic
    model: claude-haiku-4-5
    existingSecret: anthropic-key
```

### Recipe 3: Cloud primary + self-hosted fallback (resilience)

```yaml
copilot:
  enabled: true
  provider: anthropic
  existingSecret: anthropic-key
  fallback:
    enabled: true
    provider: custom
    baseUrl: http://ollama.internal:11434/v1
    apiKey: dummy-not-used
    model: llama3.1:70b
```

## Privacy & Security

### What data goes to the LLM provider?

- The user's chat messages
- The system prompt (which describes the copilot's role)
- Tool call results — which contain **cluster data** the copilot fetches via tools

This means the LLM provider you choose **will see your cluster data** (resource names,
status, logs, events, etc.) when the copilot fetches it. Choose a provider that meets
your compliance requirements.

### What KubeBolt does to protect you

- **API keys never reach the browser** — the Go backend proxies all LLM requests.
- **Secret values are redacted** — KubeBolt redacts Secret resource values at the API
  layer before they're returned to anything (including the copilot).
- **Permission awareness** — the copilot can only see what your kubeconfig has access to.
  If you don't have permission to read Secrets, neither does the copilot.
- **Sensitive data warnings** — the copilot is instructed to detect potential credentials
  in pod logs and warn you instead of echoing them verbatim.
- **No KubeBolt telemetry** — KubeBolt does not send any data to its developers about
  your copilot usage.

### What you should do

- Use a separate API key for KubeBolt (don't reuse a key from another app).
- Store the key in a Kubernetes Secret (`existingSecret`), not inline in values.yaml.
- Rotate the key periodically and restart the API pod after rotation.
- For sensitive clusters, consider self-hosted models (Ollama, vLLM) via `baseUrl`.

## Troubleshooting

**The copilot panel doesn't appear.**
The backend probably doesn't have `KUBEBOLT_AI_API_KEY` set. Check the API pod logs for:
```
AI Copilot enabled: provider=anthropic model=claude-sonnet-4-6
```
If you see "AI Copilot disabled", the env var isn't set on the API container.

**Chat returns 503.**
The cluster is not connected. The copilot needs an active cluster connection because tool
execution requires the cluster informers. Check the cluster connection status in KubeBolt.

**Chat returns "API error 401" or "403".**
Your API key is wrong or expired. Generate a new one in the provider console and update
the Secret.

**Chat returns "API error 429" frequently.**
You're hitting your provider's rate limits. Configure a fallback model or upgrade your
plan with the provider.

**The fallback never triggers.**
The fallback only activates on **recoverable** errors (429, 5xx, network). 4xx errors
(auth, bad request) propagate to the user. Check the API pod logs for the actual error.

## Disabling the Copilot

Just unset the API key and restart:

```bash
helm upgrade kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
  --set copilot.enabled=false
```

The chat panel will disappear from the UI.

## See Also

- **[copilot-providers.md](copilot-providers.md)** — Complete reference of supported LLM
  providers, endpoint URLs, model recommendations, configuration examples for each, and
  cost/compatibility notes.
