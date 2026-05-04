// Shared PromQL helpers — small utilities for query construction
// reused across the time-series panels.

// collapsePodToWorkload wraps a metric expression in nested
// label_replace calls that derive a "workload" label from a pod
// name. Hubble flow metrics (pod_flow_*) carry dst_pod / src_pod
// (full pod names with the controller's hash suffixes) but no
// workload label — so panels that want to group rates by Deployment
// / DaemonSet / StatefulSet need to recover that grouping client-
// side.
//
// Three passes, applied in order. Each later pass overrides the
// workload label only if its regex matches dst_pod, so the most
// specific pattern wins:
//   1. workload = dst_pod  (default fallback for unmatched names)
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
  podLabel = 'dst_pod',
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
