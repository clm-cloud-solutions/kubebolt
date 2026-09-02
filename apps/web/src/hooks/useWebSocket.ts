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

/**
 * useWebSocket — el canal en vivo de los eventos de recursos del cluster activo.
 *
 * `enabled` existe para el ámbito GLOBAL (Home / Fleet / Security / Admin), donde
 * ninguna pantalla lee del cluster activo: el socket sirve el firehose de
 * informers de UN cluster a páginas que están describiendo la flota entera. Ver
 * `utils/scope.ts` y §5 del plan de dos ámbitos.
 *
 * Se apaga desconectando, no "suscribiéndose a nada". El protocolo de
 * suscripción por tipo se ELIMINÓ el 2026-08-19: nunca llegó a aplicarse (el
 * cliente mandaba `{type,resources}` y el servidor leía `{action,types}`) y sus
 * vocabularios tampoco casaban — el cliente listaba KINDS y la puerta se
 * consultaba con TIPOS DE MENSAJE. Alinear solo los nombres habría dejado mudos
 * a todos los navegadores de golpe. Hoy el socket se acota por (org, cluster) y
 * ya está; ver la nota en `internal/websocket/client.go`.
 */
export function useWebSocket(enabled = true) {
  const queryClient = useQueryClient()

  useEffect(() => {
    if (!enabled) {
      // Suelta el socket al SALIR a global. Sin esto, entrar a un cluster y
      // volver a Home dejaría el firehose corriendo de fondo el resto de la
      // sesión, invalidando queries que la página global no usa.
      wsManager.disconnect()
      return
    }
    wsManager.connect()

    let overviewTimer: ReturnType<typeof setTimeout> | null = null
    let topologyTimer: ReturnType<typeof setTimeout> | null = null

    const unsubscribe = wsManager.onMessage((event) => {
      const payload = event as unknown as WSPayload

      // Connector recovery — agent-proxy cluster came back up after
      // the boot-restore + reconnect race. Invalidate immediately so
      // the user doesn't sit on a stale "Cluster unreachable" page
      // until the next 30s refetch tick. Bail before the
      // resource-detail path since this message has no metadata.
      if (payload.type === 'cluster:connected') {
        queryClient.invalidateQueries({ queryKey: ['clusters'] })
        queryClient.invalidateQueries({ queryKey: ['cluster-overview'] })
        return
      }

      // Insight lifecycle. The engine has always broadcast these the moment a
      // rule starts or stops firing, but nothing consumed them — the Insights
      // view waited on its refetchInterval, so a recovered workload's insight
      // stayed on screen for up to a full refresh cycle after it cleared. This
      // is the channel that makes an insight disappear the way it appears.
      //
      // ClusterOverview carries InsightCount (the sidebar/dashboard badge), so
      // it has to move in step or the badge contradicts the list.
      //
      // Bail before the resource-detail path: the payload is an Insight, not a
      // K8s object, so it has no metadata and would otherwise fall through to
      // the debounced overview/topology invalidation for nothing.
      if (payload.type === 'insight:new' || payload.type === 'insight:resolved') {
        queryClient.invalidateQueries({ queryKey: ['insights'] })
        queryClient.invalidateQueries({ queryKey: ['cluster-overview'] })
        return
      }

      const ns = payload.data?.metadata?.namespace
      const name = payload.data?.metadata?.name

      // List queries are NOT invalidated here on purpose. Earlier we
      // prefix-invalidated everything under ['resources'] so users saw
      // Kobi/Action changes instantly, but the side effect was that
      // any informer event in an active cluster (rolling updates,
      // kubelet status churn) refetched the list dozens of times per
      // second — list reorders, table flicker. Lists now refresh only
      // via the user-configured refetchInterval (RefreshContext) or a
      // manual refresh. Mutation handlers can opt in by calling
      // queryClient.invalidateQueries(['resources', type]) themselves
      // when post-action freshness matters.

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
  }, [enabled])
}
