import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

export function useTopology() {
  return useQuery({
    queryKey: ['topology'],
    queryFn: api.getTopology,
    refetchInterval: 30_000,
    retry: 2,
  })
}
