# Incident simulation lab for Kobi

A set of deliberately broken Kubernetes scenarios for testing, demoing and
validating what **Kobi Copilot** (KubeBolt's AI assistant) can do: incident
detection, diagnosis, and **proposed actions** (the `propose_*` tools).

Everything uses stock images (`alpine`, `python`, `nginx`, `postgres`, `pause`),
with no custom images, so anyone can apply it to their own cluster.

There are two parts:

| Folder | What it is | What it's for |
|--------|------------|---------------|
| [`scenarios/`](./scenarios/) | 15 **single-workload** incidents, one per capability | Exercise each Kobi action in isolation, cleanly |
| [`three-tier-shop/`](./three-tier-shop/) | A **3-tier** e-commerce app (frontend → APIs → external DB) split across namespaces | Exercise **cascading failures** and root-cause reasoning across the topology |

---

## The 9 actions Kobi can propose

Kobi never changes anything on its own: it **proposes** an action, which the UI
renders as a confirmation card with an *Execute* button. You approve it, and it
runs under your RBAC role, not Kobi's. These scenarios cover all nine:

| Tool | What it does | Scenario that triggers it |
|------|--------------|---------------------------|
| `propose_set_resources` | Raises/lowers CPU and memory (requests/limits) | `01-oomkilled`, `06-cpu-throttle`, `07-under-requested` |
| `propose_restart_workload` | Rollout restart (Deployment/StatefulSet/DaemonSet) | `02-crashloop-exit` |
| `propose_set_env` | Adds/changes environment variables | `03-crashloop-missing-env` |
| `propose_set_image` | Changes a container's image | `04-imagepullbackoff` |
| `propose_rollback_deployment` | `kubectl rollout undo` to a healthy revision | `05-bad-rollout` |
| `propose_patch_hpa` | Adjusts an HPA's min/maxReplicas | `08-hpa-maxed-out` |
| `propose_scale_workload` | Scales to N replicas (including 0) | `09-zero-replicas` |
| `propose_debug_pod` | Attaches an ephemeral debug container | `10-distroless-debug` |
| `propose_delete_resource` | Deletes a resource (destructive, asks for confirmation) | `13-orphaned-resources` |

On top of that, two scenarios fire insights that Kobi **diagnoses** without a
remediation action: `11-orphan-service` (`service-no-endpoints`) and
`12-orphan-networkpolicy` (`policy-no-match`).

> **Autopilot** (autonomous remediation, where KubeBolt applies a fix on its own
> within guardrails) is available in **KubeBolt Cloud** only. In the open-source
> edition you follow every scenario with Kobi Copilot: Kobi proposes, you approve.

---

## Part 1 — Single-workload scenarios (`scenarios/`)

They all live in the `kobi-incident-lab` namespace.

### Apply everything

```bash
kubectl apply -f scenarios/
```

Wait 1–2 minutes for the incidents to mature (crash loops need to accumulate
restarts; the HPA needs metrics). Then open KubeBolt → Insights, or ask Kobi.

### Scenario table

The Insight column lists KubeBolt's rule IDs, the same IDs you tune under
Administration → Insights → Rules.

| # | File | Symptom | Insight (rule ID) | Kobi action |
|---|------|---------|-------------------|-------------|
| 01 | `01-oomkilled.yaml` | OOMKilled (allocates 200Mi under a 128Mi limit) | `oom-killed` | `propose_set_resources` (memory limit) |
| 02 | `02-crashloop-exit.yaml` | Exits with code 1 in a loop | `crash-loop` / `frequent-restarts` | diagnosis + `propose_restart_workload` |
| 03 | `03-crashloop-missing-env.yaml` | `DATABASE_URL` is missing | `crash-loop` | `propose_set_env` |
| 04 | `04-imagepullbackoff.yaml` | Image tag doesn't exist | `image-pull-backoff` | `propose_set_image` |
| 05 | `05-bad-rollout.yaml` | Bad deploy with a healthy previous revision | `image-pull-backoff` (then `progress-deadline-exceeded` after the 10 min deadline) | `propose_rollback_deployment` |
| 06 | `06-cpu-throttle.yaml` | Busy loop under a 50m CPU limit | `cpu-throttle-risk` | `propose_set_resources` (CPU) |
| 07 | `07-under-requested.yaml` | Uses far more than it requests | `resource-underrequest` | `propose_set_resources` (requests) |
| 08 | `08-hpa-maxed-out.yaml` | HPA pinned at maxReplicas | `hpa-maxed-out` | `propose_patch_hpa` |
| 09 | `09-zero-replicas.yaml` | `replicas: 0` | — (see note) | `propose_scale_workload` |
| 10 | `10-distroless-debug.yaml` | Shell-less pod that needs debugging | — (triage) | `propose_debug_pod` |
| 11 | `11-orphan-service.yaml` | Service with no endpoints | `service-no-endpoints` | diagnosis |
| 12 | `12-orphan-networkpolicy.yaml` | NetworkPolicy that matches no pods | `policy-no-match` | diagnosis |
| 13 | `13-orphaned-resources.yaml` | Zombie resources | — (orphaned objects) | `propose_delete_resource` |
| 14 | `14-liveness-flapping.yaml` | Liveness fails, then self-heals | `liveness-probe-failing` | diagnosis + **lifecycle** |
| 15 | `15-oom-selfhealing.yaml` | OOMs once and recovers **in place** | `oom-killed` | diagnosis + **lifecycle** |

> **09:** the `zero-replicas` rule fires only when a Deployment wants replicas
> (`spec.replicas > 0`) and none are available. An explicit `replicas: 0` reads
> as intentional, so this scenario raises no insight; drive it by asking Kobi
> (e.g. *"scaled-down has no pods, bring it back to 2 replicas"*).

> 14 and 15 are not about watching the rule fire: they are the **lifecycle
> regression tests** (2026-08-02). What you time is when the insight
> *disappears*, not when it appears.
>
> 15 exists because "fixing it with `kubectl set resources`" **proves nothing**:
> it changes the pod template, a new ReplicaSet is born and the pod that OOMed is
> deleted, so the insight clears for the boring reason. The only case that
> discriminates is recovery **without replacing the pod**, where
> `lastState.terminated.reason` keeps saying `OOMKilled` forever.
>
> The failure criteria and the commands to check against are in the header of
> each YAML.

### Two scenarios need an extra step

- **05 (rollback):** a rollback needs ≥2 revisions. After applying, push the
  second (broken) revision with:
  ```bash
  cd scenarios && ./break.sh
  ```
- **08 (HPA):** the HPA reads CPU from `metrics.k8s.io`, so it needs
  **metrics-server**. It ships by default on most managed clusters; on kind,
  install it if it's missing.

### Clean up

```bash
kubectl delete ns kobi-incident-lab
```

---

## Part 2 — Cascading 3-tier app (`three-tier-shop/`)

The flagship scenario: a 3-tier e-commerce store where **taking down the
database** sets off a readiness cascade that climbs tier by tier. It has its own
detailed README with the architecture diagram, the cascade walkthrough and fault
injection:

➡️ **[three-tier-shop/README.md](./three-tier-shop/README.md)**

Quick summary:

```bash
cd three-tier-shop
database/run-db.sh     # starts postgres OUTSIDE the cluster (docker)
./apply.sh             # deploys frontend + APIs and wires them to the DB
database/stop-db.sh    # 💥 triggers the cascade: DB ↓ → backend NotReady → frontend serves 503
database/start-db.sh   # end-to-end recovery
database/fault-api.sh orders error   # 💥 takes down ONE API only (error|unready|slow)
database/heal-api.sh  orders         # heals that API
./teardown.sh          # removes everything
```

---

## Portability

- **kind** (tested): the DB runs as a docker container on the `kind` network and
  pods reach it by IP. This is the scripts' default path.
- **Docker Desktop / minikube / others:** the docker network may be named
  differently or not be routable from pods. Pass `DB_NETWORK=<your-network>` to
  the scripts, or point the `Endpoints` in
  [`three-tier-shop/10-data-tier.yaml`](./three-tier-shop/10-data-tier.yaml) at
  an IP your pods can reach (e.g. the `host.docker.internal` IP, or a real
  postgres). Details in the 3-tier app's README.
- The **Part 1** scenarios depend on nothing external and run the same on any
  cluster (only 08 needs metrics-server).

> Note: these manifests describe failure states **on purpose**. Do not apply
> them to a production cluster.
