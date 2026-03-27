import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { wsManager } from '@/services/websocket'
import type { WSEvent } from '@/types/kubernetes'

export function useWebSocket(resources: string[]) {
  const queryClient = useQueryClient()

  useEffect(() => {
    wsManager.connect()
    wsManager.subscribe(resources)

    // Debounce overview invalidation to prevent request storms
    let overviewTimer: ReturnType<typeof setTimeout> | null = null

    const unsubscribe = wsManager.onMessage((event: WSEvent) => {
      // Invalidate queries for the affected resource type
      queryClient.invalidateQueries({ queryKey: ['resources', event.resource] })

      // Debounce overview invalidation — many WS events can fire rapidly
      if (!overviewTimer) {
        overviewTimer = setTimeout(() => {
          overviewTimer = null
          queryClient.invalidateQueries({ queryKey: ['cluster-overview'] })
        }, 2000)
      }
    })

    return () => {
      unsubscribe()
      if (overviewTimer) clearTimeout(overviewTimer)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [resources])
}
