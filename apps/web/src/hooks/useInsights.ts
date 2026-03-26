import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { useRefreshInterval } from '@/contexts/RefreshContext'
import type { InsightParams } from '@/types/kubernetes'

export function useInsights(params?: InsightParams) {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: ['insights', params],
    queryFn: () => api.getInsights(params),
    refetchInterval: interval,
    retry: 2,
  })
}
