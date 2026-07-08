import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import type { DeployEvent, PromVectorResponse } from '@/services/api'

// useDeploysVM derives rollout events from VictoriaMetrics instead of the
// live connector — the metrics-only counterpart to api.getDeploys.
//
// A metrics-only cluster has no connector, so the connector's ReplicaSet
// walk (GetDeploys) can't run. But kube-state-metrics ships the same fact
// over vmagent: `kube_replicaset_created` carries each ReplicaSet's creation
// unix timestamp, and `kube_replicaset_owner{owner_kind="Deployment"}` names
// the owning Deployment. Every new ReplicaSet a Deployment owns IS a rollout
// (same definition the connector uses), so joining the two reconstructs the
// deploy markers from VM alone.
//
// The PromQL ships UNSCOPED — the backend's /metrics/query proxy injects
// `cluster_id` + `tenant_id` for the active cluster, exactly as it does for
// every other Capacity/Reliability query.
//
// Parity caveat: KSM exposes no per-ReplicaSet container image, so the VM
// markers carry workload name + time but no image (DeploysList renders the
// image only when present). The image is secondary on a marker whose job is
// "a rollout happened at T" — the timestamp + name are what correlate a
// metric change with a deploy.

// vmResultToDeploys maps a kube_replicaset_created⋈owner instant-query
// response to DeployEvent[], newest first. Exported for unit testing the
// pure transform without a live VM. Rows missing namespace/owner_name or a
// parseable creation timestamp are skipped — VM almost never emits them and
// one bad row shouldn't drop the whole list.
export function vmResultToDeploys(resp: PromVectorResponse | undefined): DeployEvent[] {
  const rows = resp?.data?.result
  if (!rows || rows.length === 0) return []
  const deploys: DeployEvent[] = []
  for (const row of rows) {
    const namespace = row.metric.namespace
    const name = row.metric.owner_name
    if (!namespace || !name) continue
    // value[1] is the metric VALUE (the RS creation unix seconds), not
    // value[0] (the query eval timestamp). Float because VM string-encodes.
    const createdUnix = parseFloat(row.value?.[1])
    if (!Number.isFinite(createdUnix) || createdUnix <= 0) continue
    deploys.push({
      namespace,
      kind: 'Deployment',
      name,
      deployedAt: new Date(createdUnix * 1000).toISOString(),
      // image omitted — KSM carries no per-ReplicaSet image.
    })
  }
  deploys.sort((a, b) => Date.parse(b.deployedAt) - Date.parse(a.deployedAt))
  return deploys
}

// buildDeploysVMQuery assembles the windowed join. `since` (unix seconds) is
// computed per-fetch so the window slides on refetch. Exported for testing.
export function buildDeploysVMQuery(since: number): string {
  return (
    `(kube_replicaset_created >= ${since})` +
    ` * on(namespace, replicaset) group_left(owner_name)` +
    ` kube_replicaset_owner{owner_kind="Deployment"}`
  )
}

// useDeploysVM runs the join when `enabled` (i.e. the active cluster is
// metrics-only) and returns the derived markers. Disabled → no fetch, [].
export function useDeploysVM(rangeMinutes: number, enabled: boolean): DeployEvent[] {
  const { data } = useQuery({
    queryKey: ['deploys-vm', rangeMinutes],
    queryFn: () => {
      const since = Math.floor(Date.now() / 1000) - rangeMinutes * 60
      return api.queryMetrics({ query: buildDeploysVMQuery(since) })
    },
    enabled,
    refetchInterval: 30_000,
    staleTime: 15_000,
    retry: false,
  })
  return vmResultToDeploys(data)
}
