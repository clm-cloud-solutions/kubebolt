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
    refetchInterval: 30_000,
  })
}

export function useResourceYAML(type: string, namespace: string, name: string) {
  return useQuery({
    queryKey: ['resource-yaml', type, namespace, name],
    queryFn: () => api.getResourceYAML(type, namespace, name),
    enabled: !!type && !!namespace && !!name,
  })
}

export function useTopology() {
  return useQuery({
    queryKey: ['topology'],
    queryFn: () => api.getTopology(),
    staleTime: 30_000,
  })
}

export function usePodLogs(namespace: string, name: string, container: string, tailLines: number) {
  return useQuery({
    queryKey: ['pod-logs', namespace, name, container, tailLines],
    queryFn: () => api.getPodLogs(namespace, name, container || undefined, tailLines),
    enabled: !!namespace && !!name,
    refetchInterval: 10_000,
  })
}

export function useResourceEvents(kind: string, namespace: string, name: string) {
  return useQuery({
    queryKey: ['resource-events', kind, namespace, name],
    queryFn: () => api.getEvents({ namespace: namespace === '_' ? undefined : namespace, involvedKind: kind, involvedName: name }),
    enabled: !!kind && !!name,
    refetchInterval: 30_000,
  })
}
