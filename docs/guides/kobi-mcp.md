# Kobi MCP server (read-only)

Kobi is KubeBolt's AI SRE. KubeBolt exposes the read-only investigation tools
of Kobi Copilot over the
[Model Context Protocol (MCP)](https://modelcontextprotocol.io), so you can
investigate a live Kubernetes cluster from any MCP host — Claude Code, Cursor,
a CI/CD step, or another agent — using **that host's own LLM**. No KubeBolt AI
provider key is needed for this: the host's model does the reasoning, KubeBolt
only serves the tools.

It is **read-only**: it exposes the inspection tools (overview, resources,
YAML, describe, pod logs, events, insights, topology, time-series metrics) and
withholds every mutating action. The read-only guarantee is enforced
server-side — a client cannot invoke a mutating tool even by name. Kobi's
action proposals (restart, scale, rollback, ...) exist only inside the KubeBolt
UI, where a person approves them; see [copilot.md](copilot.md#actions-and-approval).

There are two ways to run it.

## 1. Remote, over HTTP (open-source server and KubeBolt Cloud)

The main `kubebolt` server publishes the MCP endpoint at:

```
POST /api/v1/mcp
```

It uses the standard **Streamable HTTP** transport (one JSON-RPC request →
one JSON response) and sits behind normal KubeBolt authentication: a long-lived
**API token** (`kbk_…` or `kbs_…`, from **Administration → API Tokens**) or a
session access token. Session tokens expire after 15 minutes by default, so use
an API token for MCP hosts.

**Pick the token type by how the host reaches KubeBolt:**

| Host reaches KubeBolt through… | Use | Scopes |
|---|---|---|
| The bundled web container's nginx — the UI URL of a Helm or Docker Compose install (the usual case) | **API token** (`kbk_`) | Must include `/api/v1/mcp` or `*` |
| The API directly — the single binary / single-container image, or the chart's `<release>-api` Service from inside your network | API token or **service token** (`kbs_`) | A `kbs_` token's default scopes already include `/api/v1/mcp` |

> **Why not a service token over the public URL?** The bundled nginx marks every
> request it proxies with `X-KubeBolt-Edge: public`, and the API rejects `kbs_`
> service tokens on such requests with `401 invalid or expired token`. That is
> deliberate: a leaked service token is useless from the internet.
>
> **Scopes for `kbk_` tokens.** An API key is issued with the scopes you pick
> and has **no default scopes**; without `/api/v1/mcp` (or `*`) it returns
> `403 token scope does not permit this path`. The scope checkboxes in the UI
> don't list `/api/v1/mcp`, so either tick **Everything (all authenticated
> paths)**, or create the token through the API with an explicit scope:
>
> ```bash
> curl -sS -X POST https://kubebolt.example.com/api/v1/admin/api-tokens \
>   -H "Authorization: Bearer <admin-access-token>" -H 'Content-Type: application/json' \
>   -d '{"label":"claude-code-mcp","type":"apikey","role":"viewer","scopes":["/api/v1/mcp"]}'
> ```

**Setup:**

1. Create the token as described above. The value is shown once.
2. Point your MCP host at the endpoint with the token as a bearer header.

Example MCP host config (Claude Code / Cursor `mcpServers`):

```json
{
  "mcpServers": {
    "kubebolt": {
      "type": "http",
      "url": "https://kubebolt.example.com/api/v1/mcp",
      "headers": { "Authorization": "Bearer kbk_xxxxxxxxxxxxxxxx" }
    }
  }
}
```

**Multi-cluster / multi-tenant:** the endpoint resolves the target cluster the
same way the rest of the API does:

- The token identifies the **tenant** (always `default` in the open-source
  edition; the customer's organization in KubeBolt Cloud). The tenant is taken
  from the token, never from a request parameter, so one tenant can't read
  another's clusters.
- The **cluster** defaults to the server's active context. To target a
  specific cluster, send the `X-KubeBolt-Cluster: <context-name>` header with
  a cluster name as listed by `GET /api/v1/clusters` (or the `list_clusters`
  tool) — one endpoint then serves every cluster the token is authorized for.

`initialize` and `tools/list` work even when the cluster is momentarily
disconnected; a `tools/call` in that window returns a graceful `isError`
result (`{"error":"This needs live cluster access. …","needsProxy":true}`)
rather than failing the session. `get_kubebolt_docs` keeps working, since it
needs no cluster.

## 2. Local, over stdio (`kubebolt-mcp`)

For a single operator on the same machine as the kubeconfig (e.g. Claude Code
running locally, or a CI runner), use the standalone `kubebolt-mcp` binary. It
talks MCP over stdin/stdout and connects straight to your kubeconfig — no
server, no auth.

Download it from the [GitHub release](https://github.com/clm-cloud-solutions/kubebolt/releases)
assets — `kubebolt-mcp-linux-amd64`, `kubebolt-mcp-linux-arm64`,
`kubebolt-mcp-darwin-amd64`, `kubebolt-mcp-darwin-arm64`,
`kubebolt-mcp-windows-amd64.exe` — rename it to `kubebolt-mcp`, make it
executable and put it on your `PATH`. Or build it from source:

```bash
make build-mcp            # or: cd apps/api && go build -o kubebolt-mcp ./cmd/mcp
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

These are the same tool definitions Kobi uses inside KubeBolt, filtered to
the read-only set (`GovernedToolDefinitions(false, false)`) — the 9
`propose_*` action tools are neither listed nor callable.

## Prompts

The server also exposes one MCP **prompt**, `kobi-guidance`, which returns
Kobi's operating guidance (sourced from the same embedded prompt layers Kobi
uses inside KubeBolt) so the host LLM can adopt Kobi's voice and diagnostic
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
KubeBolt (Administration → API Tokens; see the token table in section 1), then:

```bash
TOKEN=kbk_xxxxxxxxxxxxxxxx
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
  `Authorization: Bearer kbk_…` header.

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
| 6 | Graceful when disconnected | `tools/call` while the cluster is down | `result.isError = true`, text `{"error":"This needs live cluster access. …","needsProxy":true}` — **not** a session failure |
| 7 | Prompts | `prompts/list` then `prompts/get {name:"kobi-guidance"}` | one prompt; one `user` message starting with the read-only preamble |
| 8 | Notification (HTTP) | POST `notifications/initialized` | HTTP **202**, empty body |
| 9 | Wrong verb (HTTP) | `GET /api/v1/mcp` | HTTP **405**, `Allow: POST` |
| 10 | **Auth required** (HTTP) | POST with no / bad token (when `KUBEBOLT_AUTH_ENABLED=true`) | HTTP **401** `{"error":"authentication required"}` / `invalid or expired token` |
| 11 | Multi-cluster routing | add header `X-KubeBolt-Cluster: <context-name>` | `tools/call` results reflect that cluster (with multiple clusters registered) |
| 12 | Tenant isolation (KubeBolt Cloud) | use token from tenant A | only tenant A's clusters are visible; there is no way to pass another tenant as a parameter |
| 13 | Edge guard (HTTP, through the web container) | POST with a `kbs_` service token via the public URL | HTTP **401** `{"error":"invalid or expired token"}` even though the token is valid |

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
