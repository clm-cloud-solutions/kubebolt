# Kobi Copilot — LLM Providers Reference

Reference of the LLM providers you can use with Kobi Copilot, KubeBolt's AI SRE in the
open-source edition: endpoint URLs, model IDs, configuration examples and selection guidance.
For enabling Kobi, configuration precedence and every `KUBEBOLT_AI_*` variable, see
[copilot.md](copilot.md).

> **TL;DR** — Kobi is bring-your-own-key. KubeBolt ships two adapters: `anthropic` (native
> Claude API) and `openai`, which works with any OpenAI-compatible Chat Completions API —
> OpenAI itself, Azure OpenAI, xAI, DeepSeek, Groq, Mistral, Ollama, vLLM and most
> self-hosted runners. `KUBEBOLT_AI_PROVIDER` accepts only these two values.

Everything below can be set either with environment variables (shown here) or in
**Administration → AI (Kobi) → Configuration**, which offers the curated model catalog from
`apps/web/src/pages/admin/settings/modelCatalog.ts` plus a **Custom…** option for any other
model ID.

## Table of Contents

1. [Native Providers](#native-providers)
2. [OpenAI-Compatible Providers](#openai-compatible-providers)
3. [Self-Hosted Models](#self-hosted-models)
4. [Configuration Examples](#configuration-examples)
5. [Choosing a Model](#choosing-a-model)
6. [Cost & Token Reference](#cost--token-reference)
7. [Compatibility Notes](#compatibility-notes)
8. [Troubleshooting](#troubleshooting)

---

## The base URL is the full endpoint

`KUBEBOLT_AI_BASE_URL` (and `KUBEBOLT_AI_FALLBACK_BASE_URL`) is the **complete request URL**.
KubeBolt POSTs to it as-is and never appends a path, so for OpenAI-compatible providers it
must end in the chat-completions path — e.g. `https://api.x.ai/v1/chat/completions`, not
`https://api.x.ai/v1`.

The `openai` adapter sends the key as `Authorization: Bearer <key>`.

---

## Native Providers

These are KubeBolt's two adapters. Their default URL is built in — set a base URL only when
routing through a proxy or gateway.

### Anthropic Claude

| Field | Value |
|---|---|
| `KUBEBOLT_AI_PROVIDER` | `anthropic` |
| Default base URL | `https://api.anthropic.com/v1/messages` |
| Default model | `claude-sonnet-5` |
| Get an API key | https://console.anthropic.com/settings/keys |

**Models in the catalog:**

| Model ID | Best for | Cost |
|---|---|---|
| `claude-sonnet-5` | **Default** — balanced depth and speed for live chat | $$ |
| `claude-opus-5` | Deep reasoning and complex investigations | $$$ |
| `claude-fable-5` | Hardest, long-horizon investigations; highest cost and latency | $$$$ |
| `claude-haiku-4-5` | Fast, cheap; short turns, fallback, compaction | $ |

Previous-generation models are also in the catalog for accounts pinned to them:
`claude-opus-4-8`, `claude-opus-4-7`, `claude-opus-4-6`, `claude-sonnet-4-6`,
`claude-sonnet-4-5`, and the date-pinned `claude-opus-4-5-20251101`,
`claude-opus-4-1-20250805`, `claude-opus-4-20250514`, `claude-sonnet-4-20250514`.

```bash
KUBEBOLT_AI_PROVIDER=anthropic
KUBEBOLT_AI_API_KEY=sk-ant-api03-...
KUBEBOLT_AI_MODEL=claude-sonnet-5
```

---

### OpenAI

| Field | Value |
|---|---|
| `KUBEBOLT_AI_PROVIDER` | `openai` |
| Default base URL | `https://api.openai.com/v1/chat/completions` |
| Default model | `gpt-4o` (used when no model is set) |
| Get an API key | https://platform.openai.com/api-keys |

**Models in the catalog:**

| Model ID | Best for | Cost |
|---|---|---|
| `gpt-5.6-sol` | Catalog's recommended primary for OpenAI — deepest reasoning, 1.05M-token window | $$$ |
| `gpt-5.6-terra` | Strong general-purpose default at a fraction of Sol's cost | $$ |
| `gpt-5.6-luna` | Fastest and cheapest of the GPT-5.6 line | $ |
| `gpt-4o` | Backend default; solid, well-understood tool calling | $$ |
| `gpt-4o-mini` | Cost-sensitive, fallback, compaction | $ |
| `o3`, `o3-mini`, `o4-mini` | Reasoning models for multi-step tool planning; slower | $$ |

Also in the catalog: `gpt-5.5`, `gpt-5.4`, `gpt-5.4-mini`, `gpt-5.4-nano`, `gpt-5.2`,
`gpt-5.2-chat-latest`, `gpt-5.1`, `gpt-5.1-chat-latest`, `gpt-5`, `gpt-5-chat-latest`,
`gpt-4.1`, `gpt-4.1-mini`, `gpt-4.1-nano`.

> Pro variants are not supported: OpenAI serves them only through `/v1/responses`, and
> KubeBolt's adapter uses `/v1/chat/completions`.

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_API_KEY=sk-proj-...
KUBEBOLT_AI_MODEL=gpt-5.6-terra
```

---

## OpenAI-Compatible Providers

These providers expose an OpenAI-compatible API. Use `KUBEBOLT_AI_PROVIDER=openai` and set
`KUBEBOLT_AI_BASE_URL` to their **full** chat-completions endpoint.

> **Set a compaction model.** Auto-compaction defaults to `gpt-4o-mini` for the `openai`
> adapter, which only OpenAI serves. With any other endpoint, set
> `KUBEBOLT_AI_COMPACT_MODEL` to a cheap model that endpoint offers (see
> [copilot.md](copilot.md#conversation-memory)).

### Azure OpenAI

| Field | Value |
|---|---|
| Base URL | `https://{resource}.openai.azure.com/openai/deployments/{deployment}/chat/completions?api-version=2024-02-15-preview` |
| Auth | Sent as `Authorization: Bearer <key>` |

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://my-resource.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=2024-02-15-preview
KUBEBOLT_AI_API_KEY=<azure-credential>
```

> The Azure URL embeds the deployment, which decides the model Azure runs. KubeBolt still
> sends `KUBEBOLT_AI_MODEL` (default `gpt-4o`) in the request body and uses it for context
> window and cost estimates, so set it to the model behind your deployment.
>
> KubeBolt does not send Azure's `api-key` header. If your resource only accepts that header,
> put a gateway in front that accepts a bearer credential.

---

### xAI Grok

| Field | Value |
|---|---|
| Base URL | `https://api.x.ai/v1/chat/completions` |
| Get an API key | https://console.x.ai |

**Models in the catalog:**

| Model ID | Best for | Cost (in/out per 1M) |
|---|---|---|
| `grok-4.3` | **Recommended** — xAI's current flagship | $1.25 / $2.50 |
| `grok-4.20-0309-reasoning` | Multi-step problems, RCA | $1.25 / $2.50 |
| `grok-4.20-0309-non-reasoning` | Faster simple queries | $1.25 / $2.50 |

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://api.x.ai/v1/chat/completions
KUBEBOLT_AI_API_KEY=xai-...
KUBEBOLT_AI_MODEL=grok-4.3
```

> Image, video and voice models are not supported — Kobi needs text chat completions with tool
> calling. `grok-4.20-multi-agent` is excluded from the catalog as well.

---

### DeepSeek

| Field | Value |
|---|---|
| Base URL | `https://api.deepseek.com/v1/chat/completions` |
| Get an API key | https://platform.deepseek.com/api_keys |

**Models in the catalog:** `deepseek-chat`, `deepseek-reasoner`

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://api.deepseek.com/v1/chat/completions
KUBEBOLT_AI_API_KEY=sk-...
KUBEBOLT_AI_MODEL=deepseek-chat
```

---

### Alibaba Qwen (DashScope)

| Field | Value |
|---|---|
| Base URL | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions` |

**Models in the catalog:** `qwen-max`, `qwen-plus`, `qwen-turbo`

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions
KUBEBOLT_AI_API_KEY=<dashscope-key>
KUBEBOLT_AI_MODEL=qwen-plus
```

---

### Groq

Fast inference for open-weights models.

| Field | Value |
|---|---|
| Base URL | `https://api.groq.com/openai/v1/chat/completions` |
| Get an API key | https://console.groq.com/keys |

**Models in the catalog:** `llama-3.3-70b-versatile`, `llama-3.1-70b-versatile`, `llama-3.1-8b-instant`

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://api.groq.com/openai/v1/chat/completions
KUBEBOLT_AI_API_KEY=gsk_...
KUBEBOLT_AI_MODEL=llama-3.3-70b-versatile
```

---

### Mistral

| Field | Value |
|---|---|
| Base URL | `https://api.mistral.ai/v1/chat/completions` |
| Get an API key | https://console.mistral.ai/api-keys |

**Models in the catalog:** `mistral-large-latest`, `mistral-medium-latest`, `mistral-small-latest`, `open-mistral-nemo`

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://api.mistral.ai/v1/chat/completions
KUBEBOLT_AI_API_KEY=<mistral-key>
KUBEBOLT_AI_MODEL=mistral-large-latest
```

---

### MiniMax

MiniMax models are not in the UI catalog (pick **Custom…** in the Configuration page), but
their pricing is still known to the usage estimator.

| Field | Value |
|---|---|
| Base URL | `https://api.minimax.io/v1/text/chatcompletion_v2` |
| Get an API key | https://platform.minimax.io |

| Model ID | Cost (in/out per 1M) |
|---|---|
| `MiniMax-M2.7` | $0.30 / $1.20 |
| `MiniMax-M2.7-highspeed` | $0.60 / $2.40 |
| `MiniMax-M2.5` | $0.30 / $1.20 |
| `MiniMax-M2.5-highspeed` | $0.60 / $2.40 |

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://api.minimax.io/v1/text/chatcompletion_v2
KUBEBOLT_AI_API_KEY=<minimax-key>
KUBEBOLT_AI_MODEL=MiniMax-M2.7
```

> **Endpoint path is non-standard.** MiniMax uses `/v1/text/chatcompletion_v2` instead of
> `/v1/chat/completions`; the request/response body is still OpenAI-compatible.
>
> Prefer the M2.7 family: MiniMax documents tool calling for M2.7; M2.5 may work but is not
> guaranteed.

---

### Aggregators and other hosts

These also expose an OpenAI-compatible API and work with `KUBEBOLT_AI_PROVIDER=openai`. Their
model catalogs change often, so copy the exact model ID from the provider and enter it with
**Custom…** (or `KUBEBOLT_AI_MODEL`).

| Provider | Base URL | Notes |
|---|---|---|
| **OpenRouter** | `https://openrouter.ai/api/v1/chat/completions` | Routes to many vendors behind one key; model IDs use `vendor/model` |
| **Together AI** | `https://api.together.xyz/v1/chat/completions` | Open-weights models (Llama, Qwen, ...) |
| **Fireworks AI** | `https://api.fireworks.ai/inference/v1/chat/completions` | Open-weights models |
| **Cerebras** | `https://api.cerebras.ai/v1/chat/completions` | Very fast open-weights inference |
| **Perplexity** | `https://api.perplexity.ai/chat/completions` | |

```bash
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=https://openrouter.ai/api/v1/chat/completions
KUBEBOLT_AI_API_KEY=sk-or-...
KUBEBOLT_AI_MODEL=<vendor/model-id>
```

> Cost estimates on the Usage page only cover models in KubeBolt's price table; unknown
> models show no estimate.

---

## Self-Hosted Models

Run a local LLM and point Kobi at it — useful for air-gapped clusters, sensitive data or cost.

> **Tool calling is required.** Kobi fetches every piece of cluster data through
> function/tool calling. The model you self-host **must** support it — see
> [Compatibility Notes](#compatibility-notes).

In all cases, set `KUBEBOLT_AI_MODEL` to the name the server actually serves, and set
`KUBEBOLT_AI_COMPACT_MODEL` (usually the same model, or a smaller one you also serve).

### Ollama

Ollama exposes an OpenAI-compatible API on port 11434.

| Field | Value |
|---|---|
| Base URL | `http://localhost:11434/v1/chat/completions` (or the Ollama Service's in-cluster DNS name) |
| Auth | None — set `KUBEBOLT_AI_API_KEY` to any non-empty string |

```bash
# Pull a model with tool-calling support first, e.g.
ollama pull llama3.1:70b

# Configure KubeBolt
KUBEBOLT_AI_PROVIDER=openai
KUBEBOLT_AI_BASE_URL=http://localhost:11434/v1/chat/completions
KUBEBOLT_AI_API_KEY=ollama
KUBEBOLT_AI_MODEL=llama3.1:70b
KUBEBOLT_AI_COMPACT_MODEL=llama3.1:70b
```

For KubeBolt running in Docker Desktop or Minikube pointing at Ollama on the host machine, use
`http://host.docker.internal:11434/v1/chat/completions`.

---

### vLLM

Production-grade serving for open-weights models.

| Field | Value |
|---|---|
| Base URL | `http://localhost:8000/v1/chat/completions` |
| Auth | Optional — set via `--api-key` when starting vLLM |

```bash
# Start vLLM with tool calling enabled
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

GUI-based local runner with an OpenAI-compatible server.

| Field | Value |
|---|---|
| Base URL | `http://localhost:1234/v1/chat/completions` |
| Auth | None — set `KUBEBOLT_AI_API_KEY` to any non-empty string |

---

### llama.cpp server

| Field | Value |
|---|---|
| Base URL | `http://localhost:8080/v1/chat/completions` |
| Auth | None by default |

---

## Configuration Examples

Helm `values.yaml` examples. `copilot.provider` and `copilot.fallback.provider` take
`anthropic` or `openai`; the fallback provider defaults to the primary's. Keys go in a Secret
with an `api-key` entry (`existingSecret`) or inline (`apiKey`, which the chart turns into a
Secret).

### Example 1: Cloud production with cross-provider fallback

Anthropic as primary, OpenAI as fallback for high availability.

```yaml
# values.yaml
copilot:
  enabled: true
  provider: anthropic
  model: claude-sonnet-5
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

The stronger model serves every request; the cheaper one takes over on rate limits and
outages.

```yaml
copilot:
  enabled: true
  provider: anthropic
  model: claude-opus-5
  existingSecret: anthropic-key
  fallback:
    enabled: true
    provider: anthropic
    model: claude-haiku-4-5
    existingSecret: anthropic-key
```

---

### Example 3: Privacy-first — self-hosted with cloud fallback

A local model for sensitive cluster data; the cloud is used only if the local model fails.

```yaml
copilot:
  enabled: true
  provider: openai
  baseUrl: http://ollama.kubebolt.svc.cluster.local:11434/v1/chat/completions
  model: llama3.1:70b
  apiKey: ollama
  fallback:
    enabled: true
    provider: anthropic
    model: claude-haiku-4-5
    existingSecret: anthropic-key
extraEnv:
  - name: KUBEBOLT_AI_COMPACT_MODEL
    value: llama3.1:70b
```

---

### Example 4: Air-gapped — fully self-hosted, no fallback

```yaml
copilot:
  enabled: true
  provider: openai
  baseUrl: http://vllm.kubebolt.svc.cluster.local:8000/v1/chat/completions
  model: Qwen/Qwen2.5-72B-Instruct
  apiKey: dummy
  # No fallback — traffic never leaves the cluster
extraEnv:
  - name: KUBEBOLT_AI_COMPACT_MODEL
    value: Qwen/Qwen2.5-72B-Instruct
```

---

### Example 5: xAI Grok with an OpenAI fallback

```yaml
copilot:
  enabled: true
  provider: openai
  baseUrl: https://api.x.ai/v1/chat/completions
  model: grok-4.3
  existingSecret: xai-key
  fallback:
    enabled: true
    provider: openai
    model: gpt-4o-mini        # no baseUrl → OpenAI's default endpoint
    existingSecret: openai-key
extraEnv:
  - name: KUBEBOLT_AI_COMPACT_MODEL
    value: grok-4.20-0309-non-reasoning
```

---

### Example 6: Local development with `make dev`

Create a `.env` file in the repo root:

```bash
# .env
KUBEBOLT_AI_PROVIDER=anthropic
KUBEBOLT_AI_API_KEY=sk-ant-api03-...
KUBEBOLT_AI_MODEL=claude-sonnet-5
```

Then run:

```bash
make dev
```

The Makefile sources `.env` and exports the variables to the API process.

---

## Choosing a Model

### Quality matters more than speed?
- **Claude Opus 5** or **Claude Fable 5** — deepest reasoning, slower and pricier
- **Claude Sonnet 5** — Kobi's default, balanced
- **GPT-5.6 Sol / Terra** — strong OpenAI alternatives
- **o3 / o4-mini** — reasoning models for multi-step plans

### Speed matters?
- **Claude Haiku 4.5**
- **GPT-5.6 Luna**, **GPT-4o-mini**, **GPT-5.4 nano**
- **Llama on Groq** — very low latency

### Cost matters?
- **Claude Haiku 4.5** — cheapest in the Anthropic catalog
- **GPT-5.6 Luna**, **GPT-4o-mini** — cheapest OpenAI options
- **DeepSeek Chat**, **Qwen Turbo** — very low per-token prices
- **Self-hosted (Ollama, vLLM)** — pay only for compute

### Privacy matters?
- **Self-hosted** (Ollama, vLLM, LM Studio) — data never leaves your network
- **Azure OpenAI** with private endpoints
- AWS Bedrock and Google Vertex AI have no native adapter; they work only through an
  OpenAI-compatible gateway in front of them

---

## Cost & Token Reference

Approximate token usage per Kobi interaction (input + output combined):

| Question type | Tokens (rough) |
|---|---|
| Simple K8s concept ("what is a pod?") | ~500-1,500 |
| Cluster overview ("what's wrong?") | ~3,000-8,000 |
| Pod troubleshooting (with logs) | ~5,000-15,000 |
| Topology analysis | ~10,000-30,000 |
| Multi-step troubleshooting | ~20,000-50,000 |

List prices from KubeBolt's usage-estimate table (`apps/api/internal/copilot/pricing.go`),
USD per 1M tokens:

| Model | Input | Output |
|---|---|---|
| Claude Fable 5 | $10 | $50 |
| Claude Opus 5 / Opus 4.8 / 4.7 / 4.6 | $5 | $25 |
| Claude Sonnet 5 | $2 | $10 |
| Claude Sonnet 4.6 / 4.5 | $3 | $15 |
| Claude Haiku 4.5 | $1 | $5 |
| GPT-5.6 Sol | $5 | $30 |
| GPT-5.6 Terra | $2 | $12 |
| GPT-5.6 Luna | $0.20 | $1.20 |
| GPT-5.4 | $2.50 | $15 |
| GPT-4.1 | $2 | $8 |
| GPT-4o | $2.50 | $10 |
| GPT-4o-mini | $0.15 | $0.60 |
| o3 | $2 | $8 |
| o4-mini / o3-mini | $1.10 | $4.40 |
| Grok 4.3 / 4.20 | $1.25 | $2.50 |
| DeepSeek Chat | $0.27 | $1.10 |
| DeepSeek Reasoner | $0.55 | $2.19 |
| Qwen Max / Plus / Turbo | $2.80 / $0.80 / $0.30 | $8.40 / $2.00 / $0.60 |
| Llama 3.3 70B (Groq) | $0.59 | $0.79 |
| Mistral Large / Medium / Small | $2 / $0.40 / $0.20 | $6 / $2 / $0.60 |
| MiniMax M2.7 | $0.30 | $1.20 |

Cached input is billed at a discount by most providers, and the Usage page accounts for it.
Always check current pricing on the provider's website — the table is a best-effort snapshot.
KubeBolt sends no usage data to its developers; your bill comes directly from your provider.

---

## Compatibility Notes

### Tool calling support

Kobi **requires** function/tool calling because all cluster data is fetched through tools. A
model without tool calling will not work as the primary model.

| Provider/Model | Tool Calling | Notes |
|---|---|---|
| Claude (catalog models) | ✅ Yes | Native adapter, most reliable |
| GPT-5.x, GPT-4.1, GPT-4o | ✅ Yes | Native, very reliable |
| o3, o3-mini, o4-mini | ✅ Yes | Reasoning models; slower |
| Grok 4.x | ✅ Yes | OpenAI-compatible |
| DeepSeek Chat | ✅ Yes | OpenAI-compatible tool calling |
| Qwen 2.5 72B (self-hosted) | ✅ Yes | Strong tool calling, especially via vLLM |
| Mistral Large | ✅ Yes | Good tool calling |
| Llama 3.1+ 70B | ⚠️ Varies | Depends on the serving stack — Groq, Together and vLLM with `--enable-auto-tool-choice` work well |
| Llama 3.1 8B, Mistral Small/Nemo | ⚠️ Limited | Can struggle with multi-step tool calls |
| MiniMax M2.7 | ✅ Yes | Documented by MiniMax for tool use |
| MiniMax M2.5 | ⚠️ Undocumented | May work, not confirmed |

Some prompted-tool-calling models (e.g. Qwen, DeepSeek-R1) also write `<tool_call>…</tool_call>`
blocks into the message text; the `openai` adapter strips them from the visible answer and
reads the native `tool_calls` field.

### Streaming support

KubeBolt does **not** need streaming from the provider: it makes blocking calls and streams
events to the browser over SSE itself. Any provider that returns a complete response works.

### Context window

Tool results fill context quickly; a multi-step investigation can reach 20–50K tokens. The
window KubeBolt assumes (used for the auto-compact budget) comes from
`apps/api/internal/copilot/budget.go`:

- Claude Fable 5, Opus 5, Sonnet 5, Opus 4.8 / 4.7 / 4.6, Sonnet 4.6: 1M tokens
- Other Claude models (Sonnet 4.5, Haiku 4.5, ...): 200K tokens
- GPT-5.6 line: 1.05M tokens
- Other GPT-5 models: 400K tokens
- GPT-4o / GPT-4.x and any unknown model: 128K tokens

For a model with a smaller real window (e.g. a 32K self-hosted model), set
`KUBEBOLT_AI_SESSION_BUDGET_TOKENS` to its size so compaction fires before the provider
truncates or rejects the request.

---

## Troubleshooting

**The provider returns 404:**
The base URL is missing the chat-completions path (KubeBolt uses it verbatim), or the model is
not available to your account. For Ollama, use the OpenAI-compatible path
`/v1/chat/completions`, not the native `/api/chat`.

**"unknown provider":**
`KUBEBOLT_AI_PROVIDER` (or the fallback provider) must be `anthropic` or `openai`. For
self-hosted and third-party endpoints use `openai` with a base URL.

**"Tool call returned no result" or similar errors with open-weights models:**
The model isn't producing valid tool calls. Try a model with stronger tool support (Llama 3.3
70B, Qwen 2.5 72B) or switch to Claude / GPT.

**vLLM serves the model but tool calling doesn't work:**
Start vLLM with `--enable-auto-tool-choice --tool-call-parser hermes` (or the parser
appropriate for your model — see the vLLM docs).

**Auto-compaction fails on a non-OpenAI endpoint:**
Set `KUBEBOLT_AI_COMPACT_MODEL` to a model that endpoint serves; the default `gpt-4o-mini`
exists only on OpenAI.

**Azure OpenAI returns 401 or 404:**
Check that the deployment name in the URL matches a real deployment, and that the resource
accepts a bearer credential (KubeBolt does not send the `api-key` header).

**OpenRouter charges more than expected:**
OpenRouter adds a small markup on top of provider rates. For pure cost optimization, go direct
to the underlying provider.
