import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { useRefreshInterval } from '@/contexts/RefreshContext'
import type { EventParams } from '@/types/kubernetes'

export function useEvents(params?: EventParams) {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: ['events', params],
    queryFn: () => api.getEvents(params),
    refetchInterval: interval,
    retry: 2,
  })
}
