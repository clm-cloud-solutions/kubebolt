# Changelog

All notable changes to KubeBolt are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.8.0] — 2026-05-04

Resilience and first-use UX release. Agent-proxy clusters now survive
backend restarts; the post-restart reconnect window is no longer a
disruptive blank page; first-boot installs with zero clusters get a
clean empty-state with an Add-cluster CTA instead of a generic
"Cluster unreachable" page; and the Integrations catalog renders even
when no cluster is connected so a fresh install can browse what's
installable before connecting anything.

### Added

- **Persistent agent registry.** New `agents` BoltDB bucket captures
  every agent Hello (capabilities, displayName from
  `KUBEBOLT_AGENT_CLUSTER_NAME`, node, version) on connect and stamps
  `DisconnectedAt` on clean Unregister. On boot, records advertising
  the `kube-proxy` capability are replayed into
  `manager.agentProxyContexts` **before** the gRPC server starts
  accepting traffic — the cluster selector keeps showing every
  previously-connected agent-proxy cluster from the moment the
  backend boots, instead of going blank for the ~30s reconnect
  window. Records older than 24h with a non-zero `DisconnectedAt`
  are pruned (configurable via
  `KUBEBOLT_AGENT_REGISTRY_PRUNE_HORIZON`).
- **Connector auto-recovery.** When an agent registers and the
  manager has an agent-proxy context whose connector is currently
  failed (typical post-restart race: UI auto-switches before the
  agent has reconnected), `AddAgentProxyCluster` now spawns a
  goroutine that re-runs `connectToContextLocked` so the cluster
  recovers without a manual click on Retry. Pairs with a `cluster:connected`
  WebSocket broadcast that invalidates `['clusters']` +
  `['cluster-overview']` immediately on receipt, instead of waiting
  up to 30s for TanStack Query's next refetch tick.
- **Fast-fail on no-agent connect.** `connectToContextLocked` checks
  `agentRegistry.CountByCluster()` before `Connector.Start()` and
  bails sub-millisecond when zero agents are registered for that
  cluster_id — the user gets a 503 immediately instead of waiting
  the full `WaitForCacheSync(20s)` on list calls that were always
  going to fail. Auto-recovery then takes over once the agent dials
  in.
- **No-clusters empty state.** Layout detects `clusters: []` (Go's
  nil-slice JSON shape) and renders a centered `Cable` icon + "No
  clusters configured" + admin-only "Add cluster" CTA pointing at
  `/clusters`. The Topbar's selector reads "No clusters" instead of
  the prior misleading "loading…", and the dropdown stays
  interactive so admins reach Manage Clusters from there.
- **Waiting-for-agent empty state.** Distinct from "Cluster
  unreachable" — when the connector failed because no agent has
  registered yet (transient post-restart), Layout shows a spinning
  Loader2 + "Waiting for agent to register" copy and **no Retry
  button** (clicking it just re-fast-fails). The page auto-heals via
  the `cluster:connected` WS broadcast.
- **Platform routes bypass.** `/clusters`, `/admin/*`, and
  `/settings` always render their own page regardless of cluster
  state — so the user can manage clusters / users / integrations
  from inside an empty-state without any chicken-and-egg trap.
- **Integrations catalog without a cluster.** `/integrations` and
  `/integrations/{id}` left the `requireConnector` middleware
  group; handlers degrade to metadata-only with `StatusNotInstalled`
  + Health.Message="No cluster connected" when `conn==nil`. The
  Integrations page shows an info banner and disables Install /
  Manage buttons (with explanatory tooltips) until a cluster is
  available — install routes still 503 server-side so the gate is
  enforced regardless of UI state.
- **Workload Monitor coverage banner**, promised in 1.7.0 NOTES.
  Detects when fewer pods are reporting samples than declared
  replicas and renders an amber "Partial coverage — KubeBolt Agent
  has data for X of N replicas" banner that distinguishes a
  scheduling gap from a healthy chart. (Implementation landed in
  the priority-knobs commit; this release also fixes a label-name
  bug below.)
- **`make dev-clean` / `make dev-api-clean` targets.** Boot the API
  against an empty kubeconfig synthesized at
  `/tmp/kb-empty-kubeconfig.yaml` on every invocation — useful for
  testing the persistent-registry boot-restore path and the
  no-clusters empty-state UX without touching `~/.kube/config`.
- **Agent-proxy tunnel idle timeout + audit log** (Sprint A.5 §0.9
  commit 8f, partial — idle timeout and audit; max-duration / quotas
  / Prometheus metrics deferred). Every SPDY tunnel opened through
  the agent (exec, port-forward, file browser) now ships with a
  watchdog goroutine that closes the tunnel when no Read/Write has
  happened for `KUBEBOLT_AGENT_TUNNEL_IDLE_TIMEOUT` (default 5m) —
  catches orphan tunnels left behind when the agent crashes
  mid-session and the upstream HTTP client doesn't notice the EOF.
  Each tunnel emits one INFO log line on open and one on close with
  cluster_id, agent_id, request_id, path, reason
  (`local close` / `peer EOF` / `peer stream_closed: <reason>` /
  `multiplexor slot closed` / `idle timeout`), duration, and
  `bytes_in`/`bytes_out` — single grep'able lifecycle record per
  session.
- **Dashboard split into Overview / Capacity / Reliability sub-tabs.**
  The previous single-page Overview was carrying both the at-a-glance
  scan ("is everything fine?") and the investigation surface ("why
  is this slow / over-provisioned?"). They wanted different
  attention budgets, so they're now lenses on the same dashboard,
  driven by a sub-nav under the Topbar with `LayoutDashboard` /
  `Gauge` / `Activity` icons. The Sidebar's Overview item and the
  Topbar's Dashboard pill stay active across all three sub-tabs;
  active state is centralized in `apps/web/src/utils/routes.ts` so
  future sub-tabs land in one place. Active underline switched from
  `kb-accent` (brand green, reserved for Kobi / health-OK signals)
  to `status-info` (blue, the same selection color the Sidebar and
  Topbar use), which homologates the "I'm here" palette across the
  app.
- **Capacity sub-tab.** Investigation surface for "how is the cluster
  consuming, and is it sized right for what it's actually doing?"
  Same `RangeSelector` + `DataFreshnessIndicator` as Overview.
  Panels: 2×2 trends grid (CPU / Memory / Network / Filesystem) with
  overlaid deploy markers + tooltip-grouped "X deploys here" for
  same-bucket rollouts; **Recent Deploys** table backed by a new
  `/deploys` endpoint that walks ReplicaSet creation timestamps and
  emits `DeployEvent[]` with namespace/kind/name/deployedAt/image;
  **Top Workloads · CPU** ranks cluster-wide consumers using a
  `label_replace` chain that collapses ReplicaSet names to their
  Deployment (recovers the user-visible workload from the agent's
  one-step ownerRef enrichment); **Right-sizing Recommendations**
  applies deterministic rules (NEAR-LIMIT if P95 ≥ 80% × limit,
  OVER-PROV if P95 < 50% × request with absolute floor of 50m / 100Mi,
  NO-SPECS for workloads with neither). Each panel ships with an
  Ask-Kobi affordance — panel-level for summarization, plus per-row
  on Recent Deploys and Right-sizing where each row is its own
  actionable investigation; single-row payloads switch the prompt
  builder to a singular phrasing so the LLM stays narrow instead of
  re-summarizing the list.
- **Reliability sub-tab.** L7 lens on what the cluster is actually
  serving — surfaces only when Hubble HTTP metrics are flowing into
  VictoriaMetrics (`useHubbleAvailable` probe), so empty clusters
  don't see a "needs Hubble" placeholder. Five panels: **Cluster
  error rate** chart split into 4xx (amber) and 5xx (red) series so
  the tooltip answers "client mistakes or server breakage?" at a
  glance, with a `tooltipExtra` slot showing absolute volume context
  (total req/s, error req/s) at the hovered timestamp;
  **Top Workloads · Traffic** with a stacked status_class
  distribution bar, per-class chips with absolute rates, and a
  sparkline of req/s; **Top Workloads · Latency** with a prominent
  160×20 sparkline and an inline `min..max` range derived from the
  trend array (no extra query) — status breakdown lives in the
  tooltip only to avoid duplicating Traffic; **Error Hot-spots**
  ranks pod-to-pod (collapsed to workload) flows by absolute error
  req/s, not percentage, so a low-volume but consistently-failing
  flow doesn't get buried; **Network Drops** surfaces L4 flows with
  `verdict=dropped` from `pod_flow_events_total` — the early-warning
  channel for NetworkPolicy violations and connection refused that
  the HTTP panels miss because they only see traffic that completed
  the handshake. Per-row Kobi on Error Hot-spots and Network Drops
  for focused investigation. Cross-cutting: shared
  `StatusDistribution` module with `useWorkloadStatusDist` hook so
  Traffic and Latency dedupe the same VM round-trip via TanStack
  Query's queryKey cache.
- **`MetricChart` `tooltipExtra` slot.** Optional callback receiving
  the hovered unix timestamp and returning JSX rendered below the
  standard payload, behind a divider. Lets a page surface
  out-of-band context (a separate range query, a joined map) without
  forcing every chart in the app to learn about it. Default
  behavior unchanged for charts that don't pass the prop. Also
  added `'percent'` to `UnitKind` (label `%`, divisor 1) and an
  `errorRate` accent (red `#ef4056`) to `METRIC_ACCENTS`.
- **`PanelInquiry` Kobi triggers.** Five new panel kinds in
  `apps/web/src/services/copilot/triggers.ts`:
  `top_consumers_cpu`, `right_sizing`, `recent_deploys`,
  `top_workloads_traffic`, `error_hotspots`, `top_latency`,
  `network_drops`. Each carries multi-row (lead/close) phrasing for
  panel-level Ask-Kobi and singular phrasing (`singleLead` /
  `singleClose`) for per-row Ask-Kobi, with operational hints baked
  in where useful — e.g. `error_hotspots` reminds the LLM that
  4xx points at the caller while 5xx points at the receiver, and
  `network_drops` enumerates likely causes (NetworkPolicy,
  connection refused, host firewall, pod down) so the model doesn't
  have to guess.

### Changed

- **Welcome before Register on the agent gRPC handshake.** Server
  now sends the Welcome envelope BEFORE adding the agent to the
  registry, closing a race where the new connector auto-retry could
  route a kube_request through the multiplexor while a DaemonSet
  pod was still mid-handshake — the agent's reader bailed with
  `expected Welcome, got KubeRequest` and went into a 1-minute
  backoff. With this fix the multiplexor only ever sees agents that
  have completed handshake.
- **`agents` BoltDB bucket** added to `auth.NewStore` schema. Lives
  in the same database file as `users`, `tenants`, etc. Backwards-
  compatible: missing on first boot of an upgraded install, created
  on next start.
- **WebSocket message types** add `cluster:connected` for connector
  recovery events. Frontend `useWebSocket` invalidates
  `['clusters']` + `['cluster-overview']` immediately on receipt,
  bypassing the existing 2s overview-debounce.

### Fixed

- **Workload Monitor "Partial coverage" banner false-fired on every
  multi-replica workload.** The count query grouped by `pod` (Prom
  convention) but the agent shipper emits the label as `pod_name`;
  grouping by a non-existent label collapsed every matching series
  into one bucket, so `count(...)` returned 1 regardless of how
  many replicas were observed. Switched to `by (pod_name)`. Verified
  against VictoriaMetrics: same selector returned 2 for a 2-replica
  deployment with the corrected label, vs 1 with the old one.
- **Agent flow aggregator silently dropped every `verdict=dropped`
  flow** (`packages/agent/internal/flows/aggregator.go`). The
  pod-to-pod path filtered out flows whose direction wasn't `EGRESS`
  to avoid double-counting forwarded traffic — each forwarded
  packet appears twice (egress on the source node, ingress on the
  destination), and we keep the egress observation. But Cilium
  emits dropped flows with `TRAFFIC_DIRECTION_UNKNOWN` (the SYN was
  rejected before direction classification kicked in) and they
  appear exactly once at the denial point. The EGRESS filter was
  swallowing every drop in clusters with NetworkPolicies active, so
  `pod_flow_events_total{verdict="dropped"}` never reached
  VictoriaMetrics and the new Reliability tab's Network Drops panel
  was perma-empty regardless of how many drops were actually
  happening — its empty state ("NetworkPolicies are passing —
  nothing's silently blocked") was a false positive in the worst
  way: looked reassuring, said nothing about reality. Fix: bypass
  the `is_reply` and `EGRESS-only` checks when verdict is
  `dropped`. Verified end-to-end against a temporary
  `CiliumNetworkPolicy` with `ingressDeny`: drops appeared in
  `cilium hubble observe`, in
  `pod_flow_events_total{verdict="dropped"}` in VM, and in the
  Network Drops panel within ~30s.
- **Sub-tabs active underline used `kb-accent` (brand green)
  instead of `status-info` (selection blue).** Inconsistent with
  every other "I'm here" indicator in the app — Sidebar items, the
  Topbar Dashboard pill, etc. all use blue. Switched to
  `border-status-info`; brand green stays reserved for Kobi /
  health-OK signals so the two color meanings don't collide.
- **Sidebar's Overview item and Topbar's Dashboard pill went
  inactive on `/capacity` and `/reliability`.** Both used
  `<NavLink to="/" end>`, which only matches the exact `/` route —
  but conceptually the dashboard is a single surface with three
  sub-tabs. Active state is now driven by `isDashboardPath()` from
  `apps/web/src/utils/routes.ts`, which checks the pathname
  against a centralized `DASHBOARD_PATHS` list. Future sub-tabs
  add to one place.

## [1.7.0] — 2026-05-01

Quality-of-life release focused on the day-to-day operator views: list
pages stop reordering on you, Node detail finally shows what's
allocated and who's eating it, and Cluster Map's Traffic layout
explains itself instead of going blank when there's nothing to draw.

### Added

- **Node detail: Allocation panel + Top consumers + schedulable
  indicator.** New section on the Node Overview tab shows the sum of
  pod requests and limits across allocatable for both CPU and memory,
  with bars colored by saturation (green < 75%, amber 75–90%, red ≥ 90%)
  and an overcommit overlay when limits exceed allocatable. Below it,
  a Top Consumers panel lists the 5 pods with the highest CPU and
  memory requests on the node. Plus a Pods count vs max-pods readout
  and Schedulable indicator (cordoned shows in amber). Answers the
  triage question "is this node full and who's behind it?" without
  shelling out to `kubectl describe node`.
- **Node detail: Pods tab.** Lists every pod scheduled on the node
  with the same CPU/Memory cells used elsewhere. Powered by a new
  `?node=` filter on `/resources/pods` that the agent-proxy and direct
  paths both support.
- **Click-to-filter on the Node column** in the Pods list. Hover any
  Node cell to reveal a filter icon; clicking scopes the list to that
  node via a `?node=` URL param. A chip below the filter bar shows the
  active filter and clears it. URL-shareable so a triage link to "pods
  on node X" can be sent over Slack without recreating the filter.
- **Sortable CPU and Memory columns** on Pods, Deployments,
  StatefulSets, DaemonSets, Jobs lists. Click the header to sort by
  absolute usage in millicores or bytes. The accessor matches what's
  rendered in the cell so the order doesn't lie.

### Changed

- **WebSocket informer events no longer broad-invalidate `['resources']`**
  on every change. On active clusters that was firing dozens of
  invalidations per second, drowning the user-configured refresh
  interval and causing visible mid-second list reorders. Lists now
  refresh on the configured cadence; targeted invalidation after
  explicit mutations (Kobi Execute, Scale / Restart / Delete) keeps the
  post-action freshness UX. Detail pages, cluster overview, and
  topology still WS-invalidate (the latter two debounced 2s).
- **Resource list sort defaults to name asc and persists per resource
  type** in localStorage. The backend also pre-sorts by name when no
  `?sort=` is given so paginated lists are stable across consecutive
  requests — without this, a single item could drift between page 1
  and page 2 because the informer cache is map-iterated.
- **Pagination control centered** below resource lists so the floating
  Kobi sigil no longer covers the Next-page button on small viewports.
- **Cluster Map Traffic layout** now distinguishes "agent missing" from
  "agent connected but no flows arriving" with two distinct CTAs, both
  centered on an empty canvas. Previously the second case rendered as
  a totally blank page with no explanation.
- **Workloads-by-namespace section** on the Overview dashboard sorts
  workloads alphabetically inside each namespace; without this they
  came out in random informer-cache order on every refresh. The header
  also now reads "Workloads by namespace" (was the half-Spanish
  "Workloads por namespace").

### Notes

A bug to be aware of (not fixed in this release): the workload Monitor
charts on Deployment / StatefulSet / DaemonSet detail pages multiply
limit / request reference lines by replica count but the data series
sums whatever VictoriaMetrics has — when the agent doesn't cover every
node carrying a replica (saturated node, NoSchedule taint, etc.), the
chart shows ample headroom while individual pods are at limit. Issue
tracked; defensive coverage-gap banner planned for the next release.

## [1.6.1] — 2026-04-30

Patch release with two changes: a relicense to Apache 2.0 (the standard
for cloud-infrastructure projects in the CNCF / Hashicorp / Kubernetes
ecosystem), and a `go vet` fix that was failing CI on `main` since
Sprint A.5 introduced an if-true scope idiom the lostat checker cannot
follow. v1.6.0 stays MIT — Apache 2.0 applies starting here.

### Changed

- **License: MIT → Apache 2.0.** Apache 2.0 brings explicit patent grant
  and patent retaliation, explicit trademark protection (the license
  does NOT grant rights to use the KubeBolt or Kobi names), and the
  procurement-friendly profile that virtually every enterprise legal
  team has pre-approved. The relicense covers files updated in this
  commit: `LICENSE` (full Apache 2.0 standard text + project copyright),
  new `NOTICE` file (per Apache 2.0 best practice), README badge and
  License section, both helm charts' `artifacthub.io/license`, the
  homebrew formula template, and the AboutModal UI. Prior commits
  remain under MIT (which permits the relicense); v1.6.0 binaries
  already in the wild stay MIT for their consumers.

### Fixed

- **`go vet` cancel-leak warning** in `apps/api/internal/cluster/connector.go`.
  The kube-system UID lookup wrapped its context in an `if ...; true { }`
  block intended to limit the variable scope, but `defer cancel()` runs
  at function exit regardless of block scope, so the construct was a
  no-op the lostat checker could not analyze. Replaced with a plain
  `cancel`/`defer` pair. Behavior identical, CI green.
- **Flaky `TestValidateAccessToken_RejectsTampered`** (~6% failure rate)
  in `apps/api/internal/auth/jwt_test.go`. The test flipped the last
  char of the JWT's base64url signature to invalidate it, but a base64url
  signature's last char carries only 4 meaningful bits + 2 filler bits,
  so distinct chars with the same 4-bit prefix decode to the same byte
  — flipping the last char to 'A' or 'B' fell into that equivalence class
  for 4 of 64 valid signatures. The test now flips a char in the middle
  of the signature, where all 6 bits are meaningful and any flip
  guarantees an invalid signature. Behavior of the validation code is
  unchanged; the test just stopped lying.

### Notes

The release CI pipeline gained two improvements during the v1.6.0
release attempt that landed in v1.6.0 itself but are worth flagging
for operators tracking the release infrastructure:

- `apps/api/Dockerfile` now expects the repo root as build context
  (because `apps/api/go.mod` has a replace directive into
  `../../packages/proto`). The release workflow uses `context: .` and
  `file: apps/api/Dockerfile` to match.
- `publish-chart` now gates on `build-api` and `build-web` succeeding
  so a failed image build no longer leaves a broken chart pointing at
  a non-existent image.

Both fixes are present in v1.6.0 (commit `19dcba4`).

---

## [1.6.0] — 2026-04-30

Feature release with three large-scale streams:

- **Kobi** — the AI Copilot becomes a named agent with a senior-SRE
  voice and a visual identity (Sigil + state-aware avatar).
- **Cluster mutations via the AI Copilot** — propose / confirm /
  execute pattern so the LLM can recommend state-changing actions
  without ever holding the cluster credential. Operator clicks
  Execute; mutation runs under the operator's RBAC role.
- **Agent-as-K8s-API-proxy** — the agent can tunnel arbitrary
  Kubernetes API requests (including SPDY upgrades for exec, files,
  port-forward) so KubeBolt operates on clusters where it has no
  direct control-plane access.

Plus per-tenant agent-ingest auth, real-time UX hardening, Cluster Map
flow-classification fixes, and prompt-cache cost optimizations.

No API breakage for the API/web surface. The agent protocol changed
(AgentChannel v2, KubeStreamData/Ack), so operators with deployed
agents must upgrade to **agent-v0.2.0+** — released on its own track
on 2026-04-29 and required for any A/B agent-channel functionality.

### Added

- **Cluster mutations via the AI Copilot** — propose / confirm / execute
  pattern. The LLM emits a structured `ActionProposal` payload as a tool
  result; the frontend renders it as an interactive card; the operator
  clicks **Execute** (or **Dismiss**); the existing mutation endpoint
  runs under the **operator's** RBAC role, never the LLM's. Closes the
  prompt-injection vector for state-changing operations. Four actions
  shipped:
  - `propose_restart_workload` — rollout restart (Deployment / STS / DaemonSet)
  - `propose_scale_workload` — scale to N replicas (0 to pause)
  - `propose_rollback_deployment` — `kubectl rollout undo`, default to previous
  - `propose_delete_resource` — destructive, irreversible, three stacked
    safeguards: blast-radius preview computed server-side (owned pods,
    services left without endpoints, orphaned HPAs, retained PVCs from
    StatefulSets, pods that mount the ConfigMap/Secret), typing-to-confirm
    for `risk=high`, RBAC Admin required at the endpoint. HPAs are part
    of the whitelist as of this release.
- **Real-time UX: WebSocket invalidates detail-page + topology queries**
  (Phase 1.10 — Real-Time UX Hardening). The WS handler used to invalidate
  only list views; now it also invalidates `['resource-detail', type, ns,
  name]` so detail pages reflect Pending → ContainerCreating → Running
  transitions instantly, plus topology graph invalidation debounced to
  2s to match the backend's rebuild cadence. Cluster-scoped resources
  match on `namespace='_'` (the placeholder the detail route uses for
  Nodes etc.).
- **Kobi — agent identity, voice, and visual mark** replaces "AI Copilot"
  - **Sigil**: a deconstructed K with an intelligence dot, four states
    (static / watching / investigating / awaiting). Color encodes state:
    emerald for idle, amber for active tool calls, sky for proposal
    pending operator decision. The avatar background tints in the same
    colour family at small render sizes so transitions are perceptible.
    Assets in `apps/web/src/assets/kobi/sigil/`.
  - **Three-layer system prompt** (~50KB rendered) embedded into the
    binary via `//go:embed`:
    - `kobi-identity.md` — core identity, voice principles, language
      mirroring, model-agnostic ("you are Kobi" — may acknowledge
      Anthropic Claude or OpenAI GPT only when asked directly).
    - `kobi-copilot.md` — Copilot-mode communication contract, with
      explicit rules for quantification, scope discipline on resource
      metrics, scannability, and the closing-investigation shape
      (mechanism → impact → options → pick).
    - `kobi-few-shots.md` — voice examples in canonical Kobi register
      plus anti-patterns (no marketing language, no performative warmth,
      no emoji warning markers, no vague impact claims).
  - **Operational appendix** preserves every existing Copilot capability
    (tool catalog, proposal whitelist, get_pod_logs intent heuristics,
    redaction policy, error handling) but rephrased without `⚠️` markers
    that conflict with Kobi's voice.
  - **Awaiting state wired** to pending action proposals (the operator
    has a card waiting for Execute / Dismiss). Sigil + avatar transition
    to sky tones; back to emerald after Execute or Dismiss.
  - **UI rebrand**: chat panel header, message avatars, toggle launcher,
    Ask Kobi buttons (Insights, Events, Resource Detail, External
    Endpoint, Cluster Map, Metric Charts), proposal card header, sidebar
    admin entry, About modal. Internal identifiers (component names,
    hooks, types, endpoint paths) unchanged — purely UX-visible.
- **Agent-as-K8s-API-proxy** (Sprint A.5)
  - The agent can now proxy arbitrary Kubernetes API requests back to
    the apiserver, so KubeBolt can drive clusters it has no direct
    access to. Backend wires this through `AgentProxyTransport` with a
    watch adapter and a `ClusterAccess` factory selecting between
    `local` and `agent-proxy` modes per cluster.
  - **SPDY upgrade tunneling** on the channel so exec, file browsing
    and port-forward all work end-to-end through the proxy. Connection /
    Upgrade headers now preserved on upgrade requests; SPDY tunnel
    plumbing in API exec/files/portforward; `CancelRequest` no-op
    silences a noisy client-go warning.
  - **Auto-registration** of agent-proxy clusters (opt-in via
    `KUBEBOLT_AGENT_PROXY_ENABLED`). Agent forwards its cluster name in
    `Hello.Labels`; backend suffixes display name with `(via agent)` so
    operators can tell at a glance.
  - **Multi-agent per cluster** support — the channel multiplexor now
    serves N agents per cluster (DaemonSets ship one per node).
  - **Operator-tier RBAC manifest** for agents that need full proxy
    access.
- **Agent ingest auth** (Sprint A)
  - Bearer-token authentication on the agent ingest gRPC channel, with
    optional mTLS and Kubernetes ServiceAccount projected-token review
    via the apiserver TokenReview API.
  - Per-tenant token storage in BoltDB with admin REST endpoints
    (`/admin/tenants/...`) for token CRUD.
  - **Admin UI** at `/admin/agent-tokens` for token issuance and
    revocation (single-tenant in OSS; multi-tenant flagged
    `ENTERPRISE-CANDIDATE`).
  - Helm chart values for auth + TLS + projected SA token mount on the
    agent side; tokenreviews RBAC + agent ingest service template on
    the API side.
  - End-to-end tests in `apps/api/internal/agent/e2e_*` that spin up a
    kind cluster and exercise the auth flow.
- **Per-tenant rate limiter** on agent ingest (token-bucket per tenant,
  flagged `ENTERPRISE-CANDIDATE`).
- **`propose_delete_resource`** now supports HPAs in addition to the
  prior whitelist.
- **Make targets** for agent deployment with auth (`agent-deploy-auth`)
  and a manifest template with the ingest-token mount.

### Changed

- **Kobi system prompt is byte-stable across requests** (Phase 6). The
  `clusterName` and `currentPath` are no longer interpolated into the
  system text — they're prepended to the operator's first user message
  via a new `BuildSessionContext` helper. Anthropic's prompt cache now
  hits the same prefix regardless of cluster, view, or operator,
  cutting `cache_creation` writes for warm sessions. Validated A/B on
  the demo cluster: cross-restart cache survival, view-switch tolerance,
  and reachable cache_write = 0 in optimal warm-cache conditions.
- **Voice tuning** during Kobi rollout (two rounds, captured in
  `kobi-few-shots.md` anti-patterns):
  - Quantify whenever you have the number — no "varias peticiones por
    segundo" when logs gave you "~15–20 req/s per pod".
  - Completeness on overview-shape requests — list every namespace, not
    a filtered "interesting" subset.
  - Range of options when proposing actions — three options ordered by
    impact including "do nothing", not a binary "yes/no".
  - Be explicit about scope in resource metrics — per-pod vs aggregate
    vs per-node, always labelled, and the math has to close.
- **Admin "Copilot Usage"** renamed to **"Kobi Usage"**. Same analytics,
  same per-session cost breakdown — just reflects the renamed agent.
- **Cluster auto-registration** suffixes agent-proxy cluster display
  names with `(via agent)` so operators can distinguish them from
  locally-configured clusters.
- **Topology query** consolidated into the standard `useResources` hook
  shape, gaining a 60-second `refetchInterval` fallback (in case the WS
  handler is disconnected) and `retry: 2` to survive transient backend
  hiccups. Real-time freshness still driven by the WS handler; the
  poll is the safety net.

### Fixed

- **Cluster Map: Hubble flow misclassification of pod IPs as external.**
  When Hubble dropped the destination identity (resolver race against pod
  restarts, identity propagation, or hostNetwork hops), pod-to-pod flows
  ended up in the synthetic "external" region. Now the backend recovers
  the mapping from cluster state already in memory (pod IPs from the
  lister, PodCIDR ranges from nodes): IP-matches-pod → rewrite as
  pod-to-pod; IP-in-PodCIDR-but-stale → drop; IP-outside-all-PodCIDRs
  → keep external, attach hostname from recent DNS if available. New
  `PodLister` / `NodeLister` accessors on the Connector for callers
  that need informer state without re-querying the API server.
- **Topbar z-index** raised so the cluster switcher and global search
  stay above admin overlays (was at z-200, same as `IntegrationDetailPanel`;
  now z-400).
- **Copilot config fetch race**: `/copilot/config` now waits for
  AuthProvider's silent-refresh init() before firing, so the chat
  toggle no longer requires a manual page refresh on first load when
  the auth token wasn't yet attached.
- **Agent-proxy SPDY tunnel** previously dropped Connection / Upgrade
  headers, breaking exec/files/portforward through the proxy. Headers
  now preserved end-to-end.
- **Agent integrations admin** pre-flights now check existing operator
  RBAC before re-creating, preventing duplicate ClusterRoleBindings on
  re-install.
- **Release workflow**: pre-release tags (`-rc`, `-beta`, `-alpha`) and
  agent-only releases no longer claim "Latest" on the repo's releases
  page.
- **`get_kubebolt_docs`** scope expanded to cover Kobi-rebranded surfaces
  (panel toggle, sigil states, awaiting transitions).

### Breaking — agent only

These changes affect operators with deployed agents only. The API/web
surface is unchanged for users without agents.

- **Protocol bump: AgentChannel v2** replaces AgentIngest v1
  (`feat(proto)!: AgentChannel v2`). A v0.1.x agent will not connect to
  a 1.6.0 backend.
- **`KubeStreamData` / `KubeStreamAck`** are new proto messages required
  for the SPDY upgrade tunnels (`feat(proto)!: KubeStreamData + KubeStreamAck`).
- **Agent v0.2.0** ships these protocol changes plus 3-tier RBAC
  (metrics / reader / operator) and the OSS distribution shape (helm
  chart + raw manifest). Released independently on the `agent-v*` tag
  track on 2026-04-29.

**Upgrade path:** bump backend to 1.6.0 + update agent images to
`agent-v0.2.0` (or later). Helm chart bumps both in lock-step.

### Known issues (carried forward)

- `/cluster/overview` still returns zero for `health.insights.*` counters.
- Tool-result JSON truncation is still byte-aligned.

### What's next

- Phase 4 of Kobi launch — inline 👍/👎 feedback per response — deferred
  until production traffic is meaningful.
- Multi-user / multi-cluster cache sharing benefit of Phase 6 not yet
  measured (single-user A/B only); production usage will quantify.

---

## [1.5.1] — 2026-04-22

Patch release focused on making auto-compact reliable. The 1.5.0 trigger
sometimes failed to fire on follow-up questions, and when it did fire it
often freed little because the bulky tool_results lived in the preserved
tail. A third bug could truncate a Copilot response when a mid-request
compact stubbed the active turn's tool_results before the LLM had
processed them.

### Fixed

- **Auto-compact trigger misses on round 0 of follow-up questions.**
  The backend underestimated context size because `ApproxTokens(messages)`
  ignored the system prompt plus tool definitions (~15–20k tokens) and
  was loose on JSON-dense tool results. The frontend now carries the
  provider-reported usage of the previous round as a hint; the backend
  seeds its check from that value so round 0 sees the same number the
  UI shows. A server-side system-and-tools overhead estimate closes the
  gap when the hint is absent.
- **Compacts that freed nothing ("1 turn folded · 25k → 25k").** The
  bulky tool_results lived in the preserved tail, not in the folded
  history, so folding old turns alone rarely saved much. Compact now
  stubs tool_results in the preserved tail too. The LLM keeps user
  text, assistant text and tool_call metadata intact and can re-fetch
  raw data on demand.
- **Response truncated after mid-request compact.** A compact fired
  between rounds could stub the active turn's tool_results before the
  LLM had synthesized them, producing responses like "the output was
  truncated." `stubToolResults` now protects every message from the
  last user-text onward. Two regression tests lock this in.

### Added

- `CompactResult.ToolResultsStubbed` plus SSE/REST exposure so the UI
  banner and the admin usage drill-down can explain what the compact
  did, not just "N turns folded".
- Auto-compact falls back to stubbing tool_results in earlier turns
  when there aren't enough turns to fold. Short conversations with a
  heavy tool call now benefit too.
- Handler logs `copilot auto-compact noop` with a reason when neither
  folding nor stubbing applies, ending the "triggered without applied"
  mystery in the logs.

### Known issues (unchanged from 1.5.0)

- `/cluster/overview` still returns zero for `health.insights.*`.
- Tool-result JSON truncation is still byte-aligned.

---

## [1.5.0] — 2026-04-22

Major release centered on production-ready AI Copilot, multi-channel
notifications, admin analytics and a test suite that covers both
backend and frontend.

### Added

- **AI Copilot — conversation memory**
  - Auto-compact folds older turns into a summary when the context
    approaches the configured budget × threshold (80% default), using
    the provider's cheap-tier model (Haiku 4.5 / gpt-4o-mini) so the
    compaction itself barely spends tokens.
  - Manual "new session with summary" (scissors icon in the panel)
    folds the entire transcript into a single summary so the user can
    pivot topics without losing context.
  - Tool history now persists across turns — the backend returns the
    full messages array on `done` and the frontend replaces its state
    with it. Matches the accumulative context model documented by
    Anthropic and OpenAI.
  - Session counter in the panel shows the real input size reported by
    the provider (input + cache reads + cache creations), not a
    client-side approximation.
  - Transcript auto-clears on a successful cluster switch — prior
    conversation wouldn't apply to the new cluster's resources.
- **AI Copilot — product knowledge base**
  - New `get_kubebolt_docs` tool with ~25 hand-curated topics covering
    UI surfaces, admin pages, configuration and keyboard shortcuts.
  - Tool description inlines the topic list so the LLM discovers keys
    without a round-trip; fuzzy-match fallback for off-by-one keys.
- **AI Copilot — scope guardrail**
  - System prompt now defines in-scope (Kubernetes, DevOps/SRE
    supporting cluster ops, KubeBolt) vs out-of-scope topics.
  - Out-of-scope questions get a one-sentence polite refusal with a
    redirect, never a partial answer.
- **AI Copilot — contextual triggers**
  - "Ask Copilot" buttons added to Insight cards, Resource Detail
    headers (Pods, Deployments, StatefulSets, Services, Nodes), and
    Warning events in the Events page.
  - Each button pre-loads a template prompt with the relevant context
    (cluster, namespace, name, symptom) and launches the Copilot panel.
- **AI Copilot — multi-provider support & caching**
  - GPT-5 / o-series compatibility via `max_completion_tokens`.
  - Anthropic prompt caching (`cache_control: ephemeral`) on system
    prompt + tool definitions.
  - OpenAI automatic caching parsed from `prompt_tokens_details.cached_tokens`
    and normalized to the Anthropic convention.
  - Fallback provider with auto-retry on 429 / 5xx.
- **Admin — Copilot Usage analytics** (`/admin/copilot-usage`)
  - Tiles: sessions, tokens billed + cache hit %, estimated USD cost
    (list-price per provider/model), avg duration + compact counts.
  - Stacked-bar timeseries chart (cache read / fresh input / output).
  - Top tools chart with call counts and error rates.
  - Recent sessions table with drill-down modal showing tool
    breakdown and compact events.
  - BoltDB-backed retention: 30 days / 5000 entries cap.
  - Configurable range (24h / 7d / 30d) with refresh button.
- **Admin — Notifications global settings**
  - Master enable/disable toggle.
  - Base URL for deep links in messages.
  - Resolved-insight alerts.
  - Email digest modes: instant / hourly / daily.
- **Structured logging**
  - `log/slog` JSON logger with daily rotation.
  - Per-session copilot logs include token breakdown, tool calls, tool
    bytes, duration, fallback flag and compaction events.
- **Testing**
  - 86 Go backend tests covering copilot, auth, config, insights,
    notifications and admin API handlers (race detector enabled in CI).
  - 14 frontend tests under vitest + jsdom for trigger prompt builders,
    suggestion generator and compact endpoint serialization.
  - CI now runs `go vet`, `go test -race` and `npm test` before build.

### Changed

- Context window sizes corrected: Claude Sonnet 4.6 and Opus 4.6/4.7 now
  report their true 1M-token budgets.
- AskCopilotButton refreshed with gradient + ring + hover glow to
  distinguish the AI entry points from the standard UI chrome.
- Admin pages (Clusters, Users, Notifications) use the full content
  width with a 3-column notification channel grid.
- Pre-release tags (`-rc`, `-beta`, `-alpha`) are auto-detected in the
  release workflow so the GitHub Release is correctly marked.

### Fixed

- Copilot chat panel: assistant messages no longer overflow the panel
  on long responses.
- Input textarea returns focus automatically after the Copilot finishes
  responding, so the user can keep typing without clicking.
- Conversation memory: removed tool-result elision (which was actively
  invalidating provider prompt caches) in favor of compaction. Measured
  sessions saw ~2× reduction in billed tokens once the caches stay warm
  across questions.
- Usage store: prune by cap now iterates the cursor instead of relying
  on `b.Stats()`, which didn't reflect pending writes inside a
  transaction and left the bucket one entry over the cap.

### Known issues

- `/cluster/overview` currently returns zero for `health.insights.*`
  counters because `Connector.GetOverview` doesn't pass the active
  insights through `GetHealth`. Pending for a follow-up release.
- Tool-result JSON truncation is byte-aligned; a structure-aware
  truncator is on the roadmap.

---

## [1.4.0] — 2026-03-28

Previous stable release. See git history for commits in range
[`v1.3.0..v1.4.0`](https://github.com/clm-cloud-solutions/kubebolt/compare/v1.3.0...v1.4.0).

## Earlier

Release history for 1.0.x through 1.3.0 is available in the GitHub
releases page: <https://github.com/clm-cloud-solutions/kubebolt/releases>.
