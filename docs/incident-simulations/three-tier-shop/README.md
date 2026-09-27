# KubeShop — a 3-tier app with cascading failures

A demo e-commerce store split into **three tiers**, each in its own namespace,
with a database that lives **outside the cluster**. It is designed to show how a
single failure (the DB) **cascades upward** through health checks that validate
dependencies, and how Kobi Copilot reasons about the root cause by walking the
topology.

## Architecture

```
   ┌──────────────────────── cluster ────────────────────────┐
   │                                                          │
   │  ns: shop-frontend          ns: shop-backend             │
   │  ┌───────────┐    HTTP   ┌──────────────┐                │
   │  │ loadgen   │──────────▶│  shop-web    │                │
   │  └───────────┘           │  (storefront)│                │
   │                          └──────┬───────┘                │
   │                       HTTP /healthz │  GET /              │
   │                          ┌──────────┴─────────┐          │
   │                          ▼                    ▼          │
   │                   ┌────────────┐       ┌────────────┐    │
   │                   │ orders-api │       │ catalog-api│    │
   │                   └──────┬─────┘       └──────┬─────┘    │
   │                          │ TCP :5432          │          │
   │  ns: shop-data           ▼                    ▼          │
   │                   ┌──────────────────────────────┐       │
   │                   │ Service shop-db (no selector) │       │
   │                   │ Endpoints → external IP       │       │
   │                   └───────────────┬──────────────┘       │
   └───────────────────────────────────┼──────────────────────┘
                                        │ TCP :5432
                            ┌───────────▼────────────┐
                            │  postgres (docker run) │   ← OUTSIDE the cluster
                            │  docker network `kind` │
                            └────────────────────────┘
```

- **shop-frontend** — `shop-web` (an HTML storefront that calls both APIs on
  every request) + `loadgen` (generates constant traffic).
- **shop-backend** — `orders-api` and `catalog-api`, two identical microservices
  (same server, different `SERVICE_NAME`) that depend on the DB.
- **shop-data** — a `Service` **with no selector** + hand-written `Endpoints`
  pointing at the IP of the external postgres container. That way the cluster
  "sees" the DB as a normal Service even though it runs outside.

Everything is `python:3.12-alpine` + a script embedded in a ConfigMap. No custom
images.

## The health-check model (the important part)

Every workload has **both liveness and readiness**, and they play different roles
on purpose:

| Tier | liveness `/livez` | readiness `/healthz` | If the dependency goes down |
|------|-------------------|----------------------|-----------------------------|
| **backend** (`orders-api`, `catalog-api`) | process alive (always 200) | **opens a TCP socket to `shop-db:5432`** | `NotReady` → leaves the Endpoints (stops receiving traffic) |
| **frontend** (`shop-web`) | process alive (always 200) | **self-contained (always 200)** | **stays Ready**; the `/` home page returns **503** to the user |

Two design decisions, both deliberate:

- **The backend DOES leave rotation** when it loses the DB: a backend without a
  database genuinely cannot serve, so pulling it out of the Service is the right
  call. Liveness does not fail (no restart loop), only readiness.
- **The frontend does NOT leave rotation** when it loses the backend: a store
  with a degraded backend should keep answering the user — with a **5xx error**,
  not by disappearing. That's why `shop-web` keeps its readiness self-contained
  and its home page returns `503` when `orders-api` is unavailable. It's the most
  realistic behavior and, as a bonus, **it makes the failure show up as error
  traffic (5xx) on the Reliability panel** instead of as pods that vanish.

## Logs

All three services emit per-request logs with a level and latency, so Kobi
(`get_pod_logs`) has real RCA material and so you can read them in KubeBolt's log
viewer. Pattern: `<timestamp> [<service>] <LEVEL> <message>`.

| Service | Healthy | During the incident |
|---------|---------|---------------------|
| `orders-api` / `catalog-api` | `INFO GET / -> 200 db=ok 1ms` | `WARN readiness failed: cannot reach database shop-db…:5432 (timed out)` |
| `shop-web` | `INFO GET / -> 200 orders=up catalog=up 9ms` | `ERROR GET / -> 503 orders-api unreachable: …` |

Choices that let the log tell the story without noise:
- Healthy probes (`/livez`, `/healthz` OK) are **not** logged.
- The backend logs `/healthz` **only when it fails** → during a DB outage its log
  fills with `WARN readiness failed … cannot reach database`, which is the
  root-cause signal Kobi reads directly.
- The frontend logs every `/` with the result of its upstreams (`orders=up/DOWN`,
  `catalog=up/DOWN`), so a total failure (503) is distinguishable from a degraded
  one (200 with the catalog down).

```bash
kubectl logs -n shop-backend  -l app=orders-api --prefix -f
kubectl logs -n shop-frontend -l app=shop-web   --prefix -f
```

## Getting started

> Requires a **kind** cluster (docker network `kind`) by default. For other
> environments, see [Portability](#portability).

```bash
cd docs/incident-simulations/three-tier-shop

# 1. Start the external DB (postgres in docker, kind network)
database/run-db.sh

# 2. Deploy the 3 tiers and wire them to the DB
./apply.sh

# 3. Check that everything is Ready
kubectl get pods -n shop-backend
kubectl get pods -n shop-frontend
```

`loadgen` should be logging `shop-web -> 200`:

```bash
kubectl logs -n shop-frontend deploy/loadgen -f
```

## The cascade 💥

```bash
database/stop-db.sh      # docker stop shop-db
```

In order, within ~10–15s:

1. **DB ↓** — the postgres container stops.
2. **Backend NotReady** — `orders-api`/`catalog-api` `/healthz` can't connect to
   `:5432` → 503 → readiness fails → the pods leave their Endpoints.
3. **Backend Services without endpoints** — the `service-no-endpoints` insight
   fires for `orders-api` and `catalog-api`.
4. **Frontend serves errors** — `shop-web` **stays Ready** (it doesn't go down).
   Its `/` home page tries to call `orders-api`, which no longer has endpoints,
   and returns **503** to the user.
5. **loadgen sees 503** — the store keeps answering, but with an error; the end
   user sees "503 — We're having trouble loading the store." instead of a dead
   connection.

This is what makes the simulation useful for **error-rate** demos: because the
frontend doesn't disappear, the 503 travels as real HTTP traffic and shows up in
Hubble / the Reliability panel (5xx / `server_err` bucket), not as missing pods.

Watch it:

```bash
kubectl get pods -A -l app.kubernetes.io/part-of=kubeshop -w   # backend NotReady, shop-web stays Ready
kubectl logs -n shop-frontend deploy/loadgen -f                 # 200 -> 503
# L7 view of the error (Cilium cluster):
kubectl exec -n kube-system ds/cilium -c cilium-agent -- \
  hubble observe --protocol http --namespace shop-frontend --last 10   # GET / -> 503
```

### What to try with Kobi

With the cascade active, ask Kobi (in the KubeBolt UI):

- *"The store is returning 503, what's the root cause?"* — it should walk the
  topology downward (shop-web → orders-api → shop-db) and point at the DB, not at
  the symptoms above it.
- *"Why is orders-api NotReady?"* — it should read `/healthz` / the events and
  point at the DB connection.
- Check **Insights** and the **Cluster Map** in KubeBolt to see the 2 backend
  services without endpoints and the cross-namespace dependency graph, and the
  **Reliability** panel for the 5xx spike on `shop-web`.

## Recovery

```bash
database/start-db.sh     # docker start shop-db
```

Recovery climbs back up the same chain, also within ~10–15s, and **without
restarting any pod** (liveness never fired). `loadgen` goes back to `200`.

## Single-API outages (fault injection)

The DB outage takes down **both** APIs at once. To simulate **a single API**
failing — and tell app failures apart from infra failures — each backend has a
`FAULT_MODE` you can inject without touching the DB or the other API:

```bash
database/fault-api.sh <orders|catalog> <error|unready|slow>   # inject
database/heal-api.sh  <orders|catalog>                        # heal
```

| Mode | What the API does | What the frontend sees | Insight / signal | RCA lesson |
|------|-------------------|------------------------|------------------|------------|
| `error` | stays **Ready** (readiness green, has endpoints) but `GET /` → **500** | `503 orders-api returned 500` | NO readiness insight; the 5xx spike lives in Reliability + the logs | The hard case: **green readiness ≠ healthy API**. You have to look at responses/logs, not just readiness. |
| `unready` | readiness fails with the **DB healthy** → leaves the endpoints | `503 orders-api unreachable` | `service-no-endpoints` (orders-api), plus `zero-replicas` once no replica is available | Distinguishable from the DB cascade because **catalog-api stays Ready** → the DB is fine; the problem is orders-api. |
| `slow` | `GET /` sleeps `FAULT_LATENCY_MS` (3s) | timeout (2s) → `503 unreachable` + high latency | latency in Reliability | Slow ≠ down; latency is the clue. |

**Criticality per tier (already built into the frontend code):**
- **orders-api** is the **critical** dependency → if it fails in any mode, the
  frontend returns **503**.
- **catalog-api** is **non-critical** → if it fails, the frontend stays at a
  **degraded 200** (`WARN GET / -> 200 orders=up catalog=DOWN`) and the site stays
  up. A *graceful degradation* demo.

Other outages with plain kubectl (no `FAULT_MODE`):
- **API at 0 replicas:** `kubectl -n shop-backend scale deploy/orders-api --replicas=0`
  → `service-no-endpoints` (not `zero-replicas`: an explicit `replicas: 0` reads
  as intentional) → Kobi proposes `propose_scale_workload`.
- **Broken image:** `kubectl -n shop-backend set image deploy/orders-api app=python:nope`
  → `ImagePullBackOff` (`image-pull-backoff`) → Kobi proposes `propose_set_image`
  / `propose_rollback_deployment`.

### What to try with Kobi (API outages)

- `fault-api.sh orders error` → *"orders-api is Ready but the store returns 503,
  why?"* — Kobi shouldn't settle for "readiness is OK"; it should read the
  logs/responses and see the `500 fault-injected` errors.
- `fault-api.sh orders unready` → *"Is it the database again?"* — the correct
  answer is **no**: catalog-api is still Ready, so the DB is fine; it's
  orders-api.

In KubeBolt OSS, every fix Kobi suggests arrives as a proposal card that you
approve. Autonomous remediation (Autopilot) is available in KubeBolt Cloud only.

## Tier isolation (optional)

```bash
kubectl apply -f 50-networkpolicies.yaml
```

Applies `default-deny` + per-tier allows (frontend→backend, backend→data). On a
**Cilium + Hubble** cluster this also feeds the **Reliability → Network Drops**
panel with `verdict=dropped` traffic if anything tries to skip a tier.

## L7 / HTTP visibility in Hubble (Cilium)

```bash
kubectl apply -f 55-l7-visibility.yaml
```

Without this, **Hubble only captures L3/L4** (IP/port) for the store — the
**Reliability** panels (error rate, latency, traffic) and the map's L7 edges stay
empty for the `shop-*` namespaces. Cilium only parses HTTP when a
`CiliumNetworkPolicy` with an `http` rule redirects the traffic to its Envoy
proxy; a standard `NetworkPolicy` (the one from the previous step) does **not**.

This file adds that visibility CNP (observability only, it doesn't filter) on the
frontend and backend tiers, on port `8080`. It's the same pattern as the
`demo-web-http-visibility` policy in
[`deploy/test/demo-workload.yaml`](../../../deploy/test/demo-workload.yaml). It
only applies to **Cilium + Hubble** clusters; on another CNI these CRDs don't
exist — skip this file.

> Validated on kind: after applying it, `hubble observe --protocol http -n shop-backend`
> shows the `GET /` calls between tiers, and the pods stay `Ready` (kubelet probes
> and the cascade toward the DB are unaffected — the policy is ingress-only).

## Cleanup

```bash
./teardown.sh            # deletes the 3 namespaces + the DB container
```

## Portability

The scripts assume **kind** (docker network `kind`, pods route to the container's
IP). For other environments:

| Environment | What to adjust |
|-------------|----------------|
| **kind** | Nothing. Default path. |
| **Another docker network** | `DB_NETWORK=<your-network> database/run-db.sh && DB_NETWORK=<your-network> ./apply.sh` |
| **Docker Desktop K8s** | The network may not be routable from pods. Point the `Endpoints` in `10-data-tier.yaml` at the `host.docker.internal` IP, or run the DB with `--network host` and use the host IP. |
| **minikube** | Use `minikube ssh` or the host IP; point the `Endpoints` at an IP reachable from the pods. |
| **Real Postgres / RDS / Cloud SQL** | No need for `run-db.sh`. Put the real IP/host in the `Endpoints` (or switch to an `ExternalName` `Service`) and apply. The cascade triggers the same way when you cut that connectivity. |

The app logic doesn't depend on kind — only the wiring of the `Endpoints` to the
external IP. Any address your pods can reach over TCP:5432 works.

> These manifests describe a failure state on purpose. Do not apply them in
> production.
