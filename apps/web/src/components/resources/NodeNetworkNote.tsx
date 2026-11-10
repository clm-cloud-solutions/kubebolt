import { Fragment, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import type { ResourceItem, ResourceList } from '@/types/kubernetes'
import { shortenNodeName } from './NodesSummaryStrip'

// Node links shown before the rest collapse into "and N more" (a DaemonSet
// spans every node).
export const NODE_NETWORK_NOTE_MAX_LINKS = 6

// A pod with hostNetwork shares its node's network namespace: what the
// kubelet reports as its network is the node's (eth0, the overlay device,
// every pod's veth). The agent sends none of it, and an older agent sent the
// node's traffic as the pod's own, so a network chart would be empty or wrong
// either way. The note replaces the chart and points at the nodes, which
// report that traffic once.
export function NodeNetworkNote({ title, scope, nodeNames }: { title: string; scope: 'pod' | 'workload'; nodeNames: string[] }) {
  const [expanded, setExpanded] = useState(false)
  const shown = expanded ? nodeNames : nodeNames.slice(0, NODE_NETWORK_NOTE_MAX_LINKS)
  const more = nodeNames.length - shown.length
  const lead = scope === 'pod'
    ? "This pod runs with hostNetwork, so its traffic is the node's."
    : "Its pods run with hostNetwork, so their traffic is their nodes'."
  return (
    <div className="rounded-lg border border-kb-border p-4 min-w-0" style={{ background: 'var(--kb-card)' }}>
      <h4 className="text-sm font-semibold text-kb-text-primary mb-3 truncate">{title}</h4>
      <div className="flex flex-col items-center justify-center py-6 gap-1 text-xs text-kb-text-secondary text-center">
        <span>Uses the node's network</span>
        <span className="text-[11px] text-kb-text-tertiary">
          {lead}{' '}
          {shown.length === 0 ? (
            "See it on each node's Monitor tab."
          ) : (
            <>
              See it on{' '}
              {shown.map((n, i) => (
                <Fragment key={n}>
                  {i > 0 && (i === shown.length - 1 && more === 0 ? ' and ' : ', ')}
                  <Link to={`/nodes/_/${n}?tab=monitor`} title={n} className="text-status-info hover:underline font-mono">
                    {shortenNodeName(n)}
                  </Link>
                </Fragment>
              ))}
              {more > 0 && (
                <>
                  {' and '}
                  <button type="button" onClick={() => setExpanded(true)} className="text-status-info hover:underline">
                    {more} more
                  </button>
                </>
              )}
              .
            </>
          )}
        </span>
      </div>
    </div>
  )
}

// The pods endpoint and the cache key the workload's Pods, Logs and Terminal
// tabs already use (hooks/useResources.ts), so a list one of them fetched is
// reused here and this note never downloads it a second time.
const workloadPods: Record<string, { key: string; fetch: (namespace: string, name: string) => Promise<ResourceList> }> = {
  Deployment: { key: 'deployment-pods', fetch: api.getDeploymentPods },
  StatefulSet: { key: 'statefulset-pods', fetch: api.getStatefulSetPods },
  DaemonSet: { key: 'daemonset-pods', fetch: api.getDaemonSetPods },
}

// The nodes a hostNetwork workload's pods run on, for NodeNetworkNote's links.
// Rendered only for hostNetwork workloads, so other workloads make no request.
// Fetched once per visit, without polling: a workload's nodes rarely change,
// and the list carries every pod in full (a DaemonSet has one per node).
export function WorkloadNodeNetworkNote({ item, kindLabel, title }: { item: ResourceItem; kindLabel: string; title: string }) {
  const ns = String(item.namespace)
  const name = String(item.name)
  const source = workloadPods[kindLabel]
  const { data } = useQuery({
    queryKey: [source?.key ?? 'workload-pods', ns, name],
    queryFn: () => source.fetch(ns, name),
    enabled: !!source,
    staleTime: 5 * 60_000,
  })
  const nodeNames = useMemo(
    () => [...new Set((data?.items ?? []).map((p) => String(p.nodeName ?? '')).filter(Boolean))].sort(),
    [data],
  )
  return <NodeNetworkNote title={title} scope="workload" nodeNames={nodeNames} />
}
