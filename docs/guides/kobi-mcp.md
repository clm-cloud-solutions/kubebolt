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

1. In KubeBolt, create a **service token** — Admin → API Tokens, type
   **Service** (the default). You get a `kbs_…` value. Its default scopes
   already include `/api/v1/mcp`, so it works against this endpoint out of the
   box.

   > **Use a service token (`kbs_`), not an API key (`kbk_`).** A `kbs_` service
   > token created with the default scopes can reach `/api/v1/mcp`. A `kbk_` API
   > key is issued with **no default scopes**, so it returns
   > `403 token scope does not permit this path` unless you explicitly grant it
   > `/api/v1/mcp` (or `*`). The same applies to **any** token you mint with
   > custom scopes — include `/api/v1/mcp` (or `*`).
2. Point your MCP host at the endpoint with the token as a bearer header.

Example MCP host config (Claude Code / Cursor `mcpServers`):

```json
{
  "mcpServers": {
    "kubebolt": {
      "type": "http",
      "url": "https://kubebolt.example.com/api/v1/mcp",
      "headers": { "Authorization": "Bearer kbs_xxxxxxxxxxxxxxxx" }
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

## Verifying it works (manual test plan)

This is the checklist to run when bringing the server up in a new environment.

### Already covered by automated tests

`go test ./internal/mcp/...` covers the protocol dispatch, both transports
(via `httptest` and in-memory pipes), the read-only guard, and prompts, all
against a fake tool provider.

Both transports have also been verified **end-to-end against a live cluster**:
stdio against a real MCP host (Claude Code) and HTTP with a real `kbs_` service
token via raw requests — `initialize` (`2025-06-18`), `tools/list` (17 tools, 0
`propose_`), real `tools/call` reads, **Secret YAML redaction** (`data` → `REDACTED`),
the **read-only guard** (`propose_*` rejected `-32602 unknown tool`), `401` with
no token, and `405` on `GET`. Re-run the checklist below when bringing the
server up in a new environment (different auth config, cluster, or host).

### The four ways to test

#### A. Raw JSON-RPC over stdio (protocol-level, no cluster needed)

Quickest sanity check. Pipe newline-delimited JSON-RPC into the binary:

```bash
cd apps/api && go build -o /tmp/kubebolt-mcp ./cmd/mcp

printf '%s\n%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | /tmp/kubebolt-mcp --kubeconfig ~/.kube/config --connect-wait 5 2>/dev/null | jq -c .
```

Expect: an `initialize` result, **no** line for the notification, then a
`tools/list` result with 17 tools.

#### B. Raw JSON-RPC over HTTP with curl (needs a live server + token)

This is the path the automated tests can't reach. Create an API token in
KubeBolt (Admin → API Tokens), then:

```bash
TOKEN=kbs_xxxxxxxxxxxxxxxx
BASE=https://kubebolt.example.com/api/v1/mcp
auth=(-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json')

# 1. initialize
curl -sS -X POST "$BASE" "${auth[@]}" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' | jq

# 2. tools/list — confirm 17 tools and NO propose_* leaked
curl -sS -X POST "$BASE" "${auth[@]}" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | jq '.result.tools | length, (map(.name) | map(select(startswith("propose_"))))'

# 3. tools/call against real cluster data
curl -sS -X POST "$BASE" "${auth[@]}" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_cluster_overview"}}' \
  | jq '.result.content[0].text | fromjson'

# 4. a tool with arguments
curl -sS -X POST "$BASE" "${auth[@]}" \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_resources","arguments":{"type":"pods","namespace":"kube-system"}}}' \
  | jq '.result.content[0].text | fromjson | .items | length'
```

#### C. MCP Inspector (official tool)

The reference [MCP Inspector](https://github.com/modelcontextprotocol/inspector)
gives a UI to browse tools/prompts and fire calls:

```bash
npx @modelcontextprotocol/inspector
```

- **stdio:** command `kubebolt-mcp`, args `--kubeconfig ~/.kube/config`.
- **HTTP:** transport "Streamable HTTP", URL `https://…/api/v1/mcp`, and add an
  `Authorization: Bearer kbs_…` header.

#### D. A real host (Claude Code / Cursor)

The end-to-end acceptance test: wire the config from sections 1/2 above and ask
the host something like *"using the kubebolt tools, what's the health of my
cluster and are any pods crash-looping?"* — confirm it calls the tools and
answers from live data.

### What to check, and the expected result

| # | Check | How | Expected |
|---|-------|-----|----------|
| 1 | Handshake | `initialize` | `result.protocolVersion` echoes yours; `serverInfo.name = kubebolt-kobi`; `capabilities` has `tools` + `prompts` |
| 2 | Catalogue | `tools/list` | exactly the 17 read tools; **zero** `propose_*` |
| 3 | Read works | `tools/call get_cluster_overview` on a connected cluster | `result.content[0].text` is the overview JSON; **no** `isError` |
| 4 | Args plumb through | `tools/call list_resources {type:pods,namespace:…}` | filtered list in the result |
| 5 | **Read-only guard** | `tools/call` with `name:"propose_delete_resource"` | JSON-RPC **error**, code `-32602`, message `unknown tool: …` — the mutation is rejected even by name |
| 6 | Graceful when disconnected | `tools/call` while the cluster is down | `result.isError = true`, text `{"error":"cluster not connected"}` — **not** a session failure |
| 7 | Prompts | `prompts/list` then `prompts/get {name:"kobi-guidance"}` | one prompt; one `user` message starting with the read-only preamble |
| 8 | Notification (HTTP) | POST `notifications/initialized` | HTTP **202**, empty body |
| 9 | Wrong verb (HTTP) | `GET /api/v1/mcp` | HTTP **405**, `Allow: POST` |
| 10 | **Auth required** (HTTP) | POST with no / bad token (when `KUBEBOLT_AUTH_ENABLED=true`) | HTTP **401** `{"error":"authentication required"}` / `invalid or expired token` |
| 11 | Multi-cluster routing | add header `X-KubeBolt-Cluster: <id>` | `tools/call` results reflect that cluster (EE/SaaS, or OSS with multiple contexts) |
| 12 | Tenant isolation (EE/SaaS) | use token from tenant A | only tenant A's clusters are visible; there is no way to pass another tenant as a parameter |

### Notes / gotchas

- **Auth disabled?** When `KUBEBOLT_AUTH_ENABLED=false`, no token is needed and
  the request resolves as the default admin/tenant — check #10 is then expected
  to succeed without a token.
- **`jq` decoding tool output:** tool results are JSON *strings* inside
  `content[0].text`, so pipe through `fromjson` (as above) to inspect them.
- **stdio logs:** they go to stderr — redirect with `2>/dev/null` when you only
  want the protocol stream, or `2>mcp.log` to keep them.
- **HTTP batching / SSE:** this server handles one JSON-RPC message per POST and
  replies with `application/json`; it does not implement JSON-RPC batch arrays
  or the optional `text/event-stream` response. If a host strictly negotiates
  SSE, it will still work over the single-response JSON path.

## Protocol notes

- JSON-RPC 2.0; MCP protocol revision `2025-06-18` (the server echoes a
  client's requested version when it sends one).
- Methods: `initialize`, `ping`, `tools/list`, `tools/call`, `prompts/list`,
  `prompts/get`, and the `notifications/initialized` notification.
- Tool-execution failures are reported via `isError: true` in the result (so
  the host LLM can read and recover), not as JSON-RPC errors. JSON-RPC errors
  are reserved for protocol misuse (unknown method/tool, bad params).
