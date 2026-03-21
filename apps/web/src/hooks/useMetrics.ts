import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

export function useMetrics(type: string, namespace: string, name: string) {
  return useQuery({
    queryKey: ['metrics', type, namespace, name],
    queryFn: () => api.getMetrics(type, namespace, name),
    enabled: !!type && !!namespace && !!name,
    refetchInterval: 15_000,
    retry: 1,
  })
}
