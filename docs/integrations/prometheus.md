# Prometheus → KubeBolt remote_write

Ship samples from an existing Prometheus (or any Prom-compatible
agent like `vmagent`, OpenTelemetry Collector with the Prom
exporter, Grafana Agent, etc.) into KubeBolt's backend. Phase 3
of the [Universal Data Plane Plan](../../internal/agent-universal-data-plane-plan.md)
makes the receiver production-ready: per-tenant bearer auth,
per-tenant rate limiting, per-tenant cardinality caps, and
`/metrics` observability so you can see exactly what's being
accepted, throttled, or rejected.

The result: operators with an existing Prom stack don't have to
swap it out — they point `remote_write` at KubeBolt and start
seeing their workloads in the UI within a scrape cycle.

> **Available from KubeBolt 1.10.0+.** Earlier releases shipped a
> Phase 2 receiver gated only by an env var (no auth, no
> per-tenant limits). Phase 3 supersedes it; the endpoint URL is
> unchanged so existing clients keep working through the upgrade.

---

## TL;DR — point an existing Prometheus at KubeBolt

```bash
# 1. Issue an ingest token (Admin UI → Agent Tokens, or curl below)
TOKEN=$(curl -s -X POST http://kubebolt.example.com/api/v1/admin/tenants/<TENANT_ID>/tokens \
  -H "Authorization: Bearer <ADMIN_JWT>" \
  -H "Content-Type: application/json" \
  -d '{"label":"prod-prometheus"}' | jq -r .token)

# 2. Drop into your prometheus.yml
cat >> prometheus.yml <<EOF
remote_write:
  - url: http://kubebolt.example.com/api/v1/prom/write
    authorization:
      credentials: ${TOKEN}
    write_relabel_configs:
      # Required when the receiver runs in enforced mode (recommended).
      # Stamps every sample with the tenant_id label so the receiver
      # can validate it against the bearer's tenant.
      - target_label: tenant_id
        replacement: <TENANT_ID>
EOF

# 3. Reload Prometheus + verify
curl -X POST http://your-prometheus:9090/-/reload
curl -s http://kubebolt.example.com/metrics \
  | grep kubebolt_prom_write_samples_accepted_total
```

---

## Endpoint

| Field | Value |
|---|---|
| URL | `POST /api/v1/prom/write` |
| Wire format | Snappy-compressed Prometheus `WriteRequest` protobuf (stock remote_write) |
| Auth | Bearer token in `Authorization` header (mode-dependent — see below) |
| Body cap | 16 MiB compressed |
| Success | `204 No Content` |

---

## Auth modes

The receiver supports three enforcement modes, selected via
`KUBEBOLT_PROM_WRITE_AUTH_MODE` on the backend. Pick based on
where the client lives and your trust posture:

| Mode | Bearer required | Tenant validation | When to use |
|---|---|---|---|
| `disabled` | ignored | none | Single-cluster OSS, trusted internal network. Default for backwards compatibility. |
| `permissive` | optional | validated when present, otherwise auto-stamped as `tenant_id="anonymous"` | Rollout window — letting legacy unauthenticated clients keep working while you migrate them. |
| `enforced` | required | anti-spoof: the `tenant_id` label on samples MUST match the bearer's tenant | Production / SaaS / multi-tenant. Reject ambiguous traffic. |

In `enforced` mode, requests are rejected with `401 Unauthorized`
when:
- the `Authorization` header is missing, empty, or carries an
  invalid token, OR
- the request body has no `tenant_id` label, OR
- the request body carries a `tenant_id` that doesn't match the
  bearer's tenant (spoof attempt).

In `permissive` mode the same conditions log a single
`WARN msg="prom remote_write permissive-fallback engaged"` per
process (subsequent fallbacks → DEBUG) and accept the request
under the synthetic `tenant_id="anonymous"` identity. Track the
ongoing rate via `kubebolt_prom_write_requests_total{tenant_id="anonymous"}`
on `/metrics` rather than the log.

---

## Multi-tenant deployment

In SaaS or shared-backend topologies, issue one ingest token per
tenant. Each Prometheus instance carries its own bearer AND
stamps every sample with its `tenant_id` label, so the receiver
can validate one against the other:

```yaml
# customer-A's prometheus.yml
global:
  external_labels:
    tenant_id: aaaa-1111-aaaa-1111   # customer A's tenant UUID

remote_write:
  - url: https://kubebolt.example.com/api/v1/prom/write
    authorization:
      credentials_file: /etc/prometheus/kubebolt-token
```

`external_labels` is preferred over `write_relabel_configs`
because it stamps every series cluster-wide, including alerting
and recording rule output. The agent's stock `prometheus_remote_storage_*`
metrics still ship out without that label, but the receiver
auto-stamps them on accept (Day 4.1 of Phase 3) so they end up
correctly attributed.

Anti-spoofing: if a client tries `tenant_id: bbbb-2222-bbbb-2222`
with a bearer that authenticates as customer A, the receiver
returns `401` and logs `prom remote_write tenant_id mismatch`.

---

## Per-tenant limits

Defaults applied to every tenant when no per-tenant override is
configured:

| Knob | Default | Env var to change globally | Override per-tenant |
|---|---|---|---|
| Write rate (samples/s) | 10,000 | `KUBEBOLT_PROM_WRITE_DEFAULT_SAMPLES_PER_SEC` | UI `/admin/ingest-limits` |
| Burst (samples) | 100,000 | `KUBEBOLT_PROM_WRITE_DEFAULT_BURST_SAMPLES` | same |
| Max active series | 1,000,000 | `KUBEBOLT_PROM_WRITE_DEFAULT_MAX_ACTIVE_SERIES` | same |

Per-tenant overrides live in BoltDB and survive restarts. Use
them when a single tenant ships substantially more (or less)
than the fleet baseline. The UI form sends only the dirty fields,
so unchanged values inherit the system default automatically.

When a limit trips:

| Limit | HTTP response | Retry-After |
|---|---|---|
| Write rate | `429 Too Many Requests` | seconds until the bucket refills |
| Burst | `429 Too Many Requests` | seconds until the bucket refills |
| Max active series | `413 Payload Too Large` | 3600 (1h — series caps don't change quickly) |
| Body size (16 MiB) | `413 Payload Too Large` | — |

`vmagent` and recent Prometheus versions honor `Retry-After`
natively; older clients fall back to exponential backoff.

---

## Observability — `/metrics`

The backend exposes its own Prom-style metrics at `GET /metrics`
(no auth — firewall this port at the load balancer in SaaS).
Useful PromQL for each operator question:

```promql
# "Is this tenant being throttled?"
rate(kubebolt_prom_write_requests_total{tenant_id="<id>",status="rate_limit"}[5m])

# "Is this tenant near the cardinality cap?"
kubebolt_prom_write_active_series{tenant_id="<id>"}
  / on(tenant_id) group_left
kubebolt_prom_write_active_series_limit  # configured separately

# "How much data is each tenant shipping?"
sum by (tenant_id) (rate(kubebolt_prom_write_samples_accepted_total[5m]))

# "What's the rejection rate, and why?"
sum by (status) (rate(kubebolt_prom_write_requests_total{status!="accepted"}[5m]))
```

The `status` label takes one of: `accepted`, `rate_limit`,
`cardinality`, `auth`, `body_size`, `malformed`,
`tenant_id_mismatch`, `tenant_id_missing`, `injection_failed`,
`upstream_error`.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `204` but samples don't appear in UI | Series under a `cluster_id` you're not viewing | Check `count by (cluster_id) ({tenant_id="<id>"})` in VM. Set `external_labels.cluster_id` in Prom if the agent's auto-detection isn't reaching the same value. |
| `401 missing Bearer token` (enforced mode) | Prometheus has no `authorization:` block, or the file is empty | Confirm `authorization.credentials_file` resolves to a non-empty file. Tokens look like `kb_<base64>` and survive base64 decode. |
| `401 invalid ingest token` | Token revoked / rotated / from a different KubeBolt instance | Re-issue from `/admin/agent-tokens` and update the Prom config. |
| `401 tenant_id label does not match` | Client stamped a tenant other than its bearer's | Make sure `external_labels.tenant_id` matches the tenant the bearer authenticates as. Spoof attempts are intentionally rejected. |
| `401 tenant_id label required` (enforced mode) | No `tenant_id` external label set | Add `external_labels.tenant_id: <UUID>` to the Prom config. |
| `413 Payload Too Large` (body size) | Single batch exceeds 16 MiB compressed | Lower `queue_config.max_samples_per_send` (default 2000). Most operators see this only with very long-running Prometheus catching up after a network blip. |
| `413` with `Retry-After: 3600` | Cardinality cap exceeded | Series count is checked every 30s against VM. Bump `maxActiveSeries` via UI or scope your Prom config to fewer targets. |
| `429 Too Many Requests` | Rate limit tripped | Bump `writeSamplesPerSec`/`writeBurstSamples` via UI, or reduce scrape frequency. |
| `502 Bad Gateway` | VictoriaMetrics unreachable from the backend | Check `kubebolt-api` → VM connectivity. Pre-fix the underlying outage; client should retry. |

---

## See also

- [`docs/agent-scraping.md`](../agent-scraping.md) — alternative path: run the bundled `vmagent` sidecar instead of standalone Prometheus
- [`internal/agent-universal-data-plane-plan.md`](../../internal/agent-universal-data-plane-plan.md) — design rationale for the multi-source ingest model
- KubeBolt admin UI: `/admin/agent-tokens` (issue/rotate/revoke), `/admin/ingest-limits` (per-tenant overrides)
