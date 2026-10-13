import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

type SearchHit = { name: string; namespace: string; kind: string; resourceType: string }

// The workload kinds a name can resolve to, in the order a tie is broken.
const WORKLOAD_TYPES = ['deployments', 'statefulsets', 'daemonsets', 'cronjobs', 'jobs']

// workloadPathFrom — the detail page of the workload with exactly this name in
// this namespace, among the search hits; null when none is a workload.
export function workloadPathFrom(hits: SearchHit[], namespace: string, name: string): string | null {
  for (const type of WORKLOAD_TYPES) {
    const hit = hits.find((h) => h.resourceType === type && h.name === name && h.namespace === namespace)
    if (hit) return `/${type}/${namespace}/${name}`
  }
  return null
}

// podsPathFor — the pods of a workload, by name, while its kind is unknown.
export function podsPathFor(namespace: string, name: string): string {
  return `/pods?namespace=${encodeURIComponent(namespace)}&search=${encodeURIComponent(name)}`
}

// useWorkloadPath — where to send a click on a workload known only by name
// and namespace (Hubble's collapsed workload, a pod's prefix). Its kind comes
// from /search, never from a guess; until that answers, or when nothing
// matches, the link opens the workload's pods.
export function useWorkloadPath(namespace: string | undefined, name: string | undefined): string {
  const q = useQuery({
    queryKey: ['workload-path', namespace, name],
    queryFn: () => api.search(name ?? ''),
    enabled: !!namespace && !!name,
    staleTime: 5 * 60_000,
    retry: false,
  })
  if (!namespace || !name) return '/pods'
  return workloadPathFrom(q.data ?? [], namespace, name) ?? podsPathFor(namespace, name)
}
