import type {
  ClusterOverview,
  ClusterHealth,
  ClusterInfo,
  ResourceList,
  Topology,
  Insight,
  ResourceMetrics,
  ResourceParams,
  InsightParams,
  EventParams,
  ResourceItem,
} from '@/types/kubernetes'

const API_BASE = '/api/v1'

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
    this.name = 'ApiError'
  }
}

async function extractErrorMessage(res: Response): Promise<string> {
  try {
    const json = await res.json()
    return json.error || json.message || res.statusText
  } catch {
    return res.text().catch(() => res.statusText)
  }
}

async function fetchJSON<T>(url: string): Promise<T> {
  const res = await fetch(url)
  if (!res.ok) {
    throw new ApiError(res.status, await extractErrorMessage(res))
  }
  return res.json()
}

function buildQuery(params?: Record<string, string | number | boolean | undefined | null>): string {
  if (!params) return ''
  const query = new URLSearchParams()
  Object.entries(params).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') {
      query.set(k, String(v))
    }
  })
  const str = query.toString()
  return str ? `?${str}` : ''
}

async function deleteRequest<T>(url: string): Promise<T> {
  const res = await fetch(url, { method: 'DELETE' })
  if (!res.ok) {
    throw new ApiError(res.status, await extractErrorMessage(res))
  }
  return res.json()
}

async function postJSON<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    throw new ApiError(res.status, await extractErrorMessage(res))
  }
  return res.json()
}

export const api = {
  // Cluster management
  listClusters: () => fetchJSON<ClusterInfo[]>(`${API_BASE}/clusters`),

  switchCluster: (context: string) =>
    postJSON<{ status: string; context: string }>(`${API_BASE}/clusters/switch`, { context }),

  getOverview: () => fetchJSON<ClusterOverview>(`${API_BASE}/cluster/overview`),

  getHealth: () => fetchJSON<ClusterHealth>(`${API_BASE}/cluster/health`),

  getResources: (type: string, params?: ResourceParams) =>
    fetchJSON<ResourceList>(`${API_BASE}/resources/${type}${buildQuery(params as Record<string, string | number | undefined>)}`),

  getResourceDetail: (type: string, namespace: string, name: string) =>
    fetchJSON<ResourceItem>(`${API_BASE}/resources/${type}/${namespace}/${name}`),

  getTopology: () => fetchJSON<Topology>(`${API_BASE}/topology`),

  getInsights: (params?: InsightParams) =>
    fetchJSON<{ items: Insight[]; total: number }>(`${API_BASE}/insights${buildQuery(params as Record<string, string | undefined>)}`),

  getEvents: (params?: EventParams) =>
    fetchJSON<ResourceList>(`${API_BASE}/events${buildQuery(params as Record<string, string | number | undefined>)}`),

  getMetrics: (type: string, namespace: string, name: string) =>
    fetchJSON<ResourceMetrics>(`${API_BASE}/metrics/${type}/${namespace}/${name}`),

  getResourceDescribe: async (type: string, namespace: string, name: string): Promise<string> => {
    const res = await fetch(`${API_BASE}/resources/${type}/${namespace}/${name}/describe`)
    if (!res.ok) throw new ApiError(res.status, await extractErrorMessage(res))
    return res.text()
  },

  getResourceYAML: async (type: string, namespace: string, name: string): Promise<string> => {
    const res = await fetch(`${API_BASE}/resources/${type}/${namespace}/${name}/yaml`)
    if (!res.ok) throw new ApiError(res.status, await extractErrorMessage(res))
    return res.text()
  },

  getDeploymentPods: (namespace: string, name: string) =>
    fetchJSON<ResourceList>(`${API_BASE}/resources/deployments/${namespace}/${name}/pods`),

  getDeploymentHistory: (namespace: string, name: string) =>
    fetchJSON<{ items: ResourceItem[]; total: number }>(`${API_BASE}/resources/deployments/${namespace}/${name}/history`),

  getStatefulSetPods: (namespace: string, name: string) =>
    fetchJSON<ResourceList>(`${API_BASE}/resources/statefulsets/${namespace}/${name}/pods`),

  getDaemonSetPods: (namespace: string, name: string) =>
    fetchJSON<ResourceList>(`${API_BASE}/resources/daemonsets/${namespace}/${name}/pods`),

  getJobPods: (namespace: string, name: string) =>
    fetchJSON<ResourceList>(`${API_BASE}/resources/jobs/${namespace}/${name}/pods`),

  getPodLogs: async (namespace: string, name: string, container?: string, tailLines?: number): Promise<string> => {
    const params = new URLSearchParams()
    if (container) params.set('container', container)
    if (tailLines) params.set('tailLines', String(tailLines))
    const query = params.toString()
    const res = await fetch(`${API_BASE}/resources/pods/${namespace}/${name}/logs${query ? `?${query}` : ''}`)
    if (!res.ok) throw new ApiError(res.status, await extractErrorMessage(res))
    return res.text()
  },

  // Resource actions
  restartResource: (type: string, namespace: string, name: string) =>
    postJSON<{ status: string }>(`${API_BASE}/resources/${type}/${namespace}/${name}/restart`, {}),

  scaleResource: (type: string, namespace: string, name: string, replicas: number) =>
    postJSON<{ status: string; fromReplicas: number; toReplicas: number }>(`${API_BASE}/resources/${type}/${namespace}/${name}/scale`, { replicas }),

  // Port forwarding
  createPortForward: (body: { namespace: string; pod: string; container?: string; remotePort: number }) =>
    postJSON<{ id: string; url: string; namespace: string; pod: string; remotePort: number; localPort: number; status: string; createdAt: string }>(
      `${API_BASE}/portforward`, body
    ),

  listPortForwards: () =>
    fetchJSON<Array<{ id: string; namespace: string; pod: string; remotePort: number; localPort: number; url: string; status: string; error?: string; createdAt: string }>>(
      `${API_BASE}/portforward`
    ),

  deletePortForward: (id: string) =>
    deleteRequest<{ status: string }>(`${API_BASE}/portforward/${id}`),
}
