# kubebolt-agent changelog

The agent ships on its own cadence — tag pattern `agent-vX.Y.Z`.
GitHub Actions builds and publishes the multi-arch image to
`ghcr.io/clm-cloud-solutions/kubebolt/agent` and the Helm chart to
`oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent` on
each tag.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
