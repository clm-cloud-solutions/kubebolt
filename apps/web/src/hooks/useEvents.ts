import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import type { EventParams } from '@/types/kubernetes'

export function useEvents(params?: EventParams) {
  return useQuery({
    queryKey: ['events', params],
    queryFn: () => api.getEvents(params),
    refetchInterval: 15_000,
    retry: 2,
  })
}
