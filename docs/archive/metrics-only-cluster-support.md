# Metrics-only clusters — a missing first-class mode (HIGH-PRIORITY SaaS gap)

> **Status: HIGH-PRIORITY gap — found 2026-06-28 (sucal pre-E1 benchmark).** A cluster
> that ships metrics but does NOT enable the agent-proxy currently has **no usable UI**.
> Many SaaS users will want monitoring-only (no operator RBAC) → showstopper for that
> segment. Pre-E1 SaaS-readiness backlog.

## What happens today (confirmed in code)

The agent advertises the `kube-proxy` capability in its Hello **only when**
`KUBEBOLT_AGENT_PROXY_ENABLED=true` (i.e. `rbac.mode=reader|operator`) —
`packages/agent/cmd/agent/main.go:274-288`. With `rbac.mode=metrics` the proxy is off
and the agent advertises only `["metrics"]`.

The backend adds a cluster to the cluster manager (`AddAgentProxyCluster`) **only when
the agent has the `kube-proxy` capability** — `apps/api/internal/agent/auto_register.go:94`
(`if !hasCapability(capabilities, "kube-proxy") { return }`). Consequences:

- **Fresh metrics-only agent** (never had kube-proxy) → cluster is **NOT added to the
  manager** → **does not appear in the cluster selector**. Its metrics DO land in VM
  (the ingest path is independent of the proxy), but there is **no way to navigate to
  the cluster** in the UI.
- **Downgraded agent** (was operator, now metrics — e.g. the sucal UTF-8 workaround) →
  cluster registration is **durable**, so it stays registered as agent-proxy. The
  backend's connector then tries to sync resources via the now-absent proxy →
  `WaitForCacheSync` times out → **503 "timed out waiting for cache sync — cluster may
  be unreachable"** → the cluster shows **Error**.

**Either way, a metrics-only cluster is unusable in the UI.**

## Why this matters (the concern is correct)

A large segment of users will want **monitoring only** — ship metrics but NOT grant
the agent the cluster-wide read/write RBAC the proxy needs (security / change-
management). Today KubeBolt gives them an invisible cluster or an Error. For the SaaS
this is a real adoption blocker, and it conflates two orthogonal things:
**"the cluster ships metrics"** vs **"the cluster's live resources are reachable via
the agent-proxy."** The UI treats the absence of the second as the cluster being
*unreachable* — which is wrong.

## The fix (product direction)

Make **metrics-only a first-class cluster mode**:

1. **Surface the cluster even without kube-proxy.** When an agent registers with only
   `metrics`, still list it in the selector (from the agent registry / metrics
   presence), flagged **monitored-only**.
2. **Don't start the resource-sync connector** for a metrics-only cluster — no
   `WaitForCacheSync`, no Error. The informers-via-proxy connector is only for
   kube-proxy-capable agents.
3. **UI degradation, not failure.** For a metrics-only cluster:
   - **Show** the metrics dashboards (Capacity / time-series — they query VM directly,
     no connector needed) + node/agent coverage.
   - **Disable / hide** the resource views (Overview resource counts, resource lists,
     Map, kubectl-ops, Kobi) with a clear note: *"This cluster is monitored-only.
     Enable the agent-proxy (`rbac.mode=reader|operator`) for resource views and
     operations."*
4. **Handle the live downgrade.** When an agent drops `kube-proxy` (operator →
   metrics), the backend should downgrade the cluster from agent-proxy to metrics-only
   (tear down the connector, flip the UI mode) instead of leaving a failing connector.

## Scope & phasing — Phase 1 (launch) + Phase 2 (post-launch)

> Added 2026-06-30 after a design review. The 4-point direction above is the backbone;
> this section pins HOW FAR metrics-only goes, WHY, and the exact UI.

### Why metrics-only is its own tier (not a degraded reader)

There are three connection models, and the customer's **constraint** picks one — it
isn't a free feature choice:

| Model | How resources are read | Requires |
|-------|------------------------|----------|
| **direct-connect** (kubeconfig) | backend talks to the API server directly | a **reachable/public** API + creds |
| **agent-proxy** (reader / operator) | agent with kube-proxy tunnels API calls | cluster-wide read (or write) RBAC + the agent |
| **metrics-only** | agent ships only metrics; no API path | nothing beyond metrics (minimal footprint) |

metrics-only's niche is the customer with a **private API** who **can't/won't grant
cluster-wide RBAC** (or whose network blocks the proxy). For them reader simply isn't
available — metrics-only is the best KubeBolt can do, not a choice *against* reader.

**The hard line is the data source, and it does NOT blur.** KSM/VM expose *state and
counters* (`kube_pod_status_phase`, `kube_deployment_status_replicas`, node/pod usage,
Hubble L7), NOT *objects* — there is no spec, YAML, logs, events, exec, or live API
through metrics:

| Capability | metrics-only (KSM/VM) | reader (proxy / direct) |
|-----------|:---:|:---:|
| Dashboards (Capacity / Reliability), counts, CPU/Mem, health | ✅ from KSM | ✅ |
| Basic list (name / namespace / status) | ⚠️ derivable from KSM | ✅ live, full |
| Spec / YAML / logs / describe / events | ❌ | ✅ |
| Exec, full topology Map | ❌ | ✅ |
| Kobi: metrics RCA + knowledge base | ✅ | ✅ |
| Kobi: resource inspection + proposed actions | ❌ | ✅ / operator |
| Write actions (restart / scale / delete / apply) | ❌ | ❌ / operator |

For the "I want full live views but won't run the proxy" case, the clean answer is
**direct-connect against a public API** — NOT rebuilding reader out of KSM. So Phase 1
keeps metrics-only lean and *points* users at one of the two real upgrade paths.

### Phase 1 — ships for launch (lean monitoring tier)

**Works, from VM/KSM, with NO connector:**

- **Overview WITH data** — resource counts + CPU/Mem + cluster health derived from KSM
  metrics (not a "monitored-only" placeholder). The user opens the cluster and sees real
  numbers.
- **Capacity + Reliability** — the time-series dashboards already query VM directly; the
  fix is to **degrade instead of hard-failing**. Today both call `useClusterOverview()`
  and `if (error) return <ErrorState/>` (CapacityPage ~L92, ReliabilityPage ~L74), so the
  overview's 503 kills them. They must render the charts and skip the overview-dependent
  extras (OverviewHeader, request/limit threshold lines, deploy markers — those need the
  connector).
- **Kobi in metrics-analysis mode** — queries metrics + its knowledge base and does
  metrics-driven RCA; its resource-inspection tools (YAML / logs / describe / detail) and
  action proposals are disabled (they need the API).

**Disabled (needs the API server — proxy or direct-connect):** resource detail tabs
(YAML, Logs, Describe, Events, Terminal, Files), the topology Map, kubectl-style actions,
Kobi resource inspection + actions.

**Backend (Phase A — DONE):** `AddMetricsOnlyCluster` + `metricsOnlyContexts`,
`ClusterInfo.Mode="metrics-only"`, connector-skip in `connectToContextLocked`,
`requireConnector` monitored-only signal, `MetricsOnlyClusterID` for VM scoping,
boot-restore. **Still TODO for Phase 1:** make `/cluster/overview` return data for a
metrics-only cluster by deriving counts + health from KSM in VM instead of 503-ing —
verify first that the agent actually ships the needed `kube_*` series.

**Frontend (Phase B — partly done; rest is Phase 1's remaining work):** land on
**Overview (with data)** for metrics-only (the user wants Overview, not a notice);
Capacity / Reliability degrade gracefully; **don't empty the sidebar** — DIM the
resource-view items (reuse the existing limited-access / shield pattern) so the menu
stays familiar and it's clear *what* is disabled and *why*.

### Phase 1 UI notices — incentivize ONE of the two upgrade paths

Every disabled/degraded surface must nudge toward a real path, not just say
"unavailable". State both paths explicitly, everywhere:

> **For live resource views & operations, either:**
> **1) expose the cluster's API** and connect it directly, **or**
> **2) enable the KubeBolt agent-proxy** (`rbac.mode=reader` for read, `operator` for
> read + write).

Placement:

- A persistent **monitored-only banner** on the cluster (Overview / cluster header) with
  the two-path message.
- A **per-tab notice** on each resource-detail tab that needs the proxy (YAML, Logs,
  Describe, Terminal, Files) — the tab is visible but shows the notice instead of content.
- A **tooltip** on each dimmed sidebar item ("monitored-only — needs the agent-proxy or a
  direct API connection").
- A **Kobi note** that it's in metrics-analysis mode (can analyze metrics + docs, can't
  inspect resources or propose actions here).

### Phase 2 — DEFERRED to after launch (KSM-derived resource views)

Build only if the constrained segment (private API + no RBAC) proves large enough:

- **Read-only basic resource lists** from KSM (name / namespace / status / age / restarts).
- **Read-only detail** for the fields KSM carries; proxy-only tabs stay notice-gated.
- Kobi could read those KSM basics; still no actions.

Why it's Phase 2, not Phase 1:

- It rebuilds a **partial, degraded reader** out of metrics — high effort + ongoing
  maintenance as KSM coverage varies by cluster.
- **UX ambiguity** — some fields appear, others don't; harder to explain than a clean
  "this needs the proxy" line.
- The clean full-feature path already exists (direct-connect / agent-proxy), so Phase 2
  is a per-segment optimization, **not a launch blocker**.

## Relationship to the other items

- This gap was **exposed** by the [UTF-8 marshal bug](./agent-utf8-marshal-bug.md):
  the workaround (`rbac.mode=metrics`) to stop the agent crash forced sucal into the
  metrics-only path, which then showed Error.
- **Independent of that bug** — even with the UTF-8 fix, a user who deliberately
  chooses metrics-only still hits this gap. **It needs its own fix.**

## sucal right now

Blocked for a usable UI view until either fix ships: the **UTF-8 fix (agent 1.1.5)**
restores operator mode (full views + ops), or **this metrics-only mode** gives a
monitored-only view. Its metrics are safe in VM regardless (the agent is stable on
`rbac.mode=metrics`).

## Related lifecycle gap — re-register is Hello-triggered, not reconciled

The backend re-registers an agent-proxy cluster ONLY on a fresh agent Hello
(`server.go:358 maybeAutoRegisterCluster`, in the registration flow — "kube-proxy
capable Hello triggers AddAgentProxyCluster", `server.go:139`). There is **no periodic
reconciliation** (the only ticker in the agent package is the SPDY tunnel
idle-watchdog). Consequences:

- Deleting a cluster from KubeBolt while its agent is **stably connected** does NOT
  re-add it (no new Hello) — the delete sticks *until the agent next reconnects*.
- On the next reconnect (network blip, agent restart, or a flapping loop) the cluster
  **resurrects** via the Hello.
- Observed: during the UTF-8 reconnect-loop the cluster re-registered every ~60s (each
  reconnect = a Hello); once the agent stabilized it stopped; restarting the agent
  brought it back.

So a delete is effectively **soft** (undone by the next reconnect). For SaaS this is
inconsistent — a deliberate delete should persist the teardown durably, or the UI
should signal that the cluster reappears while the agent runs.

## Code locations

- `packages/agent/cmd/agent/main.go:274-288` — agent advertises `kube-proxy` only when
  `KUBEBOLT_AGENT_PROXY_ENABLED=true`.
- `apps/api/internal/agent/auto_register.go:94` — backend registers as agent-proxy
  only when `hasCapability("kube-proxy")`.
- `apps/api/internal/agent/server.go:585,601` — proxy-vs-metrics capability split.
- `apps/api/internal/cluster/manager.go` — connector start + `WaitForCacheSync` (the
  timeout that surfaces as the 503 / Error).
