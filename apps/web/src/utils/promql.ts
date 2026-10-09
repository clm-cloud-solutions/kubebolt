// Shared PromQL helpers — small utilities for query construction
// reused across the time-series panels.

// SERVER_OWNED_SERIES_MATCHER leaves out the series KubeBolt's API writes
// itself about a cluster — the AI observability families. They are KubeBolt's
// bookkeeping, not ingest, so any query that counts "the series a cluster
// ships" adds it. Mirrors the API's seriesgate.ServerOwnedFamiliesRegex;
// change both together.
export const SERVER_OWNED_SERIES_MATCHER =
  '__name__!~"kubebolt_(kobi|autopilot|ai)_.+|kubebolt_cluster_team_info"'

// liveProcess reads a gauge the API writes about itself (process memory,
// build info, agent channels…) as the process running now reports it. Each
// API process writes its own series (run_id), and after a restart the dead
// process's last value stays readable for the five-minute lookback next to
// the live one's. Of the series that differ only in run_id, the live one is
// the one written last — no guess between the smaller and the larger value.
// That leaves exactly one series per point (count = 1, verified on
// VictoriaMetrics), so the outer sum only drops run_id: a replica stays one
// line across its restarts instead of one line per process.
// Counters need none of this: every process's increments really happened,
// so they are summed across run_id.
export function liveProcess(gauge: string): string {
  return `sum without (run_id) (${gauge} and (tlast_over_time(${gauge}[5m]) == ignoring (run_id) group_left max without (run_id) (tlast_over_time(${gauge}[5m]))))`
}

// collapsePodToWorkload wraps a metric expression in nested
// label_replace calls that derive a "workload" label from a pod
// name. Hubble flow metrics (pod_flow_*) carry destination_pod /
// source_pod (full pod names with the controller's hash suffixes)
// but no workload label — so panels that want to group rates by
// Deployment / DaemonSet / StatefulSet need to recover that
// grouping client-side.
//
// Three passes, applied in order. Each later pass overrides the
// workload label only if its regex matches the pod label, so the
// most specific pattern wins:
//   1. workload = pod label  (default fallback for unmatched names)
//   2. ^(.+)-[a-z0-9]{4,8}$               — DaemonSet pattern (single trailing hash)
//   3. ^(.+)-[a-z0-9]{6,12}-[a-z0-9]{4,8}$ — ReplicaSet/Deployment (two hashes)
//
// StatefulSet pods (`redis-0`, `redis-1`) match neither — the
// numeric ordinal isn't `[a-z0-9]{4,8}` — so they retain the full
// pod name, which is the right behavior: in a StatefulSet the pod
// IS the unit of identity. The user can read `redis-0` and know
// what they're looking at.
//
// Limitation: ReplicaSets created outside Deployments (uncommon
// today — the legacy bare-RS pattern) collapse to a name with the
// RS-template-hash still attached. Acceptable for v1 since those
// workloads are rare and still recognizable in the UI.
export function collapsePodToWorkload(
  metric: string,
  podLabel = 'destination_pod',
  outputLabel = 'workload',
): string {
  return [
    `label_replace(`,
    `  label_replace(`,
    `    label_replace(`,
    `      ${metric},`,
    `      "${outputLabel}", "$1", "${podLabel}", "^(.*)$"`,
    `    ),`,
    `    "${outputLabel}", "$1", "${podLabel}", "^(.+)-[a-z0-9]{4,8}$"`,
    `  ),`,
    `  "${outputLabel}", "$1", "${podLabel}", "^(.+)-[a-z0-9]{6,12}-[a-z0-9]{4,8}$"`,
    `)`,
  ].join(' ')
}
