import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { wsManager } from '@/services/websocket'

// The backend broadcasts informer events as `{type, data: <K8sObject>}`.
// Informer-cached objects don't carry TypeMeta (kind/apiVersion are
// empty), so we route invalidation by namespace + name from
// `data.metadata` and let TanStack Query's predicate match the
// detail-page query keys regardless of which kind they correspond
// to. List-query invalidation stays broad — only active queries
// refetch, so the cost is bounded.
interface WSPayload {
  type: string
  data?: {
    metadata?: { namespace?: string; name?: string }
  }
}

export function useWebSocket(resources: string[]) {
  const queryClient = useQueryClient()

  useEffect(() => {
    wsManager.connect()
    wsManager.subscribe(resources)

    let overviewTimer: ReturnType<typeof setTimeout> | null = null
    let topologyTimer: ReturnType<typeof setTimeout> | null = null

    const unsubscribe = wsManager.onMessage((event) => {
      const payload = event as unknown as WSPayload
      const ns = payload.data?.metadata?.namespace
      const name = payload.data?.metadata?.name

      // List queries: prefix-invalidate everything under ['resources'].
      // Inactive queries just go stale; only mounted list views refetch.
      queryClient.invalidateQueries({ queryKey: ['resources'] })

      // Detail page queries: ['resource-detail', type, ns, name]. Match
      // by ns+name since the kind isn't on the wire. Only one detail
      // page is mounted at a time, so over-invalidation is bounded.
      if (ns && name) {
        queryClient.invalidateQueries({
          predicate: (q) =>
            q.queryKey[0] === 'resource-detail' &&
            q.queryKey[2] === ns &&
            q.queryKey[3] === name,
        })
      }

      // Cluster-scoped resources have empty namespace; the detail page
      // stores them under '_'. Match those separately.
      if (!ns && name) {
        queryClient.invalidateQueries({
          predicate: (q) =>
            q.queryKey[0] === 'resource-detail' &&
            q.queryKey[2] === '_' &&
            q.queryKey[3] === name,
        })
      }

      // Debounce overview invalidation — many WS events can fire rapidly
      if (!overviewTimer) {
        overviewTimer = setTimeout(() => {
          overviewTimer = null
          queryClient.invalidateQueries({ queryKey: ['cluster-overview'] })
        }, 2000)
      }

      // Topology drives the Cluster Map. The backend already coalesces
      // rebuilds inside scheduleTopologyRebuild (2s debounce), so matching
      // that cadence on the client avoids fetching graphs that the server
      // hasn't rebuilt yet, while still keeping the map fresh under bursts
      // (e.g. rolling updates that fire dozens of events per second).
      if (!topologyTimer) {
        topologyTimer = setTimeout(() => {
          topologyTimer = null
          queryClient.invalidateQueries({ queryKey: ['topology'] })
        }, 2000)
      }
    })

    return () => {
      unsubscribe()
      if (overviewTimer) clearTimeout(overviewTimer)
      if (topologyTimer) clearTimeout(topologyTimer)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [resources])
}
