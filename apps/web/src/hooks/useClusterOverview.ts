import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { useRefreshInterval } from '@/contexts/RefreshContext'

export function useClusterOverview() {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: ['cluster-overview'],
    queryFn: api.getOverview,
    refetchInterval: interval,
    retry: 2,
  })
}
