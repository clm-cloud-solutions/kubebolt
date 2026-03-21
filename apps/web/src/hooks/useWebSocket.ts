import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { wsManager } from '@/services/websocket'
import type { WSEvent } from '@/types/kubernetes'

export function useWebSocket(resources: string[]) {
  const queryClient = useQueryClient()

  useEffect(() => {
    wsManager.connect()
    wsManager.subscribe(resources)

    const unsubscribe = wsManager.onMessage((event: WSEvent) => {
      // Invalidate queries for the affected resource type
      queryClient.invalidateQueries({ queryKey: ['resources', event.resource] })
      queryClient.invalidateQueries({ queryKey: ['cluster-overview'] })
    })

    return () => {
      unsubscribe()
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [resources])
}
