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

// Topology drives both the Cluster Map and the "Related" tab on detail
// pages. Real-time freshness comes from the WS handler in useWebSocket
// (debounced 2s, matching the backend's scheduleTopologyRebuild). The
// 60s refetchInterval is a fallback for when the WS connection is down
// or stale; staleTime keeps remounts from re-fetching unnecessarily.
export function useTopology() {
  return useQuery({
    queryKey: ['topology'],
    queryFn: () => api.getTopology(),
    refetchInterval: 60_000,
    staleTime: 30_000,
    retry: 2,
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
    // Adaptive polling: fast (3s) while the pod list is empty,
    // global interval once pods exist. The Job controller schedules
    // the first pod within a few seconds of Job creation; without
    // this, an operator who triggered a CronJob waits up to a full
    // refresh interval (default 15s) before "No pods found"
    // resolves. Once pods are present we drop back to the user's
    // chosen interval to avoid hammering the API for steady-state
    // observation.
    refetchInterval: (query) => {
      const data = query.state.data as { items?: unknown[] } | undefined
      if (!data || !data.items || data.items.length === 0) return 3000
      return interval
    },
  })
}

export function useDeploymentHistory(namespace: string, name: string) {
  return useQuery({
    queryKey: ['deployment-history', namespace, name],
    queryFn: () => api.getDeploymentHistory(namespace, name),
    enabled: !!namespace && !!name,
  })
}

// useRolloutHistory returns the rich per-revision payload that the
// rollout-history UI needs (multi-container images, change-cause
// annotation, current-revision marker). Works for deployments,
// statefulsets, and daemonsets — same shape, same query key root.
//
// Refresh interval is intentionally low (5s) when expanded: the
// "Active" flag flips during a rollout and operators watch it live.
export function useRolloutHistory(type: string, namespace: string, name: string) {
  return useQuery({
    queryKey: ['rollout-history', type, namespace, name],
    queryFn: () => api.getRolloutHistory(type, namespace, name),
    enabled: !!type && !!namespace && !!name,
    refetchInterval: 5000,
  })
}

// Logs have their own fixed refresh interval (independent of global setting).
// When a closed time-window or previous-container query is active, auto-refresh
// is disabled — the result is historical and shouldn't churn.
export interface PodLogsOptions {
  since?: string
  sinceTime?: string
  endTime?: string
  previous?: boolean
  timestamps?: boolean
}
export function usePodLogs(
  namespace: string,
  name: string,
  container: string,
  tailLines: number,
  opts?: PodLogsOptions,
) {
  const historical = !!(opts?.sinceTime || opts?.endTime || opts?.previous)
  return useQuery({
    queryKey: ['pod-logs', namespace, name, container, tailLines, opts],
    queryFn: () => api.getPodLogs(namespace, name, container || undefined, tailLines, opts),
    enabled: !!namespace && !!name,
    refetchInterval: historical ? false : 10_000,
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
