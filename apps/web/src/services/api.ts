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
import type { AuthConfig, AuthUser, LoginResponse, RefreshResponse } from '@/types/auth'

const API_BASE = '/api/v1'

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
    this.name = 'ApiError'
  }
}

// --- Token management (in-memory, not localStorage) ---

let accessToken: string | null = null
let refreshPromise: Promise<string | null> | null = null

export function setAccessToken(token: string | null) {
  accessToken = token
}

export function getAccessToken(): string | null {
  return accessToken
}

export function clearAccessToken() {
  accessToken = null
}

// --- Fetch helpers with auth ---

async function extractErrorMessage(res: Response): Promise<string> {
  try {
    const json = await res.json()
    return json.error || json.message || res.statusText
  } catch {
    return res.text().catch(() => res.statusText)
  }
}

function authHeaders(): Record<string, string> {
  if (accessToken) {
    return { Authorization: `Bearer ${accessToken}` }
  }
  return {}
}

async function tryRefreshToken(): Promise<string | null> {
  // Deduplicate concurrent refresh calls
  if (refreshPromise) return refreshPromise
  refreshPromise = (async () => {
    try {
      const res = await fetch(`${API_BASE}/auth/refresh`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: '{}',
      })
      if (!res.ok) return null
      const data: RefreshResponse = await res.json()
      accessToken = data.accessToken
      return data.accessToken
    } catch {
      return null
    } finally {
      refreshPromise = null
    }
  })()
  return refreshPromise
}

async function fetchWithAuth(url: string, init?: RequestInit): Promise<Response> {
  const headers = { ...authHeaders(), ...init?.headers }
  let res = await fetch(url, { ...init, headers, credentials: 'include' })

  // On 401, try one silent refresh then retry
  if (res.status === 401 && accessToken) {
    const newToken = await tryRefreshToken()
    if (newToken) {
      const retryHeaders = { ...init?.headers, Authorization: `Bearer ${newToken}` }
      res = await fetch(url, { ...init, headers: retryHeaders, credentials: 'include' })
    }
  }

  return res
}

async function fetchJSON<T>(url: string): Promise<T> {
  const res = await fetchWithAuth(url)
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
  const res = await fetchWithAuth(url, { method: 'DELETE' })
  if (!res.ok) {
    throw new ApiError(res.status, await extractErrorMessage(res))
  }
  return res.json()
}

async function postJSON<T>(url: string, body: unknown): Promise<T> {
  const res = await fetchWithAuth(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    throw new ApiError(res.status, await extractErrorMessage(res))
  }
  return res.json()
}

async function putJSON<T>(url: string, body: unknown, headers?: Record<string, string>): Promise<T> {
  const res = await fetchWithAuth(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', ...headers },
    body: typeof body === 'string' ? body : JSON.stringify(body),
  })
  if (!res.ok) {
    throw new ApiError(res.status, await extractErrorMessage(res))
  }
  return res.json()
}

export const api = {
  // --- Auth ---
  getAuthConfig: () =>
    fetchJSON<AuthConfig>(`${API_BASE}/auth/config`),

  login: (username: string, password: string) =>
    postJSON<LoginResponse>(`${API_BASE}/auth/login`, { username, password }),

  refresh: () => tryRefreshToken(),

  logout: () => postJSON<{ status: string }>(`${API_BASE}/auth/logout`, {}),

  getMe: () => fetchJSON<AuthUser>(`${API_BASE}/auth/me`),

  changePassword: (currentPassword: string, newPassword: string) =>
    putJSON<{ status: string }>(`${API_BASE}/auth/me/password`, { currentPassword, newPassword }),

  // --- User management (admin) ---
  listUsers: () => fetchJSON<AuthUser[]>(`${API_BASE}/users`),

  createUser: (data: { username: string; email: string; name: string; password: string; role: string }) =>
    postJSON<AuthUser>(`${API_BASE}/users`, data),

  updateUser: (id: string, data: { username?: string; email?: string; name?: string; role?: string }) =>
    putJSON<AuthUser>(`${API_BASE}/users/${id}`, data),

  resetUserPassword: (id: string, password: string) =>
    putJSON<{ status: string }>(`${API_BASE}/users/${id}/password`, { password }),

  deleteUser: (id: string) =>
    deleteRequest<{ status: string }>(`${API_BASE}/users/${id}`),

  // --- Cluster management ---
  listClusters: () => fetchJSON<ClusterInfo[]>(`${API_BASE}/clusters`),

  switchCluster: (context: string) =>
    postJSON<{ status: string; context: string }>(`${API_BASE}/clusters/switch`, { context }),

  addCluster: (kubeconfig: string) =>
    postJSON<{ added: string[] }>(`${API_BASE}/clusters`, { kubeconfig }),

  renameCluster: (context: string, displayName: string) =>
    putJSON<{ status: string }>(`${API_BASE}/clusters/${encodeURIComponent(context)}/rename`, { displayName }),

  deleteCluster: (context: string) =>
    deleteRequest<{ status: string }>(`${API_BASE}/clusters/${encodeURIComponent(context)}`),

  // --- Notifications (admin) ---
  getNotificationsConfig: () =>
    fetchJSON<import('@/types/auth').NotificationsConfig>(`${API_BASE}/notifications/config`),

  testNotification: (channel: 'slack' | 'discord') =>
    postJSON<{ status: string }>(`${API_BASE}/notifications/test/${channel}`, {}),

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
    const res = await fetchWithAuth(`${API_BASE}/resources/${type}/${namespace}/${name}/describe`)
    if (!res.ok) throw new ApiError(res.status, await extractErrorMessage(res))
    return res.text()
  },

  applyResourceYAML: async (type: string, namespace: string, name: string, yaml: string): Promise<{ status: string }> => {
    const res = await fetchWithAuth(`${API_BASE}/resources/${type}/${namespace}/${name}/yaml`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/yaml' },
      body: yaml,
    })
    if (!res.ok) throw new ApiError(res.status, await extractErrorMessage(res))
    return res.json()
  },

  getResourceYAML: async (type: string, namespace: string, name: string): Promise<string> => {
    const res = await fetchWithAuth(`${API_BASE}/resources/${type}/${namespace}/${name}/yaml`)
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

  getWorkloadHistory: (type: string, namespace: string, name: string) =>
    fetchJSON<{ items: ResourceItem[]; total: number }>(`${API_BASE}/resources/${type}/${namespace}/${name}/history`),

  getCronJobJobs: (namespace: string, name: string) =>
    fetchJSON<ResourceList>(`${API_BASE}/resources/cronjobs/${namespace}/${name}/jobs`),

  getJobPods: (namespace: string, name: string) =>
    fetchJSON<ResourceList>(`${API_BASE}/resources/jobs/${namespace}/${name}/pods`),

  getPodLogs: async (namespace: string, name: string, container?: string, tailLines?: number): Promise<string> => {
    const params = new URLSearchParams()
    if (container) params.set('container', container)
    if (tailLines) params.set('tailLines', String(tailLines))
    const query = params.toString()
    const res = await fetchWithAuth(`${API_BASE}/resources/pods/${namespace}/${name}/logs${query ? `?${query}` : ''}`)
    if (!res.ok) throw new ApiError(res.status, await extractErrorMessage(res))
    return res.text()
  },

  // Pod files
  listFiles: (namespace: string, name: string, container: string, path: string) =>
    fetchJSON<{ path: string; items: Array<{ name: string; type: string; size: string; modified: string; permissions: string }> }>(
      `${API_BASE}/resources/pods/${namespace}/${name}/files?container=${encodeURIComponent(container)}&path=${encodeURIComponent(path)}`
    ),

  getFileContent: async (namespace: string, name: string, container: string, path: string): Promise<string> => {
    const res = await fetchWithAuth(`${API_BASE}/resources/pods/${namespace}/${name}/files/content?container=${encodeURIComponent(container)}&path=${encodeURIComponent(path)}`)
    if (!res.ok) throw new ApiError(res.status, await extractErrorMessage(res))
    return res.text()
  },

  getFileDownloadUrl: (namespace: string, name: string, container: string, path: string) =>
    `${API_BASE}/resources/pods/${namespace}/${name}/files/download?container=${encodeURIComponent(container)}&path=${encodeURIComponent(path)}`,

  // Search
  search: (query: string) =>
    fetchJSON<Array<{ name: string; namespace: string; kind: string; resourceType: string; status: string }>>(
      `${API_BASE}/search?q=${encodeURIComponent(query)}`
    ),

  // Resource actions
  deleteResource: (type: string, namespace: string, name: string, options?: { orphan?: boolean; force?: boolean }) => {
    const params = new URLSearchParams()
    if (options?.orphan) params.set('orphan', 'true')
    if (options?.force) params.set('force', 'true')
    const query = params.toString()
    return deleteRequest<{ status: string }>(`${API_BASE}/resources/${type}/${namespace}/${name}${query ? '?' + query : ''}`)
  },

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

  // Copilot
  getCopilotConfig: () =>
    fetchJSON<{
      enabled: boolean
      provider: string
      model: string
      proxyMode: boolean
      fallback?: { provider: string; model: string }
    }>(`${API_BASE}/copilot/config`),
}
