import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import type { InsightParams } from '@/types/kubernetes'

export function useInsights(params?: InsightParams) {
  return useQuery({
    queryKey: ['insights', params],
    queryFn: () => api.getInsights(params),
    refetchInterval: 30_000,
    retry: 2,
  })
}
