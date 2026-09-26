# packages/proto

Protobuf definitions shared between the KubeBolt backend (`apps/api`) and the
agent (`packages/agent`).

## Current schema

- `kubebolt/agent/v2/channel.proto` — `AgentChannel` service. Single bidi RPC
  `Channel(stream AgentMessage) returns (stream BackendMessage)` that
  multiplexes everything the agent ↔ backend exchange: Hello/Welcome
  handshake, heartbeat, metrics push, and the K8s API proxy
  (`kube_request` / `kube_response` / `kube_event`).

The previous `agent/v1/agent.proto` (3 unary/server-streaming RPCs:
Register / StreamMetrics / Heartbeat) was replaced by v2 before the agent's first public release.
Hard cutover — the agent was not yet published externally, so no
fleets needed a migration window.

Overview: [`docs/architecture.md`](../../docs/architecture.md). Original design
(Spanish): [`docs/architecture/sprint-a5-agent-proxy.md`](../../docs/architecture/sprint-a5-agent-proxy.md).

## Generating Go code

Uses [buf](https://buf.build/). Install once:

```bash
brew install bufbuild/buf/buf
```

From this directory:

```bash
buf lint                                       # enforce style
buf breaking --against '.git#branch=develop'   # detect breaking changes
buf generate                                   # emit Go code under ./gen
```

Generated code lands under `packages/proto/gen/kubebolt/agent/v2/` with
Go import path
`github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2`.

## Versioning

Future breaking changes bump the package version (`kubebolt.agent.v3`)
and keep v2 live for at least one release while fleets migrate.
