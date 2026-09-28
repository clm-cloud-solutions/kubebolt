// TokenClusterPicker — which clusters an API key may read.
//
// Path scopes decide which ROUTES a key may call; this decides which CLUSTERS.
// "Every cluster" is an empty list on the wire (the key follows the org: a
// cluster added tomorrow is readable too); "Only these" sends their ids. The
// server validates the ids against the org and enforces the list on every read
// — REST, Kobi's MCP tools, fleet views, findings, metrics.
//
// A cluster is offered by its id (the kube-system UID the agent reports). A
// direct kubeconfig context the server has not identified yet has none, so it
// is listed but cannot be picked — the list could not vouch for it.
import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import type { ClusterInfo } from '@/types/kubernetes'
import { parseClusterDisplayName } from '@/utils/cluster'

export function useOrgClusters() {
  return useQuery({ queryKey: ['clusters'], queryFn: api.listClusters, staleTime: 30_000 })
}

// clusterNameMap maps a cluster id to what a person calls it.
export function clusterNameMap(clusters: ClusterInfo[] | null | undefined): Map<string, string> {
  const m = new Map<string, string>()
  for (const c of clusters ?? []) {
    if (c.clusterId) m.set(c.clusterId, parseClusterDisplayName(c))
  }
  return m
}

export function TokenClusterPicker({
  value,
  onChange,
}: {
  // null = every cluster of the organization; an array = only those ids.
  value: string[] | null
  onChange: (next: string[] | null) => void
}) {
  const { data: clusters, isLoading } = useOrgClusters()
  const list = clusters ?? []
  const all = value === null
  const selected = new Set(value ?? [])

  function toggle(id: string) {
    const next = new Set(selected)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    onChange(Array.from(next))
  }

  const radio = 'flex items-center gap-2 text-xs text-kb-text-secondary cursor-pointer'
  return (
    <div className="space-y-1.5">
      <label className="text-[11px] font-medium text-kb-text-secondary">Clusters</label>
      <label className={radio}>
        <input type="radio" checked={all} onChange={() => onChange(null)} />
        Every cluster in the organization
      </label>
      <label className={radio}>
        <input type="radio" checked={!all} onChange={() => onChange(value ?? [])} />
        Only these clusters
      </label>
      {!all && (
        <div className="ml-5 max-h-40 overflow-y-auto space-y-1 pt-0.5">
          {isLoading && <p className="text-[11px] text-kb-text-tertiary">Loading clusters…</p>}
          {!isLoading && list.length === 0 && (
            <p className="text-[11px] text-kb-text-tertiary">No clusters in this organization yet.</p>
          )}
          {list.map((c) => {
            const id = c.clusterId
            return (
              <label
                key={c.context}
                className={`flex items-center gap-2 text-xs ${id ? 'text-kb-text-secondary cursor-pointer' : 'text-kb-text-tertiary cursor-not-allowed'}`}
                title={id ? undefined : 'This cluster has not reported its id yet, so a token cannot be limited to it.'}
              >
                <input type="checkbox" disabled={!id} checked={!!id && selected.has(id)} onChange={() => id && toggle(id)} />
                <span className="truncate">{parseClusterDisplayName(c)}</span>
              </label>
            )
          })}
        </div>
      )}
      <p className="text-[11px] text-kb-text-secondary">
        {all
          ? 'The token also reads clusters added later.'
          : 'The token reads only the clusters you tick. You can change this after creating it.'}
      </p>
    </div>
  )
}
