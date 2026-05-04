# Changelog

All notable changes to KubeBolt are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
