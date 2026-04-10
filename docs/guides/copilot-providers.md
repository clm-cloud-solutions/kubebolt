# KubeBolt Copilot — LLM Providers Reference

Complete reference of LLM providers supported by the KubeBolt Copilot, including endpoint
URLs, model names, configuration examples, and selection guidance.

> **TL;DR** — KubeBolt is BYO key. You configure your own provider. The two adapters built
> into KubeBolt are `anthropic` (native) and `openai` (which works for any OpenAI-compatible
> API including Azure, Groq, Together, Ollama, and most self-hosted runners).

## Table of Contents

1. [Native Providers](#native-providers)
2. [OpenAI-Compatible Providers](#openai-compatible-providers)
3. [Self-Hosted Models](#self-hosted-models)
4. [Configuration Examples](#configuration-examples)
5. [Choosing a Model](#choosing-a-model)
6. [Cost & Token Reference](#cost--token-reference)
7. [Compatibility Notes](#compatibility-notes)

---

## Native Providers

These providers have a dedicated adapter in KubeBolt. The base URL is hardcoded as the
default — you only need to set it if you're routing through a proxy or self-hosted gateway.

### Anthropic Claude

| Field | Value |
|---|---|
| `KUBEBOLT_AI_PROVIDER` | `anthropic` |
| Default base URL | `https://api.anthropic.com/v1/messages` |
| Get an API key | https://console.anthropic.com/settings/keys |

**Recommended models:**

| Model ID | Best for | Speed | Cost |
|---|---|---|---|
| `claude-opus-4-6` | Complex troubleshooting, deep reasoning | Slow | $$$ |
| `claude-sonnet-4-6` | **Default** — balanced quality and speed | Medium | $$ |
| `claude-haiku-4-5` | Fast responses, simple queries, fallback | Fast | $ |

```bash
KUBEBOLT_AI_PROVIDER=anthropic
KUBEBOLT_AI_API_KEY=sk-ant-api03-...
KUBEBOLT_AI_MODEL=claude-sonnet-4-6
```

---

### OpenAI

| Field | Value |
|---|---|
| `KUBEBOLT_AI_PROVIDER` | `openai` |
| Default base URL | `https://api.openai.com/v1/chat/completions` |
| Get an API key | https://platform.openai.com/api-keys |

**Recommended models:**

| Model ID | Best for | Speed | Cost |
|---|---|---|---|
| `gpt-4o` | **Default** — high-quality general use | Medium | $$ |
| `gpt-4o-mini` | Cost-sensitive, fallback, simple queries | Fast | $ |
| `o1` | Complex reasoning (no streaming, no tools) | Slow | $$$ |

> **Note:** OpenAI's `o1` series doesn't currently support function/tool calling — KubeBolt
> needs tools to fetch cluster data, so `o1` won't work as a primary model. Use `gpt-4o`.

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_API_KEY=sk-proj-...
KUBEBOLT_AI_MODEL=gpt-4o
```

---

## OpenAI-Compatible Providers

These providers expose an OpenAI-compatible API. Use `KUBEBOLT_AI_PROVIDER=openai` and
override `KUBEBOLT_AI_BASE_URL` to point at their endpoint.

### Azure OpenAI

| Field | Value |
|---|---|
| Base URL | `https://{resource}.openai.azure.com/openai/deployments/{deployment}/chat/completions?api-version=2024-02-15-preview` |
| Auth | Azure API key |

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://my-resource.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=2024-02-15-preview
KUBEBOLT_AI_API_KEY=<azure-api-key>
```

> The Azure URL embeds the deployment name, so `KUBEBOLT_AI_MODEL` is ignored.

---

### Groq

Ultra-fast inference for open-weights models (Llama, Mixtral, Gemma).

| Field | Value |
|---|---|
| Base URL | `https://api.groq.com/openai/v1/chat/completions` |
| Get an API key | https://console.groq.com/keys |

**Available models:** `llama-3.3-70b-versatile`, `llama-3.1-70b-versatile`, `mixtral-8x7b-32768`, `gemma2-9b-it`

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://api.groq.com/openai/v1/chat/completions
KUBEBOLT_AI_API_KEY=gsk_...
KUBEBOLT_AI_MODEL=llama-3.3-70b-versatile
```

---

### Together AI

| Field | Value |
|---|---|
| Base URL | `https://api.together.xyz/v1/chat/completions` |
| Get an API key | https://api.together.xyz/settings/api-keys |

**Available models:** `meta-llama/Llama-3.3-70B-Instruct-Turbo`, `Qwen/Qwen2.5-72B-Instruct-Turbo`, and many more.

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://api.together.xyz/v1/chat/completions
KUBEBOLT_AI_API_KEY=<together-key>
KUBEBOLT_AI_MODEL=meta-llama/Llama-3.3-70B-Instruct-Turbo
```

---

### OpenRouter

A unified API that routes to dozens of providers behind a single endpoint. Useful if you
want to switch models without changing your config.

| Field | Value |
|---|---|
| Base URL | `https://openrouter.ai/api/v1/chat/completions` |
| Get an API key | https://openrouter.ai/keys |

**Available models:** Anthropic, OpenAI, Google, Meta, Mistral, and more — model names use
the format `provider/model` (e.g. `anthropic/claude-sonnet-4-6`).

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://openrouter.ai/api/v1/chat/completions
KUBEBOLT_AI_API_KEY=sk-or-...
KUBEBOLT_AI_MODEL=anthropic/claude-sonnet-4-6
```

---

### DeepSeek

| Field | Value |
|---|---|
| Base URL | `https://api.deepseek.com/v1/chat/completions` |
| Get an API key | https://platform.deepseek.com/api_keys |

**Available models:** `deepseek-chat`, `deepseek-reasoner`

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://api.deepseek.com/v1/chat/completions
KUBEBOLT_AI_API_KEY=sk-...
KUBEBOLT_AI_MODEL=deepseek-chat
```

---

### Mistral

| Field | Value |
|---|---|
| Base URL | `https://api.mistral.ai/v1/chat/completions` |
| Get an API key | https://console.mistral.ai/api-keys |

**Available models:** `mistral-large-latest`, `mistral-medium-latest`, `mistral-small-latest`, `codestral-latest`

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://api.mistral.ai/v1/chat/completions
KUBEBOLT_AI_API_KEY=<mistral-key>
KUBEBOLT_AI_MODEL=mistral-large-latest
```

---

### Fireworks AI

| Field | Value |
|---|---|
| Base URL | `https://api.fireworks.ai/inference/v1/chat/completions` |
| Get an API key | https://fireworks.ai/account/api-keys |

**Available models:** `accounts/fireworks/models/llama-v3p3-70b-instruct`, `accounts/fireworks/models/qwen2p5-72b-instruct`, etc.

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://api.fireworks.ai/inference/v1/chat/completions
KUBEBOLT_AI_API_KEY=<fireworks-key>
KUBEBOLT_AI_MODEL=accounts/fireworks/models/llama-v3p3-70b-instruct
```

---

### Other Compatible Providers

These also expose an OpenAI-compatible API and work with `KUBEBOLT_AI_PROVIDER=openai`:

| Provider | Base URL |
|---|---|
| **Anyscale** | `https://api.endpoints.anyscale.com/v1/chat/completions` |
| **Perplexity** | `https://api.perplexity.ai/chat/completions` |
| **Cerebras** | `https://api.cerebras.ai/v1/chat/completions` |
| **xAI Grok** | `https://api.x.ai/v1/chat/completions` |

---

## Self-Hosted Models

Run a local LLM and point KubeBolt at it. Useful for air-gapped clusters, sensitive data,
or cost reasons.

> **Tool calling requirement:** The KubeBolt Copilot relies on **function/tool calling** to
> fetch cluster data. The model you self-host **must** support tool calling. Not all
> open-weights models do — see [Compatibility Notes](#compatibility-notes).

### Ollama

The easiest way to run local models. Ollama exposes an OpenAI-compatible API on port 11434.

| Field | Value |
|---|---|
| Base URL | `http://localhost:11434/v1/chat/completions` (or `http://ollama.cluster.local:11434/v1/chat/completions` from inside K8s) |
| Auth | Ollama doesn't require auth — set `KUBEBOLT_AI_API_KEY` to any non-empty string |

**Models with reliable tool calling:** `llama3.1:70b`, `llama3.1:8b`, `qwen2.5:72b`, `qwen2.5:14b`, `mistral-nemo`

```bash
# Pull the model first
ollama pull llama3.1:70b

# Configure KubeBolt
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=http://localhost:11434/v1/chat/completions
KUBEBOLT_AI_API_KEY=ollama
KUBEBOLT_AI_MODEL=llama3.1:70b
```

For KubeBolt running inside Kubernetes pointing at Ollama running on the host machine
(Docker Desktop / Minikube), use `http://host.docker.internal:11434/v1/chat/completions`.

---

### vLLM

Production-grade serving for open-weights models with high throughput.

| Field | Value |
|---|---|
| Base URL | `http://localhost:8000/v1/chat/completions` |
| Auth | Optional — set via `--api-key` flag when starting vLLM |

```bash
# Start vLLM with a model that supports tool calling
vllm serve Qwen/Qwen2.5-72B-Instruct \
  --enable-auto-tool-choice \
  --tool-call-parser hermes

# Configure KubeBolt
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=http://localhost:8000/v1/chat/completions
KUBEBOLT_AI_API_KEY=vllm
KUBEBOLT_AI_MODEL=Qwen/Qwen2.5-72B-Instruct
```

---

### LM Studio

GUI-based local LLM runner with an OpenAI-compatible server.

| Field | Value |
|---|---|
| Base URL | `http://localhost:1234/v1/chat/completions` |
| Auth | None |

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=http://localhost:1234/v1/chat/completions
KUBEBOLT_AI_API_KEY=lmstudio
KUBEBOLT_AI_MODEL=qwen2.5-7b-instruct
```

---

### llama.cpp server

| Field | Value |
|---|---|
| Base URL | `http://localhost:8080/v1/chat/completions` |
| Auth | None by default |

---

## Configuration Examples

### Example 1: Cloud production with cross-provider fallback

Anthropic Claude as primary, OpenAI as fallback for high availability.

```yaml
# values.yaml
copilot:
  enabled: true
  provider: anthropic
  model: claude-sonnet-4-6
  existingSecret: anthropic-key
  fallback:
    enabled: true
    provider: openai
    model: gpt-4o
    existingSecret: openai-key
```

```bash
kubectl create secret generic anthropic-key --from-literal=api-key=sk-ant-...
kubectl create secret generic openai-key --from-literal=api-key=sk-proj-...
helm upgrade --install kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt -f values.yaml
```

---

### Example 2: Cost-optimized — premium primary, cheap fallback

Use the smart model only when it works, fall back to a cheaper model on rate limits.

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

---

### Example 3: Privacy-first — self-hosted with cloud fallback

Use a local model for sensitive cluster data; fall back to cloud only if local fails.

```yaml
copilot:
  enabled: true
  provider: custom
  baseUrl: http://ollama.kubebolt.svc.cluster.local:11434/v1/chat/completions
  model: llama3.1:70b
  apiKey: ollama
  fallback:
    enabled: true
    provider: anthropic
    model: claude-haiku-4-5
    existingSecret: anthropic-key
```

---

### Example 4: Air-gapped — fully self-hosted, no fallback

```yaml
copilot:
  enabled: true
  provider: custom
  baseUrl: http://vllm.kubebolt.svc.cluster.local:8000/v1/chat/completions
  model: Qwen/Qwen2.5-72B-Instruct
  apiKey: dummy
  # No fallback configured — provider stays local
```

---

### Example 5: OpenRouter for model flexibility

Use OpenRouter to swap models without changing configuration plumbing.

```yaml
copilot:
  enabled: true
  provider: openai
  baseUrl: https://openrouter.ai/api/v1/chat/completions
  model: anthropic/claude-sonnet-4-6
  existingSecret: openrouter-key
  fallback:
    enabled: true
    provider: openai
    baseUrl: https://openrouter.ai/api/v1/chat/completions
    model: meta-llama/llama-3.3-70b-instruct
    existingSecret: openrouter-key
```

---

### Example 6: Local development with `make dev`

Create a `.env` file in the repo root:

```bash
# .env
KUBEBOLT_AI_PROVIDER=anthropic
KUBEBOLT_AI_API_KEY=sk-ant-api03-...
KUBEBOLT_AI_MODEL=claude-sonnet-4-6
```

Then run:

```bash
make dev
```

The Makefile auto-loads `.env` and exports the variables to the API process.

---

## Choosing a Model

### Quality matters more than speed?
- **Claude Opus 4.6** — best reasoning, slowest
- **Claude Sonnet 4.6** — KubeBolt's default, balanced
- **GPT-4o** — strong alternative
- **Llama 3.3 70B** (Groq, Together) — open-weights with great quality

### Speed matters?
- **Claude Haiku 4.5** — fast and cheap
- **GPT-4o-mini** — fast and cheap
- **Groq Llama 3.3** — extreme speed (sub-second) thanks to LPUs
- **Cerebras** — also extremely fast for open-weights

### Cost matters?
- **Claude Haiku 4.5** — cheapest in the Anthropic family
- **GPT-4o-mini** — cheapest in the OpenAI family
- **DeepSeek Chat** — extremely cheap, good quality
- **Self-hosted (Ollama, vLLM)** — pay only for compute

### Privacy matters?
- **Self-hosted** (Ollama, vLLM, LM Studio) — data never leaves your network
- **Azure OpenAI** with private endpoints — enterprise compliance
- **AWS Bedrock** — coming soon (not yet supported by KubeBolt natively)

---

## Cost & Token Reference

Approximate token usage per copilot interaction (input + output combined):

| Question type | Tokens (rough) |
|---|---|
| Simple K8s concept ("what is a pod?") | ~500-1,500 |
| Cluster overview ("what's wrong?") | ~3,000-8,000 |
| Pod troubleshooting (with logs) | ~5,000-15,000 |
| Topology analysis | ~10,000-30,000 |
| Multi-step troubleshooting | ~20,000-50,000 |

Token costs vary by provider — check each one's pricing page. As of writing:

| Model | Input ($/1M tokens) | Output ($/1M tokens) |
|---|---|---|
| Claude Opus 4.6 | ~$15 | ~$75 |
| Claude Sonnet 4.6 | ~$3 | ~$15 |
| Claude Haiku 4.5 | ~$0.80 | ~$4 |
| GPT-4o | ~$2.50 | ~$10 |
| GPT-4o-mini | ~$0.15 | ~$0.60 |
| DeepSeek Chat | ~$0.27 | ~$1.10 |
| Groq Llama 3.3 70B | ~$0.59 | ~$0.79 |

> Always verify current pricing on the provider's website. KubeBolt reads no telemetry and
> reports no usage data — your bill comes directly from the LLM provider.

---

## Compatibility Notes

### Tool calling support

The KubeBolt Copilot **requires** function/tool calling because all cluster data is fetched
through tools. A model without tool calling will not work as the primary model.

| Provider/Model | Tool Calling | Notes |
|---|---|---|
| Claude (any 3.5+) | ✅ Yes | Native, most reliable |
| GPT-4o, GPT-4o-mini | ✅ Yes | Native, very reliable |
| GPT-o1 series | ❌ No | Reasoning models don't support tools yet |
| Llama 3.1+ (70B, 405B) | ⚠️ Yes (varies) | Quality varies by serving stack — Groq, Together, vLLM with `--enable-auto-tool-choice` work well |
| Llama 3.1 8B | ⚠️ Limited | Sometimes struggles with multi-step tool calls |
| Qwen 2.5 72B | ✅ Yes | Strong tool calling, especially via vLLM |
| Mistral Large | ✅ Yes | Good tool calling |
| Mistral Small/Nemo | ⚠️ Limited | Works but less reliable |
| DeepSeek Chat | ✅ Yes | OpenAI-compatible tool calling |
| Gemma 2 | ❌ No | No native tool calling |

### Streaming support

KubeBolt's chat handler does **not** require streaming from the provider — it makes blocking
calls and streams the **final** response over SSE to the frontend. Any provider that
returns a complete response will work, even if it doesn't support streaming itself.

### Context window

Tools generate context quickly. A typical multi-step troubleshooting session can fill
20-50K tokens of context. Choose models with at least **128K context window**:

- Claude 4.x family: 200K tokens
- GPT-4o: 128K tokens
- Llama 3.3: 128K tokens
- Qwen 2.5: 128K tokens
- DeepSeek Chat: 128K tokens

Models with smaller context windows (e.g., Mixtral 8x7B at 32K) will start truncating
mid-conversation.

---

## Troubleshooting

**"Tool call returned no result" or similar errors with open-weights models:**
The model isn't producing valid tool call syntax. Try a model with stronger tool support
(Llama 3.3 70B, Qwen 2.5 72B, or switch to Claude/GPT-4o).

**Self-hosted Ollama returns 404 for `/v1/chat/completions`:**
Make sure you're hitting the OpenAI-compatible endpoint (path `/v1/chat/completions`), not
Ollama's native API (path `/api/chat`).

**vLLM serves the model but tool calling doesn't work:**
You need to start vLLM with `--enable-auto-tool-choice --tool-call-parser hermes` (or the
parser appropriate for your model). Check vLLM docs for the correct parser per model.

**Azure OpenAI returns 404:**
Double-check that the deployment name in the URL matches an actual deployment in your Azure
resource. `KUBEBOLT_AI_MODEL` is ignored for Azure — the deployment is encoded in the URL.

**OpenRouter charges more than expected:**
OpenRouter adds a small markup on top of provider rates. For pure cost optimization, go
direct to the underlying provider.
