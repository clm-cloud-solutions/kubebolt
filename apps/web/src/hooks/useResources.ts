import { useQuery, keepPreviousData } from '@tanstack/react-query'
import { api } from '@/services/api'
import { useRefreshInterval } from '@/contexts/RefreshContext'
import type { ResourceParams } from '@/types/kubernetes'

export function useResources(type: string, params?: ResourceParams) {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: ['resources', type, params],
    queryFn: () => api.getResources(type, params),
    refetchInterval: interval,
    retry: 2,
    placeholderData: keepPreviousData,
  })
}

export function useResourceDetail(type: string, namespace: string, name: string) {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: ['resource-detail', type, namespace, name],
    queryFn: () => api.getResourceDetail(type, namespace, name),
    enabled: !!type && !!namespace && !!name,
    refetchInterval: interval,
  })
}

export function useResourceYAML(type: string, namespace: string, name: string) {
  return useQuery({
    queryKey: ['resource-yaml', type, namespace, name],
    queryFn: () => api.getResourceYAML(type, namespace, name),
    enabled: !!type && !!namespace && !!name,
  })
}

export function useResourceDescribe(type: string, namespace: string, name: string, enabled: boolean) {
  return useQuery({
    queryKey: ['resource-describe', type, namespace, name],
    queryFn: () => api.getResourceDescribe(type, namespace, name),
    enabled: enabled && !!type && !!namespace && !!name,
  })
}

export function useTopology() {
  return useQuery({
    queryKey: ['topology'],
    queryFn: () => api.getTopology(),
    staleTime: 30_000,
  })
}

export function useDeploymentPods(namespace: string, name: string) {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: ['deployment-pods', namespace, name],
    queryFn: () => api.getDeploymentPods(namespace, name),
    enabled: !!namespace && !!name,
    refetchInterval: interval,
  })
}

export function useStatefulSetPods(namespace: string, name: string) {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: ['statefulset-pods', namespace, name],
    queryFn: () => api.getStatefulSetPods(namespace, name),
    enabled: !!namespace && !!name,
    refetchInterval: interval,
  })
}

export function useDaemonSetPods(namespace: string, name: string) {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: ['daemonset-pods', namespace, name],
    queryFn: () => api.getDaemonSetPods(namespace, name),
    enabled: !!namespace && !!name,
    refetchInterval: interval,
  })
}

export function useWorkloadHistory(type: string, namespace: string, name: string) {
  return useQuery({
    queryKey: ['workload-history', type, namespace, name],
    queryFn: () => api.getWorkloadHistory(type, namespace, name),
    enabled: !!type && !!namespace && !!name,
  })
}

export function useCronJobJobs(namespace: string, name: string) {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: ['cronjob-jobs', namespace, name],
    queryFn: () => api.getCronJobJobs(namespace, name),
    enabled: !!namespace && !!name,
    refetchInterval: interval,
  })
}

export function useJobPods(namespace: string, name: string) {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: ['job-pods', namespace, name],
    queryFn: () => api.getJobPods(namespace, name),
    enabled: !!namespace && !!name,
    refetchInterval: interval,
  })
}

export function useDeploymentHistory(namespace: string, name: string) {
  return useQuery({
    queryKey: ['deployment-history', namespace, name],
    queryFn: () => api.getDeploymentHistory(namespace, name),
    enabled: !!namespace && !!name,
  })
}

// Logs have their own fixed refresh interval (independent of global setting)
export function usePodLogs(namespace: string, name: string, container: string, tailLines: number) {
  return useQuery({
    queryKey: ['pod-logs', namespace, name, container, tailLines],
    queryFn: () => api.getPodLogs(namespace, name, container || undefined, tailLines),
    enabled: !!namespace && !!name,
    refetchInterval: 10_000,
  })
}

export function useResourceEvents(kind: string, namespace: string, name: string) {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: ['resource-events', kind, namespace, name],
    queryFn: () => api.getEvents({ namespace: namespace === '_' ? undefined : namespace, involvedKind: kind, involvedName: name }),
    enabled: !!kind && !!name,
    refetchInterval: interval,
  })
}
