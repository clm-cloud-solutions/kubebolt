import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import type { ResourceParams } from '@/types/kubernetes'

export function useResources(type: string, params?: ResourceParams) {
  return useQuery({
    queryKey: ['resources', type, params],
    queryFn: () => api.getResources(type, params),
    refetchInterval: 30_000,
    retry: 2,
  })
}

export function useResourceDetail(type: string, namespace: string, name: string) {
  return useQuery({
    queryKey: ['resource-detail', type, namespace, name],
    queryFn: () => api.getResourceDetail(type, namespace, name),
    enabled: !!type && !!namespace && !!name,
  })
}
