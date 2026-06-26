# kubebolt-agent changelog

The agent ships on its own cadence — tag pattern `agent-vX.Y.Z`.
GitHub Actions builds and publishes the multi-arch image to
`ghcr.io/clm-cloud-solutions/kubebolt/agent` and the Helm chart to
`oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent` on
each tag.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.1.4] — 2026-06-27

A reliability fix for large clusters — same v1.0 metric/label schema, no behavior
change otherwise. Drop-in within the 1.1.x line.

### Fixed

- **Raised the AgentChannel gRPC max message size from gRPC's 4 MiB default to
  64 MiB** (override with `KUBEBOLT_AGENT_MAX_MSG_BYTES`), on **both** the agent
  client (`MaxCallRecvMsgSize`/`MaxCallSendMsgSize`) and the backend server
  (`MaxRecvMsgSize`/`MaxSendMsgSize`). On large production clusters the apiserver
  responses the backend proxies over the channel exceed 4 MiB, failing the recv
  with `ResourceExhausted: received message larger than max` and tearing down the
  **whole** session — metrics included — in a reconnect loop. Surfaced on a
  multi-node prod EKS during pre-E1 benchmarking.

### Compatibility

- Compatible with KubeBolt backend **>= 1.13.0** (same as 1.1.x); the matching
  backend-side limit ships with api **>= 1.16.x**.
- Tag pattern remains `agent-vX.Y.Z`. This release: `agent-v1.1.4`.

## [1.1.3] — 2026-06-26

A security-patch release on the **same v1.0 metric/label schema** — no
behavior change. Drop-in within the 1.1.x line.

### Security

- **Bumped `golang.org/x/crypto` v0.47.0 → v0.52.0 and
  `golang.org/x/net` v0.49.0 → v0.55.0**, clearing the same 16 HIGH
  CVEs that gated the kubebolt 1.16.0 api image (9 `x/crypto/ssh`, 7
  `x/net` HTML-parse/idna/http2). `go mod tidy` also moved
  x/sys/x/term/x/text forward.
- **The agent release pipeline now Trivy-scans the agent image by
  `@sha256` digest** (`CRITICAL,HIGH`, `--ignore-unfixed`) before the
  chart is published or the GitHub Release is cut — the same gate the
  kubebolt product images already had. A vulnerable agent image can no
  longer ship.

### Compatibility

- Compatible with KubeBolt backend **>= 1.13.0** (same as 1.1.x).
- Tag pattern remains `agent-vX.Y.Z`. This release: `agent-v1.1.3`.

## [1.1.2] — 2026-06-26

A reliability-only release on the **same v1.0 metric/label schema** — no
UI-visible behavior change. Stays in the **1.1.x generation**; drop-in
upgrade from 1.1.0 / 1.1.1.

### Fixed — connection & identity reliability

- **Bounded reconnection** — the gRPC shutdown is time-boxed and the
  connector retry is resilient, so a stuck teardown can't wedge a
  reconnect.
- **Reconnect backoff capped at 3s** after a recently-healthy session —
  it no longer escalates the backoff after a brief blip.
- **Stable agent id** — derived deterministically so reconnections stop
  accumulating ghost records in the backend agent registry.
- **kube-system UID read retried** before falling back to `"local"` —
  more stable cluster identity on boots behind a slow apiserver.

### Changed — memory defaults

- **Homologated memory defaults** across all install paths, with
  `GOMEMLIMIT` derived automatically to drive the Go scavenger.

### Compatibility

- Compatible with KubeBolt backend **>= 1.13.0** (same as 1.1.0).
- Tag pattern remains `agent-vX.Y.Z`. This release: `agent-v1.1.2`.

## [1.1.1] — 2026-06-14

### Changed — security

- **Bundled vmagent → `v1.145.0-scratch`** — moves the scrape sidecar
  to VictoriaMetrics' application-binary-only scratch variant (no OS
  packages, no OpenSSL), matching the backend's bundled VictoriaMetrics
  pin and clearing the Go-stdlib / OpenSSL Trivy findings. No schema or
  behavior change.

## [1.1.0] — 2026-05-28

The 1.13 cycle's flagship — agent gains **Mode C** (reads metrics
from a customer-managed Prometheus via `/api/v1/query_range`),
shipped with the three managed-cloud auth providers needed to
target the major Prom-as-a-service offerings out of the box. The
backend that consumes this metric stream releases as KubeBolt
1.13.0; the agent ships independently per its own cadence.

### Added — Mode C + managed-cloud auth

- **Mode C — `internal/promread/`** — agent-side collector that polls
  the customer's existing Prometheus via `/api/v1/query_range` and
  forwards the converted samples through the same AgentChannel as
  Mode A. Closes the 1.13 cycle's "Universal Data Plane Mode C"
  Phase 6 design: lets customers on AMP / Azure Monitor managed Prom
  / GMP (where Prom is query-only and `remote_write` outbound isn't
  an option), or whose change-management process forbids editing
  their Prom config, run KubeBolt without losing visibility.
- **Six auth providers** under a single `Provider` abstraction in
  `internal/promread/auth.go`:
  - `none` / `basicAuth` / `bearer` — self-managed Prom (S1).
  - `gcpIam` (`auth_gcp.go`) — OAuth tokens via
    `golang.org/x/oauth2/google.DefaultTokenSource` against the GKE
    Workload Identity metadata server. Targets GMP.
  - `awsSigV4` (`auth_aws.go`) — signs each `query_range` with SigV4
    against the configured `awsRegion`, using credentials from the
    IRSA-bound KSA. Targets AMP.
  - `azureWorkloadIdentity` (`auth_azure.go`) — exchanges the
    federated token file injected by the AKS Workload Identity
    webhook for a bearer token scoped to
    `https://prometheus.monitor.azure.com`. Targets AMW.
- **NodeStress collector** (`internal/collector/nodestress.go`) —
  reads `node_load{1,5,15}` and `node_pressure_*_waiting_seconds_total`
  directly from `/proc/loadavg` and `/proc/pressure/`. Required for
  GMP coverage parity because GMP managed collection does NOT scrape
  node-exporter.
- **K8sNodeIndex** (`internal/promread/nodes.go`) — maps node
  `InternalIP` → node name on a 5min refresh ticker. Used by Convert
  to stamp `node=<k8s-node-name>` on samples whose `__name__` starts
  with `node_`, so the UI's Node Monitor panels (which filter by
  `node`) populate correctly. Adds a `nodes` (list) verb to the
  metrics-tier ClusterRole.
- **`K8sNodeIndex.IsKnownNode`** — name-fallback for AMW's
  `instance=<vmss-name>` shape (Azure auto-stamps the VMSS name
  rather than `<pod-IP>:<port>`).
- **Lease-elected single-writer** (`internal/promread/leader.go`,
  Lease name `kubebolt-promread`) — guarantees only one pod polls
  the customer's Prom at a time even if the Deployment is scaled
  past `replicas=1`. Mirrors `internal/flows/`'s pattern; separate
  Lease name so flows + promread elect independently.
- **`KUBEBOLT_AGENT_MODE`** env var — gates which collector pipelines
  run inside this pod. `daemonset` skips promread; `promread` skips
  kubelet collectors + Hubble; unset/`both` runs everything (legacy
  single-pod dev). The chart sets this explicitly on each template.
- **`KUBEBOLT_AGENT_PROMREAD_*` env vars**: `ENABLED`, `URL`,
  `AUTH_MODE`, `BASIC_AUTH_USERNAME`, `BASIC_AUTH_PASSWORD`,
  `BEARER_TOKEN`, `POLL_INTERVAL`, `STEP`, `LOOKBACK`, `MATCHERS`
  (newline-separated). Matchers default to a surgical set fetching
  ONLY what Mode A doesn't synthesize (`kube_pod_*`, `node_load*`,
  `node_pressure_*`, `node_disk_*`, `node_network_*_errs_*`, `up`,
  `process_*`).
- **`kubebolt_promread_leader` gauge** — emitted by every promread
  pod (value 1 when leading, 0 when standby) so dashboards see who
  holds the Lease without needing to consult the K8s API.

### Changed

- **Default `--buffer` flag value** bumped 10,000 → 50,000. Defence
  in depth against bursty ingest; the per-matcher push pattern in
  promread keeps any single Push call under ~10k regardless.
- **Topology** — the agent is no longer a single DaemonSet. When
  `agent.promRead.enabled=true`, the chart renders a second workload:
  a `Deployment` (`replicas=1`, `kubebolt.dev/role=promread`) that
  runs the promread reader in isolation. The DaemonSet keeps doing
  Mode A on every node. The two pods don't share a buffer.
- **Default matchers** are explicit metric-name lists, not
  `{__name__=~"..."}` regex. GMP rejects regex on `__name__` and
  would have silently returned zero samples on GCP installs.
- **`agent.deferNodeStress`** chart value gains a third auto-trigger:
  Mode C with `auth.mode=azureWorkloadIdentity` automatically
  suppresses the in-agent NodeStress collector because Azure's
  `ama-metrics-node` always scrapes node-exporter. Operators on AMW
  no longer need the `--set agent.deferNodeStress=true` incantation.

### Fixed

- **Multi-node leader-pod silent drop**: clusters running promread
  in the original DaemonSet topology silently dropped the leader
  pod's kubelet samples. The leader pod's shared ring buffer
  overflowed at ~30k samples/min because it carried 6× the workload
  of follower pods (Mode A locally + promread cluster-wide).
  Symptom: missing rows in the Filesystem per-node chart for
  whichever node was the current leader. Resolved by the topology
  split + per-matcher push + buffer bump described above.
- **Eviction loop** when DaemonSet + promread Deployment pods both
  registered with the same `cluster_id`. Added a mode-label
  discriminator so they register as distinct agents under the same
  cluster.
- **GMP regex rejection** on `__name__` selectors — addressed via
  the default matchers change above.
- **Hubble flow collector** gracefully skips when
  `KUBEBOLT_AGENT_MODE=promread` (no Mode A pipeline → no flows to
  emit).

### Compatibility

- Compatible with KubeBolt backend **>= 1.13.0** (preferred — picks
  up the promread leader gauge + cross-backend integration probe).
  Works with 1.10.0 - 1.12.x backends but the Prometheus (read)
  integration card will not render on those.
- Tag pattern remains `agent-vX.Y.Z`. This release: `agent-v1.1.0`.

## [1.0.0] — 2026-05-09

**Breaking release.** Aligns every metric and label the agent emits
to **Prometheus convention K8s** (the de-facto schema of cAdvisor +
kube-state-metrics + node-exporter + Hubble). This is Phase 1 of the
Universal Data Plane Plan — KubeBolt's strategic move to a single
canonical schema across every ingestion path (agent, future Prom
remote_write receiver, future OTLP receiver).

**No dual emission.** v1.0 ships only the canonical names; v0.x and
v1.0 cannot coexist on the same VictoriaMetrics — operators upgrade
the agent helm chart and the corresponding KubeBolt backend in
lockstep. KubeBolt >=1.10.0 logs a `WARN` on registration when an
agent below 1.0 connects (legacy schema = empty dashboards).

### Compatibility

This release **requires KubeBolt >= 1.10.0**. Older KubeBolt versions
query the legacy schema and will render empty dashboards for the
clusters this agent is shipping from. Full matrix and migration steps:
[`docs/COMPATIBILITY.md`](../../docs/COMPATIBILITY.md).

### Migration

```bash
# 1. Bump KubeBolt FIRST (1.10.0+ ships the v1.0 query consumers).
helm upgrade kubebolt oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt \
    --version 1.10.0 \
    -n kubebolt --reuse-values

# 2. Bump the agent in lockstep, in every cluster connected to it.
helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \
    --version 1.0.0 \
    -n kubebolt-agent --reuse-values
kubectl rollout status ds/kubebolt-agent -n kubebolt-agent
```

For dev clusters using local-built images, see CLAUDE.md's
`make agent-image` + `kind load docker-image` flow.

### Changed (breaking schema rename)

- **Pod-scope labels migrate to canonical Prom convention.**
  `pod_namespace` → `namespace`, `pod_name` → `pod`. The
  enrichment-only label `pod_uid` keeps its name.
- **Per-container CPU usage gauge removed.**
  `container_cpu_usage_cores` (derived gauge) is no longer emitted
  — the agent ships only the `*_seconds_total` counter and the
  backend / UI compute rates with `rate(...[Xm])`. Same change for
  `node_cpu_usage_cores` → use `rate(node_cpu_usage_seconds_total[Xm])`.
- **Memory metrics align to cAdvisor canonical names.**
  `container_memory_rss_bytes` → `container_memory_rss`. The two
  page-fault counters `container_memory_page_faults_total` and
  `container_memory_major_page_faults_total` collapse into a single
  `container_memory_failures_total{failure_type=pgfault|pgmajfault, scope=container}`.
- **Pod network labels switch to cAdvisor convention.**
  `pod_network_*_bytes_total` → `container_network_*_bytes_total`
  with `container=""` for pod-level rows (same series cAdvisor emits
  for the pause container that owns the pod's network namespace).
  Per-container rows are also emitted; dashboards filter
  `container=""` for the pod-level view, accepting the 5x
  cardinality trade-off for canonical compatibility.
- **Volume metrics align to kubelet convention.**
  `pod_volume_used_bytes` etc. → `kubelet_volume_stats_used_bytes`,
  `_capacity_bytes`, `_available_bytes`, `_inodes`, `_inodes_used`,
  `_inodes_free`. Label `pvc_name` → `persistentvolumeclaim`. Only
  PVCs are reported now; emptyDir / configMap / secret volumes are
  out of scope (kubelet canonical doesn't expose them).
- **Hubble flow labels switch to source_/destination_ prefixes.**
  `src_namespace` → `source_namespace`, `src_pod` → `source_pod`,
  `dst_namespace` → `destination_namespace`, `dst_pod` →
  `destination_pod`, `dst_ip` → `destination_ip`. Aligns with the
  Hubble exporter, Istio, and Linkerd telemetry naming so future
  service-mesh sources interleave cleanly. The aggregator's
  `verdict=dropped` bypass (added in 0.2.1) is unchanged.
- **Node network label `interface` → `device`** (node-exporter
  convention) for `node_network_*_bytes_total`.
- **`node_cpu_capacity_cores`, `node_load_average_*m`, `node_uptime_seconds`,
  `node_imagefs_used_bytes`, `container_processes`, `container_threads`,
  `container_file_descriptors` removed.** Phase 2 reintroduces them
  via the vmagent sidecar scraping node-exporter and cAdvisor
  directly; v1.0 trims to what kubelet's `/stats/summary` plus
  `/metrics/cadvisor` actually expose without expanding scrape
  surface.

### Added

- **`kubebolt_agent_*` self-metrics** (Phase 1 Day 4, commit `2c9bf3d`).
  The agent is now observable in the same dashboard as the rest of
  the cluster:
  - `kubebolt_agent_samples_collected_total` (counter)
  - `kubebolt_agent_samples_dropped_total` (counter)
  - `kubebolt_agent_buffer_size_current` (gauge)
  - `kubebolt_agent_buffer_size_max` (gauge)
  - `kubebolt_agent_memory_bytes` (gauge — `runtime.MemStats.Alloc`)
  - `kubebolt_agent_goroutines` (gauge)
  - `kubebolt_agent_info{agent_version="..."}` (gauge=1, identity marker)
- **`agentVersion` constant in `cmd/agent/main.go` set to `1.0.0`**
  (was `0.0.7-cluster-ident`). Reported in the gRPC Hello and as
  the `agent_version` label of `kubebolt_agent_info`. Backend uses
  semver comparison vs `MinAgentVersion = "1.0.0"` to log a WARN
  when an older agent connects.
- **Helm chart 0.2.2 → 1.0.0** (chart version + appVersion).

### Reference (Phase 1 commits)

| Day | Commit | Scope |
|---|---|---|
| 1 | `373ef20` | `stats.go` — namespace/pod labels, container_memory rename, page-fault collapse, kubelet_volume_stats, container_network with container="" |
| 2 | `85455e5` | `cadvisor.go` — same label rename + container_network passthrough |
| 3 | `6f92dee` | `flows/aggregator.go` — source_/destination_ flow labels |
| 4 | `2c9bf3d` | `internal/self/` — kubebolt_agent_* collector |
| 5 | `fe1e4a0` | UI + backend metrics_query consumers |
| 5 (follow-up) | `9dc6dc6` | UI + backend flow consumers (was missed in initial Day 5 sweep, surfaced during in-vivo smoke test) |

### Spec

`internal/kubebolt-agent-technical-spec.md` bumped to v0.2; §4 is now
authoritative for the metric / label set the agent emits.

## [0.2.2] — 2026-05-07

Patch release. One fix in the Hubble flow collector — without it, the
Reliability dashboard's L7 panels could go silently empty for days
after a single transient apiserver hiccup.

### Fixed

- **Flow collector now self-heals after losing the leader-election lease.**
  Only one agent pod streams flows from `hubble-relay` at a time; the
  others sit on the lease and take over if the leader's pod dies.
  `RunLeaderElectedCollector` was calling
  `leaderelection.RunOrDie` directly, which returns once the lease is
  lost (e.g., a 10s renew window crossed during an apiserver restart
  or an EKS control-plane upgrade). When it returned, the goroutine
  ended permanently — no peer pod ever re-attempted the election, so
  the Lease object stayed wedged with `holderIdentity: ""` and zero
  flows reached VictoriaMetrics. Observed in production for ~2 days
  with every agent pod healthy and the Hubble badge in the UI still
  green; symptom was a perma-empty Reliability tab. Fix: wrap
  `RunOrDie` in a `runElectionLoop` that re-attempts indefinitely
  with exponential backoff (1s → 30s) honoring `ctx` cancellation.
  Backoff resets only when the prior term held the lease at least
  30s, so a real term ending re-attempts immediately while a flapping
  apiserver doesn't get papered over by a slow-attempt cadence.
  Validated in-vivo on a kind cluster by stealing the lease via
  `kubectl patch` (`holderIdentity=fake-stealer`): peer pod
  re-acquired and resumed streaming ~5s later, where the prior code
  stayed broken forever.



Patch release. Two fixes — one in the shipper (faster reconnect after
backend restarts), one in the flow aggregator (without it, the new
dashboard Network Drops panel was perma-empty in any cluster with
active NetworkPolicies).

### Fixed

- **Aggregator was silently dropping every `verdict=dropped` flow.**
  The pod-to-pod path in `Aggregator.Record` filtered out flows
  whose `TrafficDirection` wasn't `EGRESS`, with the legitimate
  intent of avoiding double-counting forwarded traffic (a forwarded
  packet appears twice — egress on the source node, ingress on the
  destination — and we keep just the egress observation). But
  Cilium emits **dropped** flows with `TRAFFIC_DIRECTION_UNKNOWN`:
  the SYN is rejected before Cilium classifies direction, and the
  drop is observed exactly once at the denial point. The EGRESS
  filter was therefore swallowing every drop in any cluster with a
  Cilium-enforced NetworkPolicy.
  `pod_flow_events_total{verdict="dropped"}` never reached
  VictoriaMetrics, and the dashboard's new Reliability → Network
  Drops panel was perma-empty — "NetworkPolicies are passing"
  looked reassuring but bore no relation to reality. Fix: bypass
  the `is_reply` / EGRESS-only guards when verdict is `dropped`
  (those checks exist to dedupe forwarded flows; dropped flows
  have no reply and are observed once, so neither applies).
  Caught while wiring up the Network Drops panel against a
  temporary `CiliumNetworkPolicy` with `ingressDeny` — `cilium
  hubble observe --verdict DROPPED` showed drops, VM showed none.
  Without this fix the panel would look broken to anyone running
  real network policies.
- **Shipper reconnect backoff now resets after a healthy session.**
  `Shipper.Run` previously grew the reconnect backoff exponentially
  (1s → 2s → 4s → … → 60s cap) with no reset path. Once at the cap
  — easy to hit during a development session with several backend
  restarts — it stayed there indefinitely, even after the agent had
  been running cleanly for hours. So a planned backend deploy made
  every agent sit out a full minute before reconnecting and the
  cluster selector stayed blank in every UI for that whole window.
  Fix: track session start time; if `runSession` returned an error
  after running ≥10s, treat it as a healthy session that dropped
  (typical of a graceful restart) and reset the next backoff to 1s.
  Exponential growth still kicks in for genuinely stuck dial loops.
  Measured impact during dev: post-restart reconnect went from 60s
  (stuck at cap) to ~3s.

## [0.2.0] — 2026-04-29

First public OSS release. Sprint A.5 closes the SPDY tunnel work
that lets the agent expose the cluster's apiserver to a remote
KubeBolt backend, and Sprint A.5+ adds the install ergonomics
(3-tier RBAC, inline token generation, self-targeted-proxy
detection) that make the install practical without ever opening
the KubeBolt UI.

### Added

- **3-tier RBAC model** (`rbac.mode: metrics|reader|operator`).
  Replaces the previous binary "operator-tier RBAC overlay"
  approach with three explicit modes — privacy-conscious metrics,
  cluster-wide read, or full read+write. Helm value, OSS manifest,
  and UI wizard all expose the same picker.
- **K8s API proxy** (SPDY tunneling) for the agent's outbound gRPC
  channel. When proxy is on, the backend can route apiserver calls
  — including pod exec, port-forward, file browser, kubectl-style
  mutations — through the tunnel. Proxy is auto-on for `reader`
  and `operator` modes; off for `metrics`.
- **Inline ingest-token issuance** in the KubeBolt admin wizard.
  "Generate token + create Secret" button issues a token via
  `/admin/tenants/{id}/tokens` AND materializes the K8s Secret in
  the agent namespace, so the operator never has to copy/paste
  plaintext or run `kubectl create secret` manually.
- **Self-targeted-proxy detection** on uninstall and configure.
  Refuses to remove or roll-restart the agent that backs the
  active dashboard session without an explicit typed-name
  confirmation, since either action would sever the only path the
  backend has to that cluster.
- **Pre-flight gating** when the backend runs in `enforced` auth
  mode. Install / configure with `proxy.enabled=true` AND
  `auth.mode=disabled` is rejected up-front with an actionable
  error, rather than letting the agent crash-loop on the welcome
  handshake. The wizard mirrors this gate client-side so the Save
  button disables with a tooltip instead of waiting for a 400.
- **Three OSS manifests** under `deploy/agent/`:
  `kubebolt-agent-metrics.yaml`, `kubebolt-agent-reader.yaml`,
  `kubebolt-agent-operator.yaml`. Each is self-contained (no
  kustomize, no overlays) and carries an inline `CONFIGURABLE`
  block at the top showing what to edit before `kubectl apply`.
- **Adoption logic** for pre-existing operator-tier RBAC. When the
  shipped `kubebolt-agent-rbac-operator.yaml` was applied via
  raw kubectl before the install wizard ran, the wizard now
  recognizes the `kubebolt.dev/rbac-tier=operator` signature label
  and adopts the resource (replacing labels with `managed-by`),
  instead of conflicting on it.

### Changed

- **`kubebolt-agent-reader` ClusterRole** is now the cluster-wide
  read tier, not the narrow metrics rules. The narrow rules moved
  to `kubebolt-agent-metrics`. Existing installs migrate
  automatically on the first Configure / install via the wizard,
  helm upgrade, or `kubectl apply -f` against any of the new
  manifests.
- **Wizard UI** — the binary "Operator-tier RBAC" sub-toggle is
  replaced by an explicit 3-mode picker. Proxy is auto-derived
  from the picked mode; the standalone proxy toggle is removed
  (advanced overrides go through helm values directly).
- **`kubebolt-agent` ClusterRoleBinding** (legacy, no suffix) is
  now deleted on every apply — replaced by per-tier Bindings.

### Fixed

- **Configure dialog cache** — switching to `gcTime:0` so each
  reopen of the dialog fetches fresh state. Previous installs hit
  a "values disappear after Save" bug because the pre-edit
  snapshot beat the post-Save refetch into the form.
- **Operator-tier ClusterRole adoption** — installs done via the
  shipped manifest before the UI was used now adopt cleanly when
  the wizard's Operator toggle is flipped, instead of erroring
  with `ClusterRole already exists and was not installed by
  KubeBolt`.

### Migration notes (from 0.1.x)

- Helm: re-run `helm upgrade` against the chart — the rbac.mode
  default is `reader`, which matches the most common 0.1.x setup
  (`proxyEnabled=true` without operator overlay).
- Raw kubectl: delete the legacy `kubebolt-agent`
  ClusterRoleBinding (`kubectl delete clusterrolebinding
  kubebolt-agent`), then apply one of the new manifests.
- UI: open Configure → the new mode picker shows whichever tier is
  currently in the cluster (auto-detected by ClusterRole presence)
  → Save. No data loss, just a rolling restart.

## [0.1.0] — 2026-03-XX (Sprint A baseline)

Initial public-ish release. Single ClusterRole tier (narrow
kubelet-stats + pods + namespaces), no proxy, optional ingest-token
or TokenReview auth.
