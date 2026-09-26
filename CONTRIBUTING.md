# Contributing to KubeBolt

Thanks for taking the time. Bug reports, documentation fixes, new insight
rules, integrations and UI improvements are all welcome.

By participating you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
Security issues go through [SECURITY.md](SECURITY.md), not public issues.

## Before you start

- **Bugs** — open an issue with the KubeBolt version, how it's deployed
  (Helm, Docker, binary…), the Kubernetes distribution and version, and what
  you expected versus what happened. Logs from the API help
  (`LOG_LEVEL=debug`).
- **Features and larger changes** — open an issue or a
  [discussion](https://github.com/clm-cloud-solutions/kubebolt/discussions)
  first, so we can agree on the approach before you invest the time.
- **Small fixes** (typos, docs, obvious bugs) — send the pull request
  directly.

## Repository layout

```
apps/api          Go backend (REST, WebSocket, gRPC agent channel, MCP)
apps/web          React + TypeScript frontend
packages/agent    kubebolt-agent (Go)
packages/proto    Protobuf definitions of the agent channel
deploy/           Helm charts, Docker Compose, raw agent manifests, Homebrew/krew
docs/             Operator guides, integrations, architecture, release notes
tests/            End-to-end fixtures
```

[`docs/architecture.md`](docs/architecture.md) explains how the pieces fit;
[`CLAUDE.md`](CLAUDE.md) goes package by package.

## Local setup

Requirements: **Go 1.25+** (CI uses the toolchain pinned in
`apps/api/go.mod`), **Node 22**, and a Kubernetes cluster in your kubeconfig
(kind, k3d, Docker Desktop, minikube or a remote one).

```bash
make dev            # API on :8080 and Vite on :5173, one terminal, Ctrl+C stops both
```

Or run them separately:

```bash
cd apps/api && go run ./cmd/server --kubeconfig ~/.kube/config
cd apps/web && npm install && npm run dev
```

Useful variants:

- `make dev-clean` / `make dev-api-clean` — start with an empty kubeconfig,
  to work on the no-clusters and waiting-for-agent states.
- `KUBEBOLT_ADMIN_PASSWORD=...` sets the admin password; otherwise one is
  generated and printed to the terminal. `KUBEBOLT_AUTH_ENABLED=false` skips
  login entirely.
- Copy [`.env.example`](.env.example) to `.env` to enable optional features
  (Kobi, notifications…). `make dev` loads it.
- For historical metrics, start VictoriaMetrics with
  `cd deploy && docker compose up -d victoriametrics` and export
  `KUBEBOLT_METRICS_STORAGE_URL=http://localhost:8428`.
- `make kind-testbed` installs metrics-server and a demo workload into your
  current kind cluster; `make agent-dev` builds the agent image, deploys it
  there and follows its logs.

## Tests and checks

Run the same gate CI runs before pushing:

```bash
make ci-local
```

It runs `go build`, `go vet` and `go test -race` for `apps/api`, then
`npm test` (Vitest) and `npm run build` in `apps/web`. The build matters:
Vitest does not type-check, `tsc` does.

Agent changes: `cd packages/agent && go test ./...`.

Some guard tests encode architecture rules and will fail on purpose — for
example `apps/web/src/utils/scope.test.ts` fails when a new route is not
declared as global or cluster scope, and `apps/api/internal/api/shared_seat_test.go`
fails when a handler reads the global active cluster instead of the
per-request one. Read the test's comment; it tells you what to do.

## Pull requests

- Branch from `develop` and open the pull request against `develop`;
  `main` receives releases.
- Keep each pull request focused on one change, and include tests for
  behaviour changes.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):
  `type(scope): summary` — for example `fix(insights): …`, `feat(web): …`,
  `docs(agent): …`.
- Update the docs that describe what you changed (README, `docs/`, chart
  READMEs, `.env.example` for new variables) in the same pull request, and
  describe user-visible changes in the pull request so they reach the
  [CHANGELOG](CHANGELOG.md) and the release notes.
- UI changes: include a screenshot or a short recording, in light and dark
  themes if the change is visual.
- New third-party images or dependencies go through the release security
  gates described in `CLAUDE.md` (Trivy scans, pinned versions).

## Adding common things

- **An insight rule** — `apps/api/internal/insights/rules.go` (add it to
  `AllRules()`), its entry in the policy catalog in `policy.go`
  (malfunction or expectation), and tests next to the existing ones.
- **A resource type** — informer and permissions in
  `apps/api/internal/cluster/`, the list route in `apps/web/src/App.tsx`,
  its scope in `apps/web/src/utils/scope.ts`, and the sidebar entry.
- **A Kobi tool** — definition and executor in `apps/api/internal/copilot/`.
  Read-only tools are also exposed over MCP; actions must be `propose_*`
  tools that go through user approval.

## License

KubeBolt is licensed under [Apache 2.0](LICENSE). By submitting a
contribution you agree that it is licensed under the same terms.
