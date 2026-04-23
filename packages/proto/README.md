# packages/proto

Protobuf definitions shared between the KubeBolt backend (`apps/api`) and the
agent (`packages/agent`).

## Current schema

- `agent/v1/agent.proto` — `AgentIngest` service (Register, StreamMetrics,
  Heartbeat). Wire contract for kubebolt-agent DaemonSet → backend.

Design context: `internal/kubebolt-agent-technical-spec.md`.

## Generating Go code

Uses [buf](https://buf.build/). Install once:

```bash
brew install bufbuild/buf/buf
```

From this directory:

```bash
buf lint          # enforce style
buf breaking --against '.git#branch=main'   # detect breaking changes
buf generate      # emit Go code under ./gen
```

Generated code lands under `packages/proto/gen/agent/v1/` with Go import
path `github.com/kubebolt/kubebolt/packages/proto/gen/agent/v1`.

## Versioning

Breaking changes require bumping the package version (`kubebolt.agent.v2`)
and keeping the previous version live for at least one release. The backend
must accept both N and N-1 during the overlap window.
