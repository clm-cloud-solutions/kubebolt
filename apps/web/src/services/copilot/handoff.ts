import type { ClusterInfo } from '@/types/kubernetes'

/**
 * handoffPrefix attributes what was learned before a cluster switch, so the
 * conversation opened on the other side does not restate it as its own
 * finding — and says WHERE it was learned. From a cluster page it names that
 * cluster: every handoff used to read "(From the fleet view: …)", and a Kobi
 * opened on kubeshop-back by a switch from kubeshop-front took itself to be
 * at fleet level and offered to switch to the cluster it was already on.
 */
export function handoffPrefix(established: string, fromCluster: string | null): string {
  if (!established) return ''
  const source = fromCluster ? `From cluster ${fromCluster}` : 'From the fleet view'
  return `(${source}: ${established})\n\n`
}

/**
 * handoffOrigin is the cluster a switch leaves, named the way the switcher
 * names it; null when the switch starts from a global page (Home, Fleet) or
 * no cluster is selected.
 */
export function handoffOrigin(
  scope: string,
  activeContext: string | null,
  clusters: ClusterInfo[] | undefined,
): string | null {
  if (scope !== 'cluster' || !activeContext) return null
  const c = clusters?.find((x) => x.context === activeContext)
  return c?.displayName || c?.name || activeContext
}
