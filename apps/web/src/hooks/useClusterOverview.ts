import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

export function useClusterOverview() {
  return useQuery({
    queryKey: ['cluster-overview'],
    queryFn: api.getOverview,
    refetchInterval: 30_000,
    retry: 2,
  })
}
