# KubeBolt Copilot — Integration Guide

How to implement the AI copilot chatbot inside KubeBolt's React frontend with configurable
LLM providers and KubeBolt API as the data source.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│  KubeBolt Frontend (React)                                  │
│                                                             │
│  ┌─────────────────────────┐  ┌──────────────────────────┐  │
│  │  Existing UI            │  │  CopilotPanel            │  │
│  │  (Dashboard, Resources, │  │  ├─ MessageList           │  │
│  │   Map, etc.)            │  │  ├─ MessageInput          │  │
│  │                         │  │  ├─ ToolCallIndicator     │  │
│  │                         │  │  └─ ContextBadges         │  │
│  └─────────────────────────┘  └────────┬─────────────────┘  │
│                                        │                     │
│  ┌─────────────────────────────────────┼─────────────────┐  │
│  │  CopilotProvider (React Context)    │                 │  │
│  │  ├─ useCopilot() hook               │                 │  │
│  │  ├─ Chat history state              │                 │  │
│  │  ├─ Tool execution engine    ◄──────┘                 │  │
│  │  └─ Provider adapter                                  │  │
│  └──────────┬──────────────────────────┬─────────────────┘  │
│             │                          │                     │
│             ▼                          ▼                     │
│  ┌──────────────────┐      ┌────────────────────────┐       │
│  │  LLM Provider    │      │  KubeBolt API Client   │       │
│  │  (Claude/OpenAI/ │      │  (existing api.ts)     │       │
│  │   configurable)  │      │                        │       │
│  └──────────────────┘      └────────────────────────┘       │
└─────────────────────────────────────────────────────────────┘
```

## Component Structure

```
src/
├── components/copilot/
│   ├── CopilotPanel.tsx          # Main panel (slide-out or floating)
│   ├── CopilotToggle.tsx         # FAB button to open/close
│   ├── MessageList.tsx           # Chat message rendering
│   ├── MessageBubble.tsx         # Individual message with markdown
│   ├── MessageInput.tsx          # Text input with send button
│   ├── ToolCallIndicator.tsx     # "Fetching pod metrics..." loading state
│   ├── ContextBadges.tsx         # Shows current cluster/namespace context
│   ├── InsightCard.tsx           # Rendered insight within chat
│   └── ResourceLink.tsx          # Clickable link to KubeBolt resource view
├── contexts/
│   └── CopilotContext.tsx        # Provider + useCopilot hook
├── services/
│   └── copilot/
│       ├── providers.ts          # LLM provider adapters
│       ├── tools.ts              # Tool definitions + executor
│       └── types.ts              # Copilot-specific types
└── hooks/
    └── useCopilotTools.ts        # Tool execution via existing API client
```

## LLM Provider Abstraction

The copilot supports multiple LLM providers through a common adapter interface.
The active provider is configurable via settings (stored in localStorage alongside theme).

```typescript
// services/copilot/types.ts

interface CopilotMessage {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  toolCalls?: ToolCall[];
  toolResults?: ToolResult[];
  timestamp: Date;
}

interface ToolCall {
  id: string;
  name: string;
  arguments: Record<string, unknown>;
}

interface ToolResult {
  toolCallId: string;
  name: string;
  content: string;
  isError?: boolean;
}

interface CopilotConfig {
  provider: 'anthropic' | 'openai' | 'custom';
  apiKey: string;
  model: string;
  baseUrl?: string;  // For custom/self-hosted providers
  maxTokens?: number;
}

interface LLMProvider {
  name: string;
  sendMessage(
    messages: CopilotMessage[],
    tools: ToolDefinition[],
    systemPrompt: string,
    config: CopilotConfig
  ): AsyncGenerator<StreamChunk>;
}

interface ToolDefinition {
  name: string;
  description: string;
  parameters: JSONSchema;
}

interface StreamChunk {
  type: 'text' | 'tool_call' | 'tool_call_done' | 'done' | 'error';
  text?: string;
  toolCall?: ToolCall;
  error?: string;
}
```

```typescript
// services/copilot/providers.ts

// Anthropic Claude adapter
class AnthropicProvider implements LLMProvider {
  name = 'anthropic';

  async *sendMessage(messages, tools, systemPrompt, config) {
    const response = await fetch(`${config.baseUrl || 'https://api.anthropic.com'}/v1/messages`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'x-api-key': config.apiKey,
        'anthropic-version': '2023-06-01',
        'anthropic-dangerous-direct-browser-access': 'true',
      },
      body: JSON.stringify({
        model: config.model || 'claude-sonnet-4-6',
        max_tokens: config.maxTokens || 4096,
        system: systemPrompt,
        messages: this.formatMessages(messages),
        tools: this.formatTools(tools),
        stream: true,
      }),
    });
    // Parse SSE stream and yield StreamChunks
    yield* this.parseAnthropicStream(response);
  }
}

// OpenAI adapter
class OpenAIProvider implements LLMProvider {
  name = 'openai';

  async *sendMessage(messages, tools, systemPrompt, config) {
    const response = await fetch(`${config.baseUrl || 'https://api.openai.com'}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${config.apiKey}`,
      },
      body: JSON.stringify({
        model: config.model || 'gpt-4o',
        messages: [{ role: 'system', content: systemPrompt }, ...this.formatMessages(messages)],
        tools: this.formatTools(tools),
        stream: true,
      }),
    });
    yield* this.parseOpenAIStream(response);
  }
}

// Provider registry
const providers: Record<string, LLMProvider> = {
  anthropic: new AnthropicProvider(),
  openai: new OpenAIProvider(),
};

export function getProvider(name: string): LLMProvider {
  return providers[name] || providers.anthropic;
}
```

## API Key Model (BYO Key)

KubeBolt does **NOT** ship with a managed AI service. The copilot uses the **user's own API key**
from their chosen LLM provider (Anthropic, OpenAI, etc.). This means:

- **No KubeBolt-side billing** — The user pays the LLM provider directly.
- **Privacy** — Cluster data only goes to the provider the user explicitly configured.
- **Choice** — Users can pick any provider that fits their compliance/cost needs.
- **Self-hosted** — Users can point at a self-hosted LLM (Ollama, vLLM, etc.) via `baseUrl`.

### Configuration Options

The user provides their API key via one of two mechanisms:

#### Option 1 — Environment variables (Helm / production)

When KubeBolt is deployed via Helm or Docker Compose, the API key is provided via env vars
read by the Go backend. The backend proxies LLM requests so the key never reaches the browser.

| Env Var | Required | Description |
|---|---|---|
| `KUBEBOLT_AI_PROVIDER` | Yes | `anthropic`, `openai`, or `custom` |
| `KUBEBOLT_AI_API_KEY` | Yes | The user's API key for the chosen provider |
| `KUBEBOLT_AI_MODEL` | No | Model name (defaults: `claude-sonnet-4-6` / `gpt-4o`) |
| `KUBEBOLT_AI_BASE_URL` | No | Custom endpoint (for self-hosted or proxy) |
| `KUBEBOLT_AI_MAX_TOKENS` | No | Max tokens per response (default: 4096) |
| `KUBEBOLT_AI_FALLBACK_PROVIDER` | No | Fallback provider when primary fails |
| `KUBEBOLT_AI_FALLBACK_API_KEY` | No | API key for the fallback provider |
| `KUBEBOLT_AI_FALLBACK_MODEL` | No | Model name for the fallback (defaults to provider default) |
| `KUBEBOLT_AI_FALLBACK_BASE_URL` | No | Custom endpoint for the fallback |

**If `KUBEBOLT_AI_API_KEY` is unset, the copilot is disabled** — the chat panel shows a message
explaining how to enable it. KubeBolt itself works fully without it.

### Fallback Model Behavior

When `KUBEBOLT_AI_FALLBACK_*` is configured, the backend automatically retries with the fallback
provider if the primary one fails with a recoverable error:

| Primary failure | Fallback triggered? |
|---|---|
| **429** (rate limit) | Yes |
| **503** (service unavailable) | Yes |
| **502 / 504** (gateway / timeout) | Yes |
| **5xx other** | Yes |
| **Network error / DNS failure** | Yes |
| **400** (bad request — usually a code bug) | No — propagates to user |
| **401 / 403** (auth — wrong API key) | No — propagates to user |

The fallback only activates when configured. If only the primary is set and it fails, the user
sees the original error.

The fallback can use a **different provider entirely** (e.g., primary Anthropic, fallback OpenAI),
the **same provider with a different model** (e.g., primary Claude Opus, fallback Claude Haiku),
or the **same provider/model with a different endpoint** (e.g., primary cloud, fallback self-hosted).

When the fallback is used, the response includes a small badge in the UI ("via fallback model")
so the user knows the answer came from a different model.

#### Option 2 — User settings (local dev / single user)

For local dev or single-user deployments, the user can also enter their API key in the
KubeBolt Settings UI. The key is stored in `localStorage` (browser-side) and used directly
when calling the LLM provider from the frontend.

This mode is suitable for personal use but not recommended for shared deployments since
the key lives in the browser.

### Backend Proxy (Recommended)

For Helm / Docker Compose / production deployments, the backend proxies LLM requests:

```
Browser → POST /api/v1/copilot/chat → KubeBolt Go backend → LLM Provider API
```

The backend reads `KUBEBOLT_AI_*` env vars and forwards the request with the configured key.
The frontend never sees the API key. The response is streamed back via SSE.

### Helm Chart Values

Add these to `deploy/helm/kubebolt/values.yaml`:

```yaml
# AI Copilot configuration
# The copilot uses the user's own API key — no KubeBolt-managed AI service.
# Leave api.key empty to disable the copilot entirely.
copilot:
  enabled: false
  provider: anthropic     # anthropic | openai | custom
  model: ""               # optional — uses provider default if empty
  baseUrl: ""             # optional — for custom/self-hosted endpoints
  maxTokens: 4096
  # API key — recommended to set via existingSecret rather than apiKey
  apiKey: ""
  existingSecret: ""      # name of an existing Secret containing 'api-key'

  # Optional fallback model — used when the primary fails (rate limits,
  # 5xx errors, network issues). Can be a different provider or just a
  # cheaper/smaller model from the same provider.
  fallback:
    enabled: false
    provider: ""          # anthropic | openai | custom (defaults to primary)
    model: ""             # required if fallback.enabled is true
    baseUrl: ""
    apiKey: ""
    existingSecret: ""    # name of Secret containing 'api-key' for fallback
```

The deployment template injects these as env vars on the API container:

```yaml
env:
  - name: KUBEBOLT_AI_PROVIDER
    value: {{ .Values.copilot.provider }}
  - name: KUBEBOLT_AI_MODEL
    value: {{ .Values.copilot.model }}
  - name: KUBEBOLT_AI_BASE_URL
    value: {{ .Values.copilot.baseUrl }}
  - name: KUBEBOLT_AI_MAX_TOKENS
    value: {{ .Values.copilot.maxTokens | quote }}
  {{- if .Values.copilot.existingSecret }}
  - name: KUBEBOLT_AI_API_KEY
    valueFrom:
      secretKeyRef:
        name: {{ .Values.copilot.existingSecret }}
        key: api-key
  {{- else if .Values.copilot.apiKey }}
  - name: KUBEBOLT_AI_API_KEY
    valueFrom:
      secretKeyRef:
        name: {{ include "kubebolt.fullname" . }}-copilot
        key: api-key
  {{- end }}
  {{- if .Values.copilot.fallback.enabled }}
  - name: KUBEBOLT_AI_FALLBACK_PROVIDER
    value: {{ .Values.copilot.fallback.provider | default .Values.copilot.provider }}
  - name: KUBEBOLT_AI_FALLBACK_MODEL
    value: {{ .Values.copilot.fallback.model }}
  - name: KUBEBOLT_AI_FALLBACK_BASE_URL
    value: {{ .Values.copilot.fallback.baseUrl }}
  {{- if .Values.copilot.fallback.existingSecret }}
  - name: KUBEBOLT_AI_FALLBACK_API_KEY
    valueFrom:
      secretKeyRef:
        name: {{ .Values.copilot.fallback.existingSecret }}
        key: api-key
  {{- else if .Values.copilot.fallback.apiKey }}
  - name: KUBEBOLT_AI_FALLBACK_API_KEY
    valueFrom:
      secretKeyRef:
        name: {{ include "kubebolt.fullname" . }}-copilot-fallback
        key: api-key
  {{- end }}
  {{- end }}
```

When `copilot.apiKey` is set inline (not recommended for production), the chart creates a
Secret automatically. Production deployments should use `existingSecret` so the key is
managed outside the Helm release (via Sealed Secrets, External Secrets, Vault, etc.).

### Docker Compose Configuration

For local Docker Compose, add to `deploy/docker-compose.yml`:

```yaml
services:
  api:
    environment:
      - KUBEBOLT_AI_PROVIDER=${KUBEBOLT_AI_PROVIDER:-anthropic}
      - KUBEBOLT_AI_API_KEY=${KUBEBOLT_AI_API_KEY:-}
      - KUBEBOLT_AI_MODEL=${KUBEBOLT_AI_MODEL:-}
      - KUBEBOLT_AI_BASE_URL=${KUBEBOLT_AI_BASE_URL:-}
```

The user provides values via a `.env` file or shell environment.

### Backend Capability Endpoint

The frontend needs to know if the copilot is configured and enabled. Add a new endpoint:

```
GET /api/v1/copilot/config
```

Returns:
```json
{
  "enabled": true,
  "provider": "anthropic",
  "model": "claude-sonnet-4-6",
  "proxyMode": true,
  "fallback": {
    "provider": "openai",
    "model": "gpt-4o-mini"
  }
}
```

If `enabled: false`, the frontend hides the copilot panel or shows a "configure your API key"
message. The actual API keys are never returned to the browser. The `fallback` field is only
present when a fallback model is configured.

## Tool Definitions

Map each KubeBolt API endpoint to an LLM tool definition. The tool executor
calls the existing `api.ts` client functions.

```typescript
// services/copilot/tools.ts

import { api } from '@/services/api';

export const copilotTools: ToolDefinition[] = [
  {
    name: 'get_cluster_overview',
    description: 'Get cluster summary: resource counts, CPU/memory usage, health score, recent events, namespace workloads',
    parameters: { type: 'object', properties: {}, required: [] },
  },
  {
    name: 'list_resources',
    description: 'List Kubernetes resources by type with optional filtering. Types: pods, deployments, statefulsets, daemonsets, jobs, cronjobs, services, ingresses, gateways, httproutes, endpoints, pvcs, pvs, storageclasses, configmaps, secrets, hpas, nodes, namespaces, events',
    parameters: {
      type: 'object',
      properties: {
        type: { type: 'string', description: 'Resource type' },
        namespace: { type: 'string', description: 'Filter by namespace' },
        search: { type: 'string', description: 'Search by name' },
        status: { type: 'string', description: 'Filter by status' },
        sort: { type: 'string', description: 'Sort field' },
        order: { type: 'string', enum: ['asc', 'desc'] },
        page: { type: 'number', description: 'Page number (default 1)' },
        limit: { type: 'number', description: 'Page size (default 50)' },
      },
      required: ['type'],
    },
  },
  {
    name: 'get_resource_detail',
    description: 'Get full details of a specific resource including live metrics',
    parameters: {
      type: 'object',
      properties: {
        type: { type: 'string' },
        namespace: { type: 'string', description: 'Use _ for cluster-scoped resources' },
        name: { type: 'string' },
      },
      required: ['type', 'namespace', 'name'],
    },
  },
  {
    name: 'get_resource_yaml',
    description: 'Get raw YAML of a resource (secrets are redacted automatically). Use when the user wants to see the full spec or check annotations/labels.',
    parameters: {
      type: 'object',
      properties: {
        type: { type: 'string' },
        namespace: { type: 'string', description: 'Use _ for cluster-scoped resources' },
        name: { type: 'string' },
      },
      required: ['type', 'namespace', 'name'],
    },
  },
  {
    name: 'get_resource_describe',
    description: 'Get kubectl describe output for any resource. Includes events, conditions, and detailed status. Best tool for troubleshooting scheduling issues, pending pods, and resource conditions.',
    parameters: {
      type: 'object',
      properties: {
        type: { type: 'string' },
        namespace: { type: 'string', description: 'Use _ for cluster-scoped resources' },
        name: { type: 'string' },
      },
      required: ['type', 'namespace', 'name'],
    },
  },
  {
    name: 'get_pod_logs',
    description: 'Get logs from a pod. Use to diagnose crashes, errors, and application issues',
    parameters: {
      type: 'object',
      properties: {
        namespace: { type: 'string' },
        name: { type: 'string' },
        container: { type: 'string', description: 'Container name (required for multi-container pods)' },
        tailLines: { type: 'number', description: 'Lines from end: 100, 500, or 1000' },
      },
      required: ['namespace', 'name'],
    },
  },
  {
    name: 'get_workload_pods',
    description: 'List pods owned by a workload (deployment, statefulset, daemonset, or job)',
    parameters: {
      type: 'object',
      properties: {
        type: { type: 'string', enum: ['deployments', 'statefulsets', 'daemonsets', 'jobs'] },
        namespace: { type: 'string' },
        name: { type: 'string' },
      },
      required: ['type', 'namespace', 'name'],
    },
  },
  {
    name: 'get_workload_history',
    description: 'Get revision history of a workload (Deployment, StatefulSet, or DaemonSet). Use when investigating recent changes or rollouts.',
    parameters: {
      type: 'object',
      properties: {
        type: { type: 'string', enum: ['deployments', 'statefulsets', 'daemonsets'] },
        namespace: { type: 'string' },
        name: { type: 'string' },
      },
      required: ['type', 'namespace', 'name'],
    },
  },
  {
    name: 'get_cronjob_jobs',
    description: 'List Job children of a CronJob to investigate execution history and failures',
    parameters: {
      type: 'object',
      properties: {
        namespace: { type: 'string' },
        name: { type: 'string' },
      },
      required: ['namespace', 'name'],
    },
  },
  {
    name: 'get_metrics',
    description: 'Get detailed CPU/memory metrics for a specific resource',
    parameters: {
      type: 'object',
      properties: {
        type: { type: 'string' },
        namespace: { type: 'string', description: 'Use _ for cluster-scoped resources' },
        name: { type: 'string' },
      },
      required: ['type', 'namespace', 'name'],
    },
  },
  {
    name: 'get_topology',
    description: 'Get the full cluster topology graph showing relationships between all resources',
    parameters: { type: 'object', properties: {}, required: [] },
  },
  {
    name: 'get_insights',
    description: 'Get active insights (issues detected by KubeBolt) with severity and recommendations',
    parameters: {
      type: 'object',
      properties: {
        severity: { type: 'string', enum: ['critical', 'warning', 'info'] },
        resolved: { type: 'boolean', description: 'Include resolved insights (default false)' },
      },
      required: [],
    },
  },
  {
    name: 'get_events',
    description: 'Get Kubernetes events, optionally filtered by type, namespace, or involved resource',
    parameters: {
      type: 'object',
      properties: {
        type: { type: 'string', enum: ['Normal', 'Warning'] },
        namespace: { type: 'string' },
        involvedName: { type: 'string' },
        involvedKind: { type: 'string' },
        limit: { type: 'number' },
      },
      required: [],
    },
  },
  {
    name: 'search_resources',
    description: 'Global search across all resource types by name. Use when the user mentions a name without specifying the resource type.',
    parameters: {
      type: 'object',
      properties: {
        q: { type: 'string', description: 'Search query (substring match)' },
      },
      required: ['q'],
    },
  },
  {
    name: 'get_permissions',
    description: 'Get RBAC permissions detected for the current kubeconfig connection',
    parameters: { type: 'object', properties: {}, required: [] },
  },
  {
    name: 'list_clusters',
    description: 'List all available kubeconfig contexts (clusters)',
    parameters: { type: 'object', properties: {}, required: [] },
  },
];

// Workload pods dispatcher — maps a workload type to the correct API method
async function fetchWorkloadPods(type: string, namespace: string, name: string) {
  switch (type) {
    case 'deployments':  return api.getDeploymentPods(namespace, name);
    case 'statefulsets': return api.getStatefulSetPods(namespace, name);
    case 'daemonsets':   return api.getDaemonSetPods(namespace, name);
    case 'jobs':         return api.getJobPods(namespace, name);
    default: throw new Error(`Unsupported workload type for pods: ${type}`);
  }
}

// Tool executor — calls existing API client (api from services/api.ts)
export async function executeTool(name: string, args: Record<string, unknown>): Promise<string> {
  try {
    let result: unknown;
    switch (name) {
      case 'get_cluster_overview':
        result = await api.getOverview();
        break;
      case 'list_resources':
        result = await api.getResources(args.type as string, {
          namespace: args.namespace as string | undefined,
          search: args.search as string | undefined,
          status: args.status as string | undefined,
          sort: args.sort as string | undefined,
          order: args.order as 'asc' | 'desc' | undefined,
          page: args.page as number | undefined,
          limit: args.limit as number | undefined,
        });
        break;
      case 'get_resource_detail':
        result = await api.getResourceDetail(
          args.type as string,
          args.namespace as string,
          args.name as string,
        );
        break;
      case 'get_resource_yaml':
        result = await api.getResourceYAML(
          args.type as string,
          args.namespace as string,
          args.name as string,
        );
        break;
      case 'get_resource_describe':
        result = await api.getResourceDescribe(
          args.type as string,
          args.namespace as string,
          args.name as string,
        );
        break;
      case 'get_pod_logs':
        result = await api.getPodLogs(
          args.namespace as string,
          args.name as string,
          args.container as string | undefined,
          args.tailLines as number | undefined,
        );
        break;
      case 'get_workload_pods':
        result = await fetchWorkloadPods(
          args.type as string,
          args.namespace as string,
          args.name as string,
        );
        break;
      case 'get_workload_history':
        result = await api.getWorkloadHistory(
          args.type as string,
          args.namespace as string,
          args.name as string,
        );
        break;
      case 'get_cronjob_jobs':
        result = await api.getCronJobJobs(args.namespace as string, args.name as string);
        break;
      case 'get_metrics':
        result = await api.getMetrics(
          args.type as string,
          args.namespace as string,
          args.name as string,
        );
        break;
      case 'get_topology':
        result = await api.getTopology();
        break;
      case 'get_insights':
        result = await api.getInsights({
          severity: args.severity as string | undefined,
          resolved: args.resolved as boolean | undefined,
        });
        break;
      case 'get_events':
        result = await api.getEvents(args as Record<string, string>);
        break;
      case 'search_resources':
        result = await api.search(args.q as string);
        break;
      case 'get_permissions':
        result = await api.getPermissions();
        break;
      case 'list_clusters':
        result = await api.listClusters();
        break;
      default:
        return JSON.stringify({ error: `Unknown tool: ${name}` });
    }
    return JSON.stringify(result);
  } catch (error) {
    const msg = error instanceof Error ? error.message : 'Unknown error';
    return JSON.stringify({ error: msg });
  }
}
```

> **Note on existing API methods**: All methods referenced in `executeTool` already exist in
> `apps/web/src/services/api.ts`. The copilot implementation reuses the existing API client —
> no new wrappers needed.

## System Prompt

The system prompt sent to the LLM on every request. It establishes the copilot's
role and provides context about the connected cluster.

```typescript
export function buildSystemPrompt(clusterName: string, currentPath: string): string {
  return `You are the KubeBolt AI Copilot — an expert Kubernetes assistant embedded in KubeBolt's monitoring UI.

You have access to tools that fetch real-time data from the user's connected Kubernetes cluster "${clusterName}" via KubeBolt's API.

## Your capabilities
- Fetch and analyze any Kubernetes resource (pods, deployments, services, nodes, etc.)
- Read pod logs to diagnose issues
- Check cluster health and active insights (issues detected by KubeBolt)
- Analyze cluster topology and resource relationships
- Explain Kubernetes concepts in the context of the user's actual cluster

## Language
**Always respond in the same language the user writes in.** If they write in Spanish, respond in Spanish. If English, English. Switch with them if they switch mid-conversation. Technical terms (Deployment, Pod, kubectl, etc.) stay in English regardless.

## Response style
- Be concise and action-oriented. Lead with the answer or diagnosis.
- Format resource references as namespace/name (e.g., production/api-server).
- Format metrics helpfully: "450Mi (72% of 625Mi limit)" not raw bytes.
- Use markdown: code blocks for commands (with language tag), tables for comparisons, bold for key values.
- All Kubernetes timestamps are UTC — mention "UTC" or use relative time ("3 minutes ago").

## Tool usage
- When you need cluster data, call the appropriate tool — don't guess.
- Be efficient: prefer narrow queries (limit, namespace filter) over fetching everything.
- Cap yourself at 3-4 tool calls per user message. If you need more, ask the user to narrow the question.
- Never paste raw JSON to the user — extract and summarize the relevant fields.
- Don't retry the same failed tool call more than once.

## Troubleshooting methodology
Follow: Identify → Gather data → Correlate → Diagnose → Recommend.

## Error handling
- **403** (Forbidden): Acknowledge the permission gap, work with what's accessible, don't retry.
- **404** (Not Found): The resource may have been deleted. Suggest checking events or listing similar resources.
- **503** (Service Unavailable): The cluster connection is unavailable. Suggest the user check the connection status.
- **500/timeout**: Apologize, retry once at most, then explain the limitation.

## Destructive command warnings
You only READ data. But when recommending kubectl commands, ALWAYS warn before destructive ones:
- \`kubectl delete\`, \`--force\`, \`--grace-period=0\`, \`drain\`, \`rollout undo\`, \`scale --replicas=0\`
- Use a ⚠️ marker and explain the consequences before showing the command.
- Suggest \`--dry-run=server\` first when applicable.
- Prefer the safest alternative: \`rollout restart\` over \`delete pod\`, \`scale\` over \`delete deployment\`.

## Privacy
Logs may contain sensitive data. Never echo verbatim strings that look like API keys, tokens, passwords, or DSNs with credentials. Redact when quoting (\`Bearer [REDACTED]\`). Warn the user if you detect potential credentials in logs.

## What you cannot do
- You cannot execute kubectl commands — only read data through KubeBolt's API.
- You cannot modify cluster resources — recommend commands the user can run.
- You don't have historical metrics — only point-in-time data from Metrics Server.
- You cannot read Secret values — KubeBolt redacts them by design.

## Current context
- The user is currently viewing: ${currentPath}
- Use this to provide contextually relevant responses (e.g., if they're on /deployments, they may be asking about a deployment they see).
`;
}
```

## React Context & Hook

```typescript
// contexts/CopilotContext.tsx

interface CopilotState {
  messages: CopilotMessage[];
  isOpen: boolean;
  isLoading: boolean;
  error: string | null;
  config: CopilotConfig;
}

interface CopilotContextValue extends CopilotState {
  sendMessage: (text: string) => Promise<void>;
  togglePanel: () => void;
  clearHistory: () => void;
  updateConfig: (config: Partial<CopilotConfig>) => void;
}

// The provider wraps the app (in App.tsx alongside QueryClientProvider)
// It manages chat state, streams LLM responses, and executes tool calls
// in a loop until the LLM produces a final text response.
```

## Tool Call Loop

The core interaction loop handles multi-step tool calling:

```
1. User sends message
2. Append to messages, send to LLM with tools
3. LLM responds:
   a. If text → display to user, done
   b. If tool_call → execute tool via API client → append result → go to 2
4. Show "Fetching [resource]..." indicator during tool execution
5. Max 10 tool call rounds to prevent infinite loops
```

## UI Design Notes

### Panel Layout
- Slide-out panel from right side (like Intercom/Zendesk)
- FAB button in bottom-right corner with KubeBolt bolt icon
- Panel width: 420px (doesn't overlap with sidebar)
- Respects existing theme (dark/light via `--kb-*` CSS variables)

### Message Rendering
- User messages: right-aligned, `--kb-elevated` background
- Assistant messages: left-aligned, `--kb-card` background
- Tool calls: collapsed indicator ("Checked cluster overview", "Fetched pod logs for api-server")
- Markdown rendering for assistant messages (code blocks, lists, bold)
- ResourceLink component: clickable links that navigate to KubeBolt resource views

### Context Awareness
- Show current cluster name in panel header
- Show active namespace filter if applicable
- When user is viewing a resource detail page, pre-populate context
  (e.g., "I see you're looking at deployment api-server in production")

### Settings

The Settings panel adapts based on whether the backend has a configured API key:

**Backend-managed mode (production / Helm)**
When `KUBEBOLT_AI_API_KEY` is set on the backend, the Settings panel only shows:
- Read-only display of provider and model
- "Configured by administrator" notice
- No API key input — keys are managed via env vars / Kubernetes Secrets

**Browser-managed mode (local dev / single user)**
When no backend key is configured, the Settings panel allows the user to enter their key:
- Provider selector (Anthropic / OpenAI / Custom)
- API key input (stored in `localStorage`)
- Model selector (per provider)
- Custom base URL (for self-hosted endpoints)
- A clear warning: "Your API key is stored in this browser only. For shared deployments, configure it on the backend instead."

The frontend determines the mode by calling `GET /api/v1/copilot/config` on app load.

## WebSocket Integration

The copilot can optionally subscribe to KubeBolt's WebSocket (`/api/v1/ws`) for real-time context.
This is a **Phase 2 enhancement** — the initial implementation uses on-demand API calls only.

### Available Events

These are the event types broadcast by the KubeBolt backend (`apps/api/internal/websocket/events.go`):

| Event Type | When fired | Payload |
|---|---|---|
| `resource:updated` | Any K8s resource changes (pod status, deployment scale, etc.) | The full resource object |
| `resource:deleted` | A resource is removed from the cluster | The deleted resource object |
| `event:new` | A new Kubernetes event is recorded | The event object |
| `insight:new` | Insights engine detects a new issue | The insight object |
| `insight:resolved` | A previously active insight is no longer detected | The resolved insight |
| `metrics:refresh` | Metrics collector polled new data | Metadata only (timestamp) |

The frontend also receives an internal event when the user switches clusters via
`POST /api/v1/clusters/switch`:

| Event Type | When fired | Payload |
|---|---|---|
| `cluster.switched` | User changed active context | `{ context: "new-context-name" }` |

### Phase 2 Enhancements (Pending)

When the copilot subscribes to WebSocket events, it can:

- **Proactive context** — When a `resource:updated` event arrives for a resource discussed in
  the current conversation, note "The pod we were just looking at restarted again."
- **Insight notifications** — When `insight:new` fires for a Critical issue, offer to investigate.
- **Auto-refresh stale data** — Invalidate cached tool results when the underlying resource changes.
- **Reset on cluster switch** — Clear conversation context when `cluster.switched` arrives, since
  the previous cluster's resources are no longer relevant.

## Go Backend Implementation

The backend reads the user's API key from environment variables and proxies LLM requests
so the key never reaches the browser.

### Configuration loader

```go
// internal/config/copilot.go

package config

import (
    "os"
    "strconv"
)

type ProviderConfig struct {
    Provider string  // "anthropic" | "openai" | "custom"
    APIKey   string  // never exposed to the frontend
    Model    string  // optional, provider default if empty
    BaseURL  string  // optional, for custom/self-hosted endpoints
}

type CopilotConfig struct {
    Enabled   bool
    Primary   ProviderConfig
    Fallback  *ProviderConfig // nil if not configured
    MaxTokens int             // default 4096
}

func LoadCopilotConfig() CopilotConfig {
    cfg := CopilotConfig{
        Primary: ProviderConfig{
            Provider: getEnvOr("KUBEBOLT_AI_PROVIDER", "anthropic"),
            APIKey:   os.Getenv("KUBEBOLT_AI_API_KEY"),
            Model:    os.Getenv("KUBEBOLT_AI_MODEL"),
            BaseURL:  os.Getenv("KUBEBOLT_AI_BASE_URL"),
        },
        MaxTokens: 4096,
    }
    if v := os.Getenv("KUBEBOLT_AI_MAX_TOKENS"); v != "" {
        if n, err := strconv.Atoi(v); err == nil {
            cfg.MaxTokens = n
        }
    }
    // Optional fallback
    if fbKey := os.Getenv("KUBEBOLT_AI_FALLBACK_API_KEY"); fbKey != "" {
        cfg.Fallback = &ProviderConfig{
            Provider: getEnvOr("KUBEBOLT_AI_FALLBACK_PROVIDER", cfg.Primary.Provider),
            APIKey:   fbKey,
            Model:    os.Getenv("KUBEBOLT_AI_FALLBACK_MODEL"),
            BaseURL:  os.Getenv("KUBEBOLT_AI_FALLBACK_BASE_URL"),
        }
    }
    cfg.Enabled = cfg.Primary.APIKey != ""
    return cfg
}

func getEnvOr(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}
```

### Chat handler

```go
// internal/api/copilot.go

type CopilotChatRequest struct {
    Messages []CopilotMessage `json:"messages"`
}

// isRecoverable returns true if an error from the primary provider should
// trigger fallback retry (rate limits, 5xx, network issues — NOT auth/4xx).
func isRecoverable(err error) bool {
    if err == nil {
        return false
    }
    // HTTP errors with status code
    var herr *ProviderHTTPError
    if errors.As(err, &herr) {
        // Recoverable: 429, 502, 503, 504, other 5xx
        if herr.StatusCode == 429 || herr.StatusCode >= 500 {
            return true
        }
        // Not recoverable: 4xx (auth, bad request)
        return false
    }
    // Network errors, timeouts, DNS — recoverable
    return true
}

// HandleCopilotChat proxies chat requests to the configured LLM provider.
// On recoverable failures, retries with the fallback provider if configured.
func (h *handlers) HandleCopilotChat(w http.ResponseWriter, r *http.Request) {
    if !h.copilotConfig.Enabled {
        respondError(w, http.StatusServiceUnavailable, "copilot is not configured (KUBEBOLT_AI_API_KEY not set)")
        return
    }

    var req CopilotChatRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        respondError(w, http.StatusBadRequest, "invalid request")
        return
    }

    // Set up SSE response
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    // Try primary provider
    primary := getProvider(h.copilotConfig.Primary.Provider)
    stream, err := primary.Chat(r.Context(), req.Messages, h.copilotConfig.Primary, h.copilotConfig.MaxTokens)
    usedFallback := false

    // On recoverable error, try fallback if configured
    if err != nil && isRecoverable(err) && h.copilotConfig.Fallback != nil {
        log.Printf("Copilot primary failed (%v), trying fallback %s/%s",
            err, h.copilotConfig.Fallback.Provider, h.copilotConfig.Fallback.Model)
        fallback := getProvider(h.copilotConfig.Fallback.Provider)
        stream, err = fallback.Chat(r.Context(), req.Messages, *h.copilotConfig.Fallback, h.copilotConfig.MaxTokens)
        usedFallback = true
    }

    if err != nil {
        respondError(w, http.StatusBadGateway, "LLM provider error: "+err.Error())
        return
    }
    defer stream.Close()

    // Send a meta event so the UI can show the "via fallback" badge
    if usedFallback {
        fmt.Fprintf(w, "event: meta\ndata: {\"fallback\":true}\n\n")
        w.(http.Flusher).Flush()
    }

    // Stream chunks back as SSE
    flusher := w.(http.Flusher)
    for chunk := range stream {
        data, _ := json.Marshal(chunk)
        fmt.Fprintf(w, "data: %s\n\n", data)
        flusher.Flush()
    }
}

// HandleCopilotConfig returns the public copilot configuration (without API keys).
func (h *handlers) HandleCopilotConfig(w http.ResponseWriter, r *http.Request) {
    resp := map[string]interface{}{
        "enabled":   h.copilotConfig.Enabled,
        "provider":  h.copilotConfig.Primary.Provider,
        "model":     h.copilotConfig.Primary.Model,
        "proxyMode": true,
    }
    if h.copilotConfig.Fallback != nil {
        resp["fallback"] = map[string]string{
            "provider": h.copilotConfig.Fallback.Provider,
            "model":    h.copilotConfig.Fallback.Model,
        }
    }
    respondJSON(w, http.StatusOK, resp)
}
```

### Route registration

```go
// internal/api/router.go
r.Get("/api/v1/copilot/config", h.HandleCopilotConfig)
r.Post("/api/v1/copilot/chat", h.HandleCopilotChat)
```

The `/copilot/config` endpoint is **not** behind `requireConnector` — it must be reachable
even when no cluster is connected so the frontend can show the copilot UI on the welcome screen.

The `/copilot/chat` endpoint should be behind `requireConnector` since the copilot needs
cluster context to be useful.

### Security Notes

- The API key is **never** logged. Sanitize log statements that include the request body.
- The key is read from env vars at startup (or via Kubernetes Secret mount when using Helm).
- Rotation: restart the API pod after changing the secret. KubeBolt does not watch env vars.
- The frontend's `/copilot/config` response intentionally **omits** `apiKey` — only metadata is
  exposed to confirm the copilot is enabled.

## Testing the Copilot

### Manual Test Scenarios

1. **"What's wrong with my cluster?"** → Should call `get_cluster_overview` + `get_insights`, summarize issues
2. **"Why is pod api-server crashing in production?"** → Should call `get_resource_detail` + `get_pod_logs` + `get_events`
3. **"Why is pod X stuck in Pending?"** → Should call `get_resource_describe(pods, ns, X)` to read scheduling events
4. **"Show me CPU usage across nodes"** → Should call `list_resources(nodes)`, format metrics
5. **"What services route to the checkout deployment?"** → Should call `get_topology`, trace edges
6. **"Explain what a CrashLoopBackOff means"** → Should answer from K8s knowledge, no tools needed
7. **"What clusters do I have?"** → Should call `list_clusters`
8. **"Are any HPAs maxed out?"** → Should call `list_resources(hpas)`, check current vs max
9. **"Find anything named gitlab"** → Should call `search_resources(q=gitlab)`
10. **"What changed in the api deployment recently?"** → Should call `get_workload_history(deployments, ns, api)`
11. **"Why is my CronJob backup failing?"** → Should call `get_cronjob_jobs` + `get_pod_logs` for failed jobs
12. **"Show me the YAML for service nginx"** → Should call `get_resource_yaml(services, ns, nginx)`
