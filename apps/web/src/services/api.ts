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

  getResourceYAML: async (type: string, namespace: string, name: string): Promise<string> => {
    const res = await fetch(`${API_BASE}/resources/${type}/${namespace}/${name}/yaml`)
    if (!res.ok) throw new ApiError(res.status, await extractErrorMessage(res))
    return res.text()
  },

  getPodLogs: async (namespace: string, name: string, container?: string, tailLines?: number): Promise<string> => {
    const params = new URLSearchParams()
    if (container) params.set('container', container)
    if (tailLines) params.set('tailLines', String(tailLines))
    const query = params.toString()
    const res = await fetch(`${API_BASE}/resources/pods/${namespace}/${name}/logs${query ? `?${query}` : ''}`)
    if (!res.ok) throw new ApiError(res.status, await extractErrorMessage(res))
    return res.text()
  },
}
