# Kobi MCP server (read-only)

KubeBolt exposes Kobi's read-only investigation tools over the
[Model Context Protocol (MCP)](https://modelcontextprotocol.io), so you can
drive a live Kubernetes cluster from any MCP host — Claude Code, Cursor, a
CI/CD step, or another agent — using **that host's own LLM**.

It is **read-only**: it exposes the inspection tools (overview, resources,
YAML, describe, pod logs, events, insights, topology, time-series metrics) and
withholds every mutating action. The read-only guarantee is enforced
server-side — a client cannot invoke a mutating tool even by name.

There are two ways to run it.

## 1. Remote, over HTTP (works with the OSS server, and SaaS/EE)

The main `kubebolt` server publishes the MCP endpoint at:

```
POST /api/v1/mcp
```

It uses the standard **Streamable HTTP** transport (one JSON-RPC request →
one JSON response) and is authenticated with a normal KubeBolt **API token**.

**Setup:**

1. In KubeBolt, create an API token (Admin → API Tokens). You get a `kb_…`
   value.
2. Point your MCP host at the endpoint with the token as a bearer header.

Example MCP host config (Claude Code / Cursor `mcpServers`):

```json
{
  "mcpServers": {
    "kubebolt": {
      "type": "http",
      "url": "https://kubebolt.example.com/api/v1/mcp",
      "headers": { "Authorization": "Bearer kb_xxxxxxxxxxxxxxxx" }
    }
  }
}
```

**Multi-cluster / multi-tenant:** the endpoint resolves the target cluster the
same way the rest of the API does:

- The token identifies the **tenant** (always `default` in OSS; a real tenant
  in EE/SaaS). The tenant is taken from the token, never from a request
  parameter, so one tenant can't read another's clusters.
- The **cluster** defaults to the server's active context. To target a
  specific cluster, send the `X-KubeBolt-Cluster: <cluster-id>` header — one
  endpoint then serves every cluster the token is authorized for.

`initialize` and `tools/list` work even when the cluster is momentarily
disconnected; a `tools/call` in that window returns a graceful
`{"error":"cluster not connected"}` result rather than failing the session.

## 2. Local, over stdio (`kubebolt-mcp`)

For a single operator on the same machine as the kubeconfig (e.g. Claude Code
running locally, or a CI runner), use the standalone `kubebolt-mcp` binary. It
talks MCP over stdin/stdout and connects straight to your kubeconfig — no
server, no auth.

Build it:

```bash
cd apps/api && go build -o kubebolt-mcp ./cmd/mcp
```

MCP host config:

```json
{
  "mcpServers": {
    "kubebolt": {
      "command": "kubebolt-mcp",
      "args": ["--kubeconfig", "/home/me/.kube/config"]
    }
  }
}
```

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--kubeconfig` | `$KUBECONFIG` or `~/.kube/config` | kubeconfig to use |
| `--connect-wait` | `10` | seconds to wait for the initial connection before serving (`0` = don't wait) |
| `--metric-interval` | (server default) | metrics polling interval, seconds |
| `--insight-interval` | (server default) | insight evaluation interval, seconds |
| `--version` | | print version and exit |

All logs go to **stderr**, so they never corrupt the protocol stream on
stdout.

## Tools exposed

`get_cluster_overview`, `list_resources`, `get_resource_detail`,
`get_resource_yaml`, `get_resource_describe`, `get_pod_logs`,
`get_workload_pods`, `get_workload_history`, `get_cronjob_jobs`,
`get_topology`, `get_insights`, `get_events`, `search_resources`,
`get_permissions`, `list_clusters`, `get_workload_metrics`,
`get_kubebolt_docs`.

These are the same tool definitions the in-product Copilot uses, filtered to
the read-only set (`GovernedToolDefinitions(false, false)`).

## Prompts

The server also exposes one MCP **prompt**, `kobi-guidance`, which returns
Kobi's operating guidance (sourced from the same embedded prompt layers the
in-product Copilot uses) so the host LLM can adopt Kobi's voice and diagnostic
approach. It is prefixed with a note that this surface is read-only.

## Protocol notes

- JSON-RPC 2.0; MCP protocol revision `2025-06-18` (the server echoes a
  client's requested version when it sends one).
- Methods: `initialize`, `ping`, `tools/list`, `tools/call`, `prompts/list`,
  `prompts/get`, and the `notifications/initialized` notification.
- Tool-execution failures are reported via `isError: true` in the result (so
  the host LLM can read and recover), not as JSON-RPC errors. JSON-RPC errors
  are reserved for protocol misuse (unknown method/tool, bad params).
