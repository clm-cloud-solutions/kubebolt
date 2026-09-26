# Kobi Copilot — Configuration Guide

KubeBolt is an open-source Kubernetes operations platform, and **Kobi** is its AI SRE. In the
open-source edition Kobi runs as **Kobi Copilot**: an in-app assistant that combines
Kubernetes knowledge with live access to your cluster through KubeBolt's own API. You ask,
Kobi answers — it investigates, explains insights and events, and when a fix is warranted it
**proposes** an action that runs only after you approve it, under your role, and is recorded
in the audit trail.

**Bring your own key (BYOK).** KubeBolt is not a managed AI service in the open-source
edition. You configure your own LLM provider and pay that provider directly; KubeBolt has no
AI billing.

> Kobi Autopilot, the autonomous mode, is available only in KubeBolt Cloud and Enterprise
> Self-Hosted ([kubebolt.io](https://kubebolt.io)). Everything in this guide is Kobi Copilot.

Public documentation: [kubebolt.io/docs/copilot](https://kubebolt.io/docs/copilot) ·
[Enabling Kobi](https://kubebolt.io/docs/enabling-kobi) · [Kobi](https://kubebolt.io/docs/kobi)

## What Kobi can do

Kobi works through **26 tools** that the backend executes on its behalf (the LLM never talks
to your cluster directly).

**17 read tools** — `get_cluster_overview`, `list_resources`, `get_resource_detail`,
`get_resource_yaml`, `get_resource_describe`, `get_pod_logs`, `get_workload_pods`,
`get_workload_history`, `get_cronjob_jobs`, `get_topology`, `get_insights`, `get_events`,
`search_resources`, `get_permissions`, `list_clusters`, `get_workload_metrics`,
`get_kubebolt_docs`.

**9 action proposals** — `propose_restart_workload`, `propose_debug_pod`,
`propose_scale_workload`, `propose_rollback_deployment`, `propose_delete_resource`,
`propose_set_resources`, `propose_set_image`, `propose_set_env`, `propose_patch_hpa`. A
proposal changes nothing by itself; see [Actions and approval](#actions-and-approval).

The same 17 read tools are also available to external MCP hosts (Claude Code, Cursor, CI) —
see [kobi-mcp.md](kobi-mcp.md).

Kobi answers even when **no cluster is connected**: it still talks about itself, KubeBolt and
Kubernetes (and can use `get_kubebolt_docs`), and tells you in its own words that there is no
live cluster to inspect instead of returning an error. On a metrics-only cluster (metrics
are ingested but KubeBolt has no Kubernetes API connection) Kobi still talks, but only
`get_kubebolt_docs` works; the other tools return an error explaining that live cluster
access (a direct API connection or the agent proxy) is needed.

## Quick Start

### 1. Get an API key from your LLM provider

| Provider | Where to get a key |
|---|---|
| **Anthropic Claude** | https://console.anthropic.com/settings/keys |
| **OpenAI** | https://platform.openai.com/api-keys |
| **OpenAI-compatible (xAI, DeepSeek, Groq, Mistral, ...)** | The provider's console |
| **Self-hosted (Ollama, vLLM)** | Any non-empty value (only the endpoint matters) |

> See [copilot-providers.md](copilot-providers.md) for the **complete reference** of supported
> providers, including Azure OpenAI, xAI Grok, DeepSeek, Groq, Mistral, OpenRouter and
> self-hosted models, with configuration examples for each.

### 2. Configure Kobi

You can configure Kobi in the UI (no restart) or through environment variables. When both are
set, the UI values win — see [Configuration precedence](#configuration-precedence).

#### From the UI

As an admin, open **Administration → AI (Kobi) → Configuration** (`/admin/ai`), pick the
provider and model, paste the API key and save. The first-run setup wizard offers the same
form as its **AI Copilot** step. Changes apply to the next chat request without restarting
the API.

This requires authentication to be enabled (`KUBEBOLT_AUTH_ENABLED`, on by default), because
the settings are stored in KubeBolt's BoltDB database.

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

The chart exposes the provider, model, base URL, max tokens and fallback settings under
`copilot.*`. The remaining `KUBEBOLT_AI_*` variables (governance, rounds, compaction,
conversation retention) go through `extraEnv`:

```yaml
extraEnv:
  - name: KUBEBOLT_AI_DESTRUCTIVE_ACTIONS_ENABLED
    value: "false"
  - name: KUBEBOLT_AI_MAX_ROUNDS
    value: "30"
```

#### With Docker Compose

Copy `deploy/.env.example` to `deploy/.env` and fill in:

```bash
cp deploy/.env.example deploy/.env
# Edit deploy/.env and set KUBEBOLT_AI_API_KEY
cd deploy && docker compose up -d --build
```

### 3. Open Kobi

Click the Kobi launcher in the bottom-right corner, or press `⌘J` (`Ctrl+J` on Linux/Windows)
to toggle the panel. The launcher and the shortcut appear only while Kobi is enabled — that
is, while an API key is configured (from the UI or from `KUBEBOLT_AI_API_KEY`).

## Configuration Reference

### Configuration precedence

1. **Environment variables** (`KUBEBOLT_AI_*`) form the baseline, read at startup.
2. **Administration → AI (Kobi) → Configuration** stores partial overrides in BoltDB. Any field
   set there wins over its environment variable; fields left untouched keep following the
   environment.
3. **Reset to env defaults** on that page clears every stored value — including the stored API keys — and
   falls back to the environment only.

API keys saved from the UI are encrypted at rest with a key derived from the JWT secret. If
the JWT secret changes (for example, it was auto-generated and the API restarted), stored keys
can no longer be decrypted and KubeBolt falls back to the environment key — set a stable
`KUBEBOLT_JWT_SECRET` (or `auth.jwtSecret` / `auth.existingSecret` in Helm) and re-enter
the key if that happens.

### Primary provider

| Variable | Default | Description |
|---|---|---|
| `KUBEBOLT_AI_PROVIDER` | `anthropic` | `anthropic` or `openai` (`openai` covers every OpenAI-compatible API) |
| `KUBEBOLT_AI_API_KEY` | — | Your provider API key. **Kobi is disabled when no key is configured** (here or in the UI). |
| `KUBEBOLT_AI_MODEL` | `claude-sonnet-5` (anthropic) / `gpt-4o` (openai) | Model ID sent to the provider |
| `KUBEBOLT_AI_BASE_URL` | provider default | Full endpoint URL, used as-is (e.g. `.../v1/chat/completions`) |
| `KUBEBOLT_AI_MAX_TOKENS` | `4096` | Cap on output tokens per provider call |

### Fallback provider (optional)

When the primary fails with a **recoverable** error — HTTP 429, any 5xx, 404 (model or
endpoint unavailable for your account) or a network error — the backend retries the same
request on the fallback. The fallback can be another provider, a cheaper model from the same
provider, or a self-hosted endpoint.

| Variable | Default | Description |
|---|---|---|
| `KUBEBOLT_AI_FALLBACK_API_KEY` | — | **Setting this enables the fallback** |
| `KUBEBOLT_AI_FALLBACK_PROVIDER` | primary provider | `anthropic` or `openai` |
| `KUBEBOLT_AI_FALLBACK_MODEL` | provider default | Model ID for the fallback |
| `KUBEBOLT_AI_FALLBACK_BASE_URL` | provider default | Full endpoint URL for the fallback |

When the fallback answers, the chat shows an "Answered by the fallback model" badge with its
provider and model, so a misconfigured primary is never silently masked.

### Tool loop, display and action governance

| Variable | Default | Description |
|---|---|---|
| `KUBEBOLT_AI_MAX_ROUNDS` | `20` | Max tool-calling rounds per turn (clamped to 2–40). A round is one model turn that calls one or more tools. When exhausted, Kobi gives a final summary of what it found instead of erroring. Raise it for small models that call tools one at a time. |
| `KUBEBOLT_AI_SHOW_TOOL_CALLS` | `true` | Show a collapsible card for each tool call (name, status, result) in the chat. `false` keeps only the final answer. |
| `KUBEBOLT_AI_ACTIONS_ENABLED` | `true` | Master switch for action proposals. `false` withholds every `propose_*` tool, so Kobi is read-only advisory. |
| `KUBEBOLT_AI_DESTRUCTIVE_ACTIONS_ENABLED` | `true` | `false` withholds `propose_delete_resource` and rejects deletes and scale-to-0 server-side. |
| `KUBEBOLT_AI_ACTION_PROGRESS_TIMEOUT` | `90s` | How long the UI follows an approved action's rollout before declaring it stalled and asking Kobi to investigate. Go duration; minimum `10s`. |

All of these can also be changed from **Administration → AI (Kobi) → Configuration**
(*Max tool steps*, *Tool calls*, *Kobi actions*, *Destructive actions*, *Action timeout*).

### Conversation memory (auto-compact)

| Variable | Default | Description |
|---|---|---|
| `KUBEBOLT_AI_AUTO_COMPACT` | `true` | Master switch for auto-compaction |
| `KUBEBOLT_AI_SESSION_BUDGET_TOKENS` | model context window | Total ceiling; compaction fires at budget × threshold |
| `KUBEBOLT_AI_AUTO_COMPACT_THRESHOLD` | `0.80` | Fraction of the budget at which compaction fires (between 0 and 1) |
| `KUBEBOLT_AI_COMPACT_MODEL` | `claude-haiku-4-5` (anthropic) / `gpt-4o-mini` (openai) | Model used to write the summary |
| `KUBEBOLT_AI_COMPACT_PRESERVE_TURNS` | `3` | Turns kept intact after a fold |

See [Conversation memory](#conversation-memory) for how compaction works.

### Conversation history

| Variable | Default | Description |
|---|---|---|
| `KUBEBOLT_COPILOT_CONVERSATION_RETENTION_HORIZON` | `2160h` (90 days) | Conversations not updated within this window are pruned by the hourly retention pass |
| `KUBEBOLT_COPILOT_CONVERSATION_MAX_PER_USER` | `200` | Per-user cap; the oldest conversations drop out once exceeded |

`KUBEBOLT_AI_DEBUG=1` is a legacy shortcut that forces debug-level logging (same as
`LOG_LEVEL=debug`).

## Actions and approval

Kobi never mutates the cluster on its own. When a fix is warranted, it calls one of the
`propose_*` tools, which renders an **action card** in the chat:

1. **Proposal** — the card shows the action, the target, the parameters and a risk level.
2. **Dry-run preview** — before you decide, the card sends the same request with a
   server-side dry run (`dryRun=All`: full admission — quotas, LimitRanges, webhooks,
   validation — without persisting) and shows whether the cluster would accept it, including
   a quota breakdown when a ResourceQuota would block it. Rollback has no dry-run preview.
3. **Approval** — nothing happens until you click **Execute**. The request goes through the
   same action endpoints the UI buttons use, under **your** KubeBolt role: executing requires
   Editor or Admin, so a Viewer can read a proposal but not run it. On agent-connected
   clusters, the agent's permission tier must also allow writes.
4. **Audit** — every executed action is recorded in the audit trail with source
   `copilot_proposal` (versus `ui` for a direct click), so approved Kobi actions can be told
   apart from manual ones (`GET /api/v1/admin/actions`).
5. **Follow-through** — after execution the card tracks the rollout. If it does not converge
   within `KUBEBOLT_AI_ACTION_PROGRESS_TIMEOUT`, Kobi is asked to investigate why.

Admins can turn proposals off entirely (`KUBEBOLT_AI_ACTIONS_ENABLED=false`) or only the
destructive ones (`KUBEBOLT_AI_DESTRUCTIVE_ACTIONS_ENABLED=false`), from the environment or
from the Configuration page. When a proposal is blocked by that policy, Kobi says so instead
of blaming your role, and offers the equivalent `kubectl` command.

## Recipes

These Helm examples use `values.yaml`. Every provider is either `anthropic` or `openai`; for
self-hosted and third-party OpenAI-compatible endpoints use `openai` with a `baseUrl`.

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
  model: claude-opus-5
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
    provider: openai
    baseUrl: http://ollama.internal:11434/v1/chat/completions
    apiKey: dummy-not-used
    model: llama3.1:70b   # whatever model you pulled into Ollama
```

## Contextual "Ask Kobi"

One-click **Ask Kobi** buttons across the UI open Kobi with a prompt already loaded with the
relevant context — cluster, namespace, resource, symptom, the rows of a panel. You don't have
to copy-paste anything.

| Surface | Where | What Kobi is asked |
|---|---|---|
| Insight cards | Overview and Insights | Diagnose the insight and recommend a fix |
| Resource detail header | Every resource detail page | Investigate the resource; the prompt adapts to the active tab (Monitor, Logs, YAML, ...) |
| Workload health | Overview (not-ready workloads) | Why the workload is not ready |
| Warning events | Events page and the Overview events feed | Explain the Warning event and its impact |
| Traffic flows | Cluster Map edges and external endpoints | Explain the flow, drops and error rates |
| Metric charts | Charts that offer Ask Kobi | Interpret the series against its reference lines |
| Dashboard panels | Top CPU consumers, Right-sizing, Recent deploys, Recent OOM kills, Top workloads by traffic, Error hot-spots, Top latency, Network drops, Cost breakdown | Analyze the panel, or a single row |
| Stalled actions | Action cards (automatic) | Why an approved action did not converge |

Prompt templates live in `apps/web/src/services/copilot/triggers.ts`. Every session records
the `trigger` that started it (`manual`, `insight`, `resource_inquiry`, `not_ready_resource`,
`warning_event`, `flow_edge`, `metric_anomaly`, `panel_inquiry`, `action_stalled`), visible in
the Usage page's session details.

## Conversations

Conversations are personal and persist per user, so you can refresh, sign out and resume
where you left off. The **History** button in the panel header lists your past conversations;
**New conversation** starts a fresh one. Conversation history can be browsed even when the
cluster is unreachable.

Persistence uses KubeBolt's BoltDB database, so it requires authentication to be enabled (the
default). Retention and the per-user cap are set by
`KUBEBOLT_COPILOT_CONVERSATION_RETENTION_HORIZON` and
`KUBEBOLT_COPILOT_CONVERSATION_MAX_PER_USER`.

## Conversation memory

Long sessions would otherwise bleed context: every tool call adds tokens to the next request
until the model's context window overflows. Kobi manages this automatically.

**Auto-compact** triggers when the estimated conversation size crosses
`SESSION_BUDGET_TOKENS × AUTO_COMPACT_THRESHOLD` (default 80% of the model's context window).
Older turns are folded into a single summary written by the cheap-tier model of the same
provider — `claude-haiku-4-5` for Anthropic, `gpt-4o-mini` for OpenAI — and bulky tool results
in the preserved tail are stubbed. The active turn's tool results are always kept intact: the
model never sees a placeholder for data it is still working with.

> **OpenAI-compatible providers:** the automatic cheap-tier pick is `gpt-4o-mini`, which only
> exists on OpenAI itself. If you point `openai` at another endpoint (xAI, Groq, Ollama, ...),
> set `KUBEBOLT_AI_COMPACT_MODEL` to a model that endpoint serves.

**Manual compact** — the Scissors icon in the panel header runs the same primitive over the
entire transcript, collapsing it into a single summary so you can pivot topics without losing
context.

After a compaction the panel shows a banner with the reduction, and the counter at the bottom
of the panel shows the context size the provider reported on the most recent round.

## Scope guardrail

The system prompt defines what Kobi engages with — the connected cluster, Kubernetes,
DevOps/SRE topics that support cluster operations, and KubeBolt itself — and what is out of
scope (general coding unrelated to Kubernetes, non-technical topics, cloud products not
running on Kubernetes, non-technical opinions). Out-of-scope requests get a one-sentence
decline in the user's language with a redirect to something cluster-related, never a partial
answer. This keeps Kobi focused and costs predictable.

## Product knowledge base

`get_kubebolt_docs` is the tool Kobi calls for questions about KubeBolt itself ("how do I
port-forward from the UI?", "what does the Scissors button do?"). It returns a curated, terse
description for a few dozen topics (navigation, admin pages, configuration, keyboard
shortcuts, compaction). Keys are fuzzy-matched, so `pod terminal` and `pod-terminal` both
resolve. It needs no cluster, so it works in the no-cluster state too.

## Usage analytics

**Administration → AI (Kobi) → Usage** (Admin role, authentication enabled) shows:

- **Summary**: sessions, tokens billed, cache hit rate, estimated USD cost, average duration,
  compaction events
- **Timeseries**: cache-read vs. fresh input vs. output tokens over 24h / 7d / 30d
- **Top tools**: calls and error rate per tool — useful to spot tools that return too much or
  fail too often
- **Recent sessions**: every session in the retention window, with a detail view (model,
  trigger, rounds, duration, tokens, estimated cost, tool breakdown, compaction events)

Usage records live in the same BoltDB file as users. Retention: 30 days or 5,000 records,
whichever comes first. The USD figure comes from a best-effort price snapshot in
`apps/api/internal/copilot/pricing.go`; your real bill is whatever your provider charges.

## Privacy & Security

### What data goes to the LLM provider?

- Your chat messages
- The system prompt (Kobi's role and operating guidance)
- Tool results — which contain **cluster data** Kobi fetched through tools

The provider you choose **will see your cluster data** (resource names, status, logs, events,
metrics) when Kobi fetches it. Choose a provider that meets your compliance requirements.

### What KubeBolt does to protect you

- **API keys never reach the browser** — the Go backend proxies every LLM request, and keys
  saved from the UI are encrypted at rest.
- **Secret values are redacted** — Secret data is redacted at the API layer before it is
  returned to anything, Kobi included.
- **Permission awareness** — Kobi's tools read through the same cluster connection as the rest
  of KubeBolt, so they can only see what that kubeconfig, ServiceAccount or agent can access.
- **Human approval for every change** — actions are proposals until you execute them, run
  under your KubeBolt role, and are audited.
- **Sensitive data warnings** — Kobi is instructed to detect potential credentials in pod logs
  and warn you instead of echoing them.
- **No KubeBolt telemetry** — KubeBolt does not send data about your Kobi usage to its
  developers.

### What you should do

- Use a separate API key for KubeBolt (don't reuse a key from another app).
- Store the key in a Kubernetes Secret (`existingSecret`) or in the UI, not inline in
  `values.yaml`.
- Rotate the key periodically: update it in the UI (no restart), or update the Secret and
  restart the API pod.
- For sensitive clusters, consider self-hosted models (Ollama, vLLM) via the base URL.
- Consider `KUBEBOLT_AI_DESTRUCTIVE_ACTIONS_ENABLED=false` where deletes should never be
  proposed.

## Troubleshooting

**The Kobi launcher doesn't appear.**
No API key is configured. Check **Administration → AI (Kobi) → Configuration**, or the API
logs at startup:
```
AI copilot enabled provider=anthropic model=...
```
`AI copilot disabled (KUBEBOLT_AI_API_KEY not set)` means the environment has no key. That
startup line only reflects the environment — a key saved in the UI still enables Kobi.

**Kobi says there is no cluster connected.**
Kobi answers without a cluster, but can't inspect live resources until one is connected and
reachable. Check the cluster status in KubeBolt.

**Chat returns "authentication failed (HTTP 401)" or "403".**
Your provider key is wrong, expired, or not valid for the configured provider. Generate a new
one and update it in the UI or in the Secret.

**Chat returns a 404 from the provider.**
The configured model isn't available for your account, or the base URL is wrong. The base URL
must be the full endpoint (e.g. `https://api.openai.com/v1/chat/completions`) — KubeBolt does
not append a path to it.

**Chat returns "rate limit hit" frequently.**
You're hitting your provider's rate limits. Configure a fallback or raise your plan with the
provider.

**The fallback never triggers.**
It only activates on recoverable errors (429, 5xx, 404, network). Auth errors (401/403) and
other 4xx validation errors go straight to the user. Check the API logs for the actual error.

**Kobi stops mid-investigation with a summary.**
It hit the tool-round budget. Raise `KUBEBOLT_AI_MAX_ROUNDS` (up to 40) or *Max tool steps* in
the Configuration page.

**Execute on an action card fails.**
The card explains why: your role is too low (ask an Editor or Admin), the action-governance
policy blocks it (an admin can change it in the Configuration page), the agent is in
read-only mode, or the target no longer exists.

## Disabling Kobi

Remove the key from both places Kobi reads it:

1. In **Administration → AI (Kobi) → Configuration**, click **Reset to env defaults** (clears stored keys and
   overrides) if a key was saved from the UI.
2. Unset the environment key — with Helm:

   ```bash
   helm upgrade kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
     --reuse-values --set copilot.enabled=false
   ```

With no key left, the launcher and `⌘J` disappear from the UI. To keep Kobi but make it
read-only, set `KUBEBOLT_AI_ACTIONS_ENABLED=false` instead.

## See Also

- **[copilot-providers.md](copilot-providers.md)** — Supported LLM providers, endpoint URLs,
  models, configuration examples, costs and compatibility notes.
- **[kobi-mcp.md](kobi-mcp.md)** — Use Kobi's read-only tools from Claude Code, Cursor or any
  MCP host.
- [kubebolt.io/docs/copilot](https://kubebolt.io/docs/copilot) ·
  [kubebolt.io/docs/enabling-kobi](https://kubebolt.io/docs/enabling-kobi) ·
  [kubebolt.io/docs/kobi](https://kubebolt.io/docs/kobi)
