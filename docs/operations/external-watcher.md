# External watcher on Grafana Cloud

KubeBolt measures itself into the VictoriaMetrics it runs next to, and reads
those series in Administration › System › Health. The one failure that view
can never show is its own: the API down, or the store with it. The external
watcher closes that gap from outside:

1. the API pushes a second copy of its **health series** to Grafana Cloud by
   Prometheus remote_write (`KUBEBOLT_EXTERNAL_METRICS_*`);
2. a Grafana alert fires when those series **stop arriving**;
3. Synthetic Monitoring checks the **public API** over HTTP.

Only the API's own health crosses — series without `tenant_id` or
`cluster_id`, from an allowlist (`apps/api/internal/api/metrics_external.go`):
`kubebolt_build_info`, `kubebolt_http_*`, `kubebolt_job_*`, `kubebolt_vm_*`,
`kubebolt_ws_*`, `kubebolt_api_runtimes`, `process_resident_memory_bytes`,
`process_cpu_seconds_total`, `process_start_time_seconds`, `go_goroutines`.
No cluster identifier and nothing of Kobi leave the install. A few hundred
series per replica, one data point per minute: inside Grafana Cloud's free tier.

---

## 1. Credentials

In the Grafana Cloud portal, on your stack:

- **Prometheus → Details**: the remote_write URL
  (`https://prometheus-prod-XX-prod-REGION.grafana.net/api/prom/push`) and the
  **instance id** (a number) — the basic-auth user.
- **Access policies**: a policy with the `metrics:write` scope, and a token for
  it (`glc_…`) — the basic-auth password. It is a secret.

## 2. Turn the push on

| Variable | Value |
|---|---|
| `KUBEBOLT_EXTERNAL_METRICS_URL` | the remote_write URL; blank = off |
| `KUBEBOLT_EXTERNAL_METRICS_USER` | the instance id |
| `KUBEBOLT_EXTERNAL_METRICS_TOKEN` | the token — from a Secret in Kubernetes |
| `KUBEBOLT_EXTERNAL_METRICS_LABELS` | `env=prod` (or `env=dev`): tells deployments apart in one stack |
| `KUBEBOLT_EXTERNAL_METRICS_INTERVAL` | `60s` (default; 15 s minimum) |

- **Local dev** (`make dev*`): add them to the repo-root `.env`.
- **Docker Compose**: the same variables in `deploy/.env`.
- **Helm**: `extraEnv`, with the token from a Secret — see the commented
  example in `deploy/helm/kubebolt/values.yaml`:
  `kubectl create secret generic kubebolt-external-metrics --from-literal=token=<glc_…>`.

The API logs `external metrics push on` at boot. Its own background job
`external_push` shows in **Administration › System › Health › Background jobs**;
a push that fails is logged as `external metrics push failed` with Grafana's
answer.

Check in **Explore** on the Grafana Cloud Prometheus data source:

```promql
kubebolt_build_info{env="prod"}
```

## 3. Dashboard

`deploy/grafana/kubebolt-platform-health.json` — **Dashboards → New → Import →
Upload dashboard JSON file**, then pick the stack's Prometheus data source. It
mirrors Administration › System › Health: overview (replicas reporting, last
push, version, 5xx share, jobs late), API (memory, CPU, goroutines, uptime,
cluster runtimes), HTTP (status, 5xx by route group, response time,
route-group table, open requests, WebSocket drops), background jobs (table and
time since last success in intervals) and calls to VictoriaMetrics. Variables:
data source, `env`, replica.

Grafana Cloud speaks standard PromQL, not VictoriaMetrics' MetricsQL, so the
queries differ from the in-product ones: counters through `rate` /
`increase` over `$__rate_interval`, and gauges through the standard form of
`liveProcess` — of the series of a replica that differ only in `run_id`, the
one whose sample is newest (`timestamp()`), so a restarted process is never
summed with the dead one.

## 4. Alert: KubeBolt stopped reporting

**Alerting → Alert rules → New alert rule**, data source = the stack's
Prometheus:

```promql
count(kubebolt_build_info{env="prod"}) or vector(0)
```

- Condition: **IS BELOW 1**.
- Evaluate every **1m**, pending period **5m** — two missed pushes at most are a
  blip, five minutes without one is an outage.
- **No data / error handling: Alerting** (if the query itself returns nothing,
  that is the outage).
- Contact point: the platform Slack channel, or on-call.

One rule per `env` you want watched. For the rest — server errors, latency,
late jobs, failing calls to VictoriaMetrics, Kobi's provider and answer
quality — load `deploy/prometheus/kubebolt-alerts.yaml` into the Prometheus
or Grafana that receives these series: this one exists for when the API
itself is gone.

Optional, also from outside: replicas restarting —
`count by (instance) (count by (instance, run_id) (last_over_time(kubebolt_build_info{env="prod"}[1h]))) > 5`.

## 5. Synthetic Monitoring: the public API

**Testing & synthetics → Synthetics → Add check → HTTP**:

- URL: `https://<public API host>/api/v1/auth/config` — public, cheap, and it
  crosses the gateway and the API process. Expect **200**.
- Frequency **5 minutes**, from **2 probes** in different regions: about 17k
  executions a month, inside the free 100k (every minute from 3 probes is not).
- Alert sensitivity: **medium** (fires when both probes fail).

The push tells you the API process is alive; the check tells you a customer can
reach it. Each catches what the other cannot: a healthy API behind a broken
gateway, or a reachable gateway in front of an API that cannot push.
