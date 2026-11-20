import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { api } from '@/services/api'
import { useSwitchCluster } from '@/hooks/useSwitchCluster'
import type { ClusterInfo } from '@/types/kubernetes'

// Home is a global page that links into cluster pages: an episode, a Kobi
// conversation. A link that names another cluster must land IN that cluster —
// the page it opens reads the active one (finding #81).
//
// The episode detail made switching explicit, never automatic (2026-09-01): a
// switch drops port-forwards and reloads every cluster query. From Home the
// click on a row that names another cluster IS the explicit act, so these links
// switch first. A cluster that is no longer registered cannot be switched to;
// its link opens as it is (the episode detail reads dead clusters).

/** The cluster a link must switch to before opening, or null when none is
 * needed: the target is already active, or it is not in the cluster list (a
 * removed cluster). Accepts a cluster UID (episodes) or a context name (Kobi
 * conversations). */
export function clusterSwitchTarget(
  clusters: ClusterInfo[] | undefined,
  idOrContext: string | undefined,
): ClusterInfo | null {
  if (!idOrContext || !clusters?.length) return null
  // The UID first: it is the stable identity. A context name can be renamed.
  const hit =
    clusters.find((c) => !!c.clusterId && c.clusterId === idOrContext) ??
    clusters.find((c) => c.context === idOrContext || c.context === `agent:${idOrContext}`)
  if (!hit || hit.active) return null
  return hit
}

export function useOpenInCluster() {
  const { data: clusters } = useQuery({ queryKey: ['clusters'], queryFn: api.listClusters })
  const switchMutation = useSwitchCluster({ goHome: false })
  const navigate = useNavigate()

  const targetFor = (idOrContext?: string) => clusterSwitchTarget(clusters, idOrContext)

  /** Switch to the cluster when needed, then navigate. A switch that fails still
   * navigates: the pages these links open read any cluster of the org, and a
   * click that does nothing reads as a dead link. */
  const open = async (idOrContext: string | undefined, to: string) => {
    const target = targetFor(idOrContext)
    if (target) {
      try {
        await switchMutation.mutateAsync(target.context)
      } catch {
        // the overlay already said it failed; the page opens anyway
      }
    }
    navigate(to)
  }

  /** Switch to a cluster; true once the switch AND its cleanup landed (the
   * mutation's onSuccess clears Kobi's transcript, so anything that opens a
   * conversation must wait for this, not start alongside it). */
  const switchTo = async (context: string): Promise<boolean> => {
    try {
      await switchMutation.mutateAsync(context)
      return true
    } catch {
      return false
    }
  }

  return { targetFor, open, switchTo, switching: switchMutation.isPending }
}
