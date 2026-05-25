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
  // Optional structured payload that callers parse out of 4xx
  // responses for tailored UX (typed-confirmation modals, conflict
  // hints, etc.). Backed by the JSON body — `payload?.someKey`
  // pattern keeps callers tolerant when the server omits fields.
  public payload?: Record<string, unknown>
  constructor(public status: number, message: string, payload?: Record<string, unknown>) {
    super(message)
    this.name = 'ApiError'
    this.payload = payload
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

// extractErrorPayload returns both the human message + the parsed
// JSON body so callers can pull structured fields (selfTargetedProxy,
// notManaged, etc.) without re-fetching. The response body is
// consumed once — JSON failure falls back to plain text.
async function extractErrorPayload(res: Response): Promise<{ message: string; payload?: Record<string, unknown> }> {
  try {
    const json = await res.json()
    return {
      message: typeof json === 'object' && json && (json.error || json.message) ? String(json.error || json.message) : res.statusText,
      payload: typeof json === 'object' && json ? (json as Record<string, unknown>) : undefined,
    }
  } catch {
    return { message: await res.text().catch(() => res.statusText) }
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

async function deleteRequest<T>(url: string, headers?: Record<string, string>): Promise<T> {
  const res = await fetchWithAuth(url, { method: 'DELETE', headers })
  if (!res.ok) {
    const { message, payload } = await extractErrorPayload(res)
    throw new ApiError(res.status, message, payload)
  }
  return res.json()
}

// parseJSONOrEmpty safely handles the "200/204 with no body" case that
// trips res.json() with SyntaxError. Endpoints like /admin/setup/
// complete return 204 No Content; their callers type the return as
// `void`, so we just resolve to undefined instead of throwing.
async function parseJSONOrEmpty<T>(res: Response): Promise<T> {
  if (res.status === 204 || res.headers.get('content-length') === '0') {
    return undefined as T
  }
  const text = await res.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}

async function postJSON<T>(url: string, body: unknown, headers?: Record<string, string>): Promise<T> {
  const res = await fetchWithAuth(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...headers },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const { message, payload } = await extractErrorPayload(res)
    throw new ApiError(res.status, message, payload)
  }
  return parseJSONOrEmpty<T>(res)
}

async function putJSON<T>(url: string, body: unknown, headers?: Record<string, string>): Promise<T> {
  const res = await fetchWithAuth(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', ...headers },
    body: typeof body === 'string' ? body : JSON.stringify(body),
  })
  if (!res.ok) {
    const { message, payload } = await extractErrorPayload(res)
    throw new ApiError(res.status, message, payload)
  }
  return parseJSONOrEmpty<T>(res)
}

// putJSONWithWarnings is the variant for endpoints that surface soft
// validation warnings via the X-KubeBolt-Validation-Warnings response
// header (currently: /admin/tenants/:id/limits). Body still parses as
// JSON; warnings are split on "; " to match the server's joiner.
async function putJSONWithWarnings<T>(url: string, body: unknown): Promise<{ data: T; warnings: string[] }> {
  const res = await fetchWithAuth(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    throw new ApiError(res.status, await extractErrorMessage(res))
  }
  const data = (await res.json()) as T
  const raw = res.headers.get('X-KubeBolt-Validation-Warnings') ?? ''
  const warnings = raw ? raw.split('; ').filter(Boolean) : []
  return { data, warnings }
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

  // --- Agent ingest tokens (admin) ---
  //
  // The OSS UI operates on the auto-seeded "default" tenant only.
  // Multi-tenant management UI is ENTERPRISE-CANDIDATE — the backend
  // exposes everything (see auth/tenant_handlers.go) but the OSS
  // frontend deliberately surfaces only the default tenant.
  listTenants: () => fetchJSON<Tenant[]>(`${API_BASE}/admin/tenants`),

  getTenant: (id: string) =>
    fetchJSON<TenantWithTokens>(`${API_BASE}/admin/tenants/${id}`),

  listAgentTokens: (tenantID: string) =>
    fetchJSON<IngestToken[]>(`${API_BASE}/admin/tenants/${tenantID}/tokens`),

  issueAgentToken: (tenantID: string, label: string, ttlSeconds?: number, clusterId?: string) =>
    postJSON<IssuedToken>(`${API_BASE}/admin/tenants/${tenantID}/tokens`, {
      label,
      ttlSeconds: ttlSeconds ?? 0,
      // Pass through when set so the backend can scope the token
      // to a specific cluster. Omitting (or empty) preserves the
      // legacy "any cluster" semantic.
      ...(clusterId ? { clusterId } : {}),
    }),

  rotateAgentToken: (tenantID: string, tokenID: string) =>
    postJSON<IssuedToken>(
      `${API_BASE}/admin/tenants/${tenantID}/tokens/${tokenID}/rotate`,
      {},
    ),

  revokeAgentToken: (tenantID: string, tokenID: string) =>
    deleteRequest<{ status: string }>(
      `${API_BASE}/admin/tenants/${tenantID}/tokens/${tokenID}`,
    ),

  // --- Per-tenant Prom remote_write limits (Phase 3) ---
  //
  // GET returns the three-view DTO: effective (what enforcement uses),
  // custom (the overrides the tenant has applied), defaults (the system
  // fallback so the UI can render Reset). PUT accepts a partial patch —
  // omit a field to leave its current override in place. DELETE clears
  // every override at once.
  getTenantLimits: (tenantID: string) =>
    fetchJSON<LimitsResponse>(`${API_BASE}/admin/tenants/${tenantID}/limits`),

  setTenantLimits: (tenantID: string, patch: TenantLimits) =>
    putJSONWithWarnings<LimitsResponse>(
      `${API_BASE}/admin/tenants/${tenantID}/limits`,
      patch,
    ),

  resetTenantLimits: (tenantID: string) =>
    deleteRequest<LimitsResponse>(`${API_BASE}/admin/tenants/${tenantID}/limits`),

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

  testNotification: (channel: 'slack' | 'discord' | 'email') =>
    postJSON<{ status: string }>(`${API_BASE}/notifications/test/${channel}`, {}),

  // --- Copilot usage analytics (admin) ---
  getCopilotUsageSummary: (range: string) =>
    fetchJSON<import('@/types/copilotUsage').CopilotUsageSummary>(
      `${API_BASE}/admin/copilot/usage/summary?range=${range}`,
    ),

  getCopilotUsageTimeseries: (range: string) =>
    fetchJSON<import('@/types/copilotUsage').CopilotUsageBucket[]>(
      `${API_BASE}/admin/copilot/usage/timeseries?range=${range}`,
    ),

  getCopilotUsageSessions: (range: string, limit = 100) =>
    fetchJSON<import('@/types/copilotUsage').CopilotSessionEnriched[]>(
      `${API_BASE}/admin/copilot/usage/sessions?range=${range}&limit=${limit}`,
    ),

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

  // Detailed history: rich per-revision metadata used by the rollout-
  // history UI (multi-container images, change-cause annotation,
  // current-revision marker). Works for deployments, statefulsets,
  // and daemonsets — the backend dispatches based on resource type.
  getRolloutHistory: (type: string, namespace: string, name: string) => {
    const url =
      type === 'deployments'
        ? `${API_BASE}/resources/deployments/${namespace}/${name}/history?detailed=true`
        : `${API_BASE}/resources/${type}/${namespace}/${name}/history?detailed=true`
    return fetchJSON<RolloutHistory>(url)
  },

  getCronJobJobs: (namespace: string, name: string) =>
    fetchJSON<ResourceList>(`${API_BASE}/resources/cronjobs/${namespace}/${name}/jobs`),

  getJobPods: (namespace: string, name: string) =>
    fetchJSON<ResourceList>(`${API_BASE}/resources/jobs/${namespace}/${name}/pods`),

  getPodLogs: async (
    namespace: string,
    name: string,
    container?: string,
    tailLines?: number,
    opts?: {
      since?: string        // relative duration, e.g. '15m', '1h'
      sinceTime?: string    // RFC3339 absolute lower bound
      endTime?: string      // RFC3339 absolute upper bound
      previous?: boolean    // logs from prior container instance
      timestamps?: boolean  // kubelet-prefixed timestamps on each line
    },
  ): Promise<string> => {
    const params = new URLSearchParams()
    if (container) params.set('container', container)
    if (tailLines) params.set('tailLines', String(tailLines))
    if (opts?.since) params.set('since', opts.since)
    if (opts?.sinceTime) params.set('sinceTime', opts.sinceTime)
    if (opts?.endTime) params.set('endTime', opts.endTime)
    if (opts?.previous) params.set('previous', 'true')
    if (opts?.timestamps) params.set('timestamps', 'true')
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

  // Resource actions. The optional `source` tags the audit log entry —
  // UI buttons leave it default ("ui"); Copilot proposal cards pass
  // "copilot_proposal" via the X-KubeBolt-Action-Source header so the
  // audit trail distinguishes execution paths.
  deleteResource: (
    type: string,
    namespace: string,
    name: string,
    options?: { orphan?: boolean; force?: boolean; source?: string },
  ) => {
    const params = new URLSearchParams()
    if (options?.orphan) params.set('orphan', 'true')
    if (options?.force) params.set('force', 'true')
    const query = params.toString()
    return deleteRequest<{ status: string }>(
      `${API_BASE}/resources/${type}/${namespace}/${name}${query ? '?' + query : ''}`,
      options?.source ? { 'X-KubeBolt-Action-Source': options.source } : undefined,
    )
  },

  // The optional `source` argument tags the audit log entry. UI buttons
  // leave it default ("ui"); Copilot proposal cards pass "copilot_proposal"
  // so the audit trail distinguishes the two execution paths.
  // The `resource` field carries the post-mutation object in the same
  // shape as `useResourceDetail`, so callers can call `setQueryData`
  // and reflect the change without waiting for a WS event or poll.
  restartResource: (type: string, namespace: string, name: string, source?: string) =>
    postJSON<{ status: string; resource: ResourceItem | null }>(
      `${API_BASE}/resources/${type}/${namespace}/${name}/restart`,
      {},
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Evict a Pod via the policy/v1 Eviction API — distinct from
  // `deleteResource('pods', ...)`. Eviction respects PodDisruptionBudgets;
  // when blocked by a PDB the apiserver returns 429 and the backend
  // surfaces a structured payload (`pdbBlocked: true`) the caller can
  // use to render an explicit "blocked by PDB" message instead of a
  // generic 429. Pod-only — the backend rejects other types.
  evictPod: (namespace: string, name: string, source?: string) =>
    postJSON<{ status: string }>(
      `${API_BASE}/resources/pods/${namespace}/${name}/evict`,
      {},
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Spawn an ephemeral debug container inside a running pod. Returns
  // the auto-generated container name so the caller can navigate to
  // the Terminal tab pre-selected on it. Pod-only — backend rejects
  // other types. See spec #09 V2 Item 4 / C1 audit decision.
  debugPod: (
    namespace: string,
    name: string,
    body: { image: string; targetContainer?: string; command?: string[]; shareProcessNamespace?: boolean },
    source?: string,
  ) =>
    postJSON<{ status: string; ephemeralContainerName: string }>(
      `${API_BASE}/resources/pods/${namespace}/${name}/debug`,
      body,
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  scaleResource: (type: string, namespace: string, name: string, replicas: number, source?: string) =>
    postJSON<{ status: string; fromReplicas: number; toReplicas: number; resource: ResourceItem | null }>(
      `${API_BASE}/resources/${type}/${namespace}/${name}/scale`,
      { replicas },
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Rollback a Deployment to a previous revision (kubectl rollout undo).
  // toRevision = 0 (or omitted) means "previous revision".
  rollbackResource: (type: string, namespace: string, name: string, toRevision?: number, source?: string) =>
    postJSON<{ status: string; fromRevision: number; toRevision: number; resource: ResourceItem | null }>(
      `${API_BASE}/resources/${type}/${namespace}/${name}/rollback`,
      { toRevision: toRevision ?? 0 },
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Set image — strategic merge patch on container images, equivalent
  // to `kubectl set image deploy/X c=img:tag`. The backend captures
  // the from-image state and returns it so the UI can show a
  // before/after diff. `status` is "patched" on a real change or
  // "unchanged" if every requested image already matches the current
  // one (we short-circuit those to avoid spurious "rollout in progress"
  // states).
  setImageResource: (
    type: string,
    namespace: string,
    name: string,
    images: { container: string; image: string }[],
    source?: string,
  ) =>
    postJSON<{
      status: 'patched' | 'unchanged'
      fromImages: { container: string; image: string }[]
      toImages: { container: string; image: string }[]
      resource: ResourceItem | null
    }>(
      `${API_BASE}/resources/${type}/${namespace}/${name}/set-image`,
      { images },
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Set resources — kubectl set resources. Strategic merge patch on
  // each container's resources sub-object. Only the dimensions the
  // operator explicitly sets are touched; absent or empty-string
  // dimensions are skipped server-side. Tier 2 #6 — see
  // internal/k8s-operations/tier2-set-resources.md.
  setResourcesResource: (
    type: string,
    namespace: string,
    name: string,
    containers: ContainerResourcesPatch[],
    source?: string,
  ) =>
    postJSON<{
      status: 'patched'
      fromResources: ContainerResourcePair[]
      toResources: ContainerResourcePair[]
      resource: ResourceItem | null
    }>(
      `${API_BASE}/resources/${type}/${namespace}/${name}/set-resources`,
      { containers },
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Set env — kubectl set env. Strategic merge patch on each
  // container's env array. Per-row action discriminates set vs
  // remove; the backend uses the strategic-merge `$patch: delete`
  // directive to drop targeted entries. Tier 2 #7 — see
  // internal/k8s-operations/tier2-set-env.md.
  setEnvResource: (
    type: string,
    namespace: string,
    name: string,
    body: SetEnvBody,
    source?: string,
  ) =>
    postJSON<{
      status: 'patched'
      fromEnv: ContainerEnvSnapshot[]
      toEnv: ContainerEnvSnapshot[]
      triggerRollout: boolean
      resource: ResourceItem | null
    }>(
      `${API_BASE}/resources/${type}/${namespace}/${name}/set-env`,
      body,
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Patch HPA bounds — strategic merge on spec.minReplicas /
  // spec.maxReplicas. Companion to the set_* family but scoped to
  // autoscaling/v1 HPAs. Backend enforces a maxReplicas <= 1000
  // safety cap. See
  // internal/copilot-execution-capacity/06-insight-rule-coverage.md.
  patchHpaBounds: (
    namespace: string,
    name: string,
    body: { minReplicas?: number; maxReplicas?: number },
    source?: string,
  ) =>
    postJSON<{
      status: 'patched' | 'unchanged'
      fromBounds: { minReplicas: number; maxReplicas: number }
      toBounds: { minReplicas: number; maxReplicas: number }
      resource: ResourceItem | null
    }>(
      `${API_BASE}/resources/hpas/${namespace}/${name}/set-bounds`,
      body,
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Edit metadata — kubectl label / kubectl annotate equivalents.
  // JSON merge patch on metadata.labels + metadata.annotations via
  // the dynamic client; works on any kind. Tier 2 #8 — see
  // internal/k8s-operations/tier2-edit-labels-annotations.md.
  editResourceMetadata: (
    type: string,
    namespace: string,
    name: string,
    body: EditMetadataBody,
    source?: string,
  ) =>
    postJSON<{
      status: 'patched'
      labels: MetadataDiff
      annotations: MetadataDiff
    }>(
      `${API_BASE}/resources/${type}/${namespace}/${name}/edit-metadata`,
      body,
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Reveal a Secret's values. POST (not GET) so the request body —
  // including the operator's reason — never lands in HTTP access
  // logs or browser history; also so caches between client and server
  // can't snapshot the response payload. Tier 2 #9 — see
  // internal/k8s-operations/tier2-secret-reveal.md.
  revealSecret: (
    namespace: string,
    name: string,
    body: { keys?: string[]; reason: string },
    source?: string,
  ) =>
    postJSON<SecretRevealResponse>(
      `${API_BASE}/resources/secrets/${namespace}/${name}/reveal`,
      body,
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Create a new resource from a YAML or JSON manifest. Tier 2 #10
  // — kubectl create -f equivalent. URL is /resources/:type/:ns; the
  // resource NAME comes from metadata.name in the manifest body.
  // For cluster-scoped kinds, namespace is `_`.
  createResource: (
    type: string,
    namespace: string,
    manifest: string,
    source?: string,
  ) => {
    // Send the raw manifest bytes — the backend's sigs.k8s.io/yaml
    // decoder accepts both YAML and JSON, so a single content-type
    // (application/yaml) covers both. We don't go through postJSON
    // because the body isn't JSON-serialized; it's the raw text.
    const headers: Record<string, string> = { 'Content-Type': 'application/yaml' }
    if (source) headers['X-KubeBolt-Action-Source'] = source
    return fetchWithAuth(`${API_BASE}/resources/${type}/${namespace}`, {
      method: 'POST',
      headers,
      body: manifest,
    }).then(async (res) => {
      if (!res.ok) {
        const { message, payload } = await extractErrorPayload(res)
        throw new ApiError(res.status, message, payload)
      }
      return (await res.json()) as CreateResourceResponse
    })
  },

  // Node maintenance — cordon / uncordon. Drain lives separately
  // because it streams SSE rather than returning a single JSON
  // response. Both use the same `_` placeholder for the namespace
  // segment of cluster-scoped resources.
  cordonNode: (name: string, source?: string) =>
    postJSON<{ status: 'cordoned'; alreadyCordoned: boolean; node: ResourceItem | null }>(
      `${API_BASE}/resources/nodes/_/${name}/cordon`,
      {},
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  uncordonNode: (name: string, source?: string) =>
    postJSON<{ status: 'uncordoned'; alreadyUncordoned: boolean; node: ResourceItem | null }>(
      `${API_BASE}/resources/nodes/_/${name}/uncordon`,
      {},
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Rollout pause / resume — kubectl rollout pause / resume.
  // Deployment-only (the backend rejects other types with 400).
  // The path uses `rollout-pause` / `rollout-resume` because the
  // shorter `/resume` slug is already taken by the CronJob handler;
  // the `rollout-` prefix calques `kubectl rollout pause` directly.
  // Response carries the post-patch deployment so the panel can
  // re-render without an extra refetch round-trip, plus the
  // `alreadyPaused` / `alreadyActive` flag for no-op detection.
  pauseRollout: (type: string, namespace: string, name: string, source?: string) =>
    postJSON<{
      status: 'paused'
      alreadyPaused: boolean
      deployment: ResourceItem | null
    }>(
      `${API_BASE}/resources/${type}/${namespace}/${name}/rollout-pause`,
      {},
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  resumeRollout: (type: string, namespace: string, name: string, source?: string) =>
    postJSON<{
      status: 'resumed'
      alreadyActive: boolean
      deployment: ResourceItem | null
    }>(
      `${API_BASE}/resources/${type}/${namespace}/${name}/rollout-resume`,
      {},
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Drain — long-running streaming operation. The POST body
  // configures the drain; the response IS the SSE stream of pod-
  // evicted events terminating in drain-complete. We return the
  // raw Response so the caller can use `response.body.getReader()`
  // to parse events as they arrive — JSON parsing wouldn't fit
  // since the body never closes until the drain finishes.
  drainNode: (
    name: string,
    body: {
      gracePeriodSeconds: number
      timeoutSeconds: number
      deleteEmptyDirData: boolean
      ignoreDaemonsets: boolean
      force: boolean
      disableEviction: boolean
    },
    source?: string,
    signal?: AbortSignal,
  ) =>
    fetchWithAuth(`${API_BASE}/resources/nodes/_/${name}/drain`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(source ? { 'X-KubeBolt-Action-Source': source } : {}),
      },
      body: JSON.stringify(body),
      signal,
    }),

  // Re-attach to an in-flight drain. Returns 404 if no session is
  // active for this node, otherwise the same SSE stream the POST
  // would have produced (with replay of past events first). Used
  // when the operator closes the modal mid-drain and reopens it.
  attachDrainSession: (name: string, signal?: AbortSignal) =>
    fetchWithAuth(`${API_BASE}/resources/nodes/_/${name}/drain`, {
      method: 'GET',
      signal,
    }),

  // Cancel an in-flight drain. Pods already submitted for eviction
  // continue terminating per their grace period; new evictions
  // stop. The backend's session emits drain-complete with
  // status=cancelled.
  cancelDrain: (name: string) =>
    deleteRequest<{ status: string; node: string }>(
      `${API_BASE}/resources/nodes/_/${name}/drain`,
    ),

  // CronJob ergonomics — suspend / resume / trigger.
  // Suspend & resume mirror cordon/uncordon: the response includes
  // an `alreadySuspended`/`alreadyActive` flag so the UI can render
  // "no change" rather than a fake success toast on a no-op.
  suspendCronJob: (namespace: string, name: string, source?: string) =>
    postJSON<{ status: 'suspended'; alreadySuspended: boolean; cronJob: ResourceItem | null }>(
      `${API_BASE}/resources/cronjobs/${namespace}/${name}/suspend`,
      {},
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  resumeCronJob: (namespace: string, name: string, source?: string) =>
    postJSON<{ status: 'resumed'; alreadyActive: boolean; cronJob: ResourceItem | null }>(
      `${API_BASE}/resources/cronjobs/${namespace}/${name}/resume`,
      {},
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

  // Trigger creates a one-off Job from the CronJob's jobTemplate.
  // Body fields are all optional — without them the backend
  // auto-generates a Job name and won't auto-suspend.
  triggerCronJob: (
    namespace: string,
    name: string,
    body?: { jobName?: string; suspendAfterTrigger?: boolean },
    source?: string,
  ) =>
    postJSON<{
      status: 'triggered'
      // Full Job map (same shape as GET /resources/jobs/<ns>/<name>),
      // built directly from the freshly-created object on the
      // backend rather than via the informer cache. Lets the modal
      // pre-populate the destination page's detail-cache so
      // "Open job" doesn't 404 while the informer catches up.
      job: ResourceItem
      fromCronJob: string
      suspended?: boolean
      suspendError?: string
    }>(
      `${API_BASE}/resources/cronjobs/${namespace}/${name}/trigger`,
      body ?? {},
      source ? { 'X-KubeBolt-Action-Source': source } : undefined,
    ),

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

  // Historical metrics (VictoriaMetrics PromQL pass-through, Phase 2)
  queryMetricsRange: (params: { query: string; start: number; end: number; step: string }) =>
    fetchJSON<PromRangeResponse>(
      `${API_BASE}/metrics/query_range${buildQuery({
        query: params.query,
        start: params.start,
        end: params.end,
        step: params.step,
      })}`
    ),

  // Instant PromQL query — single-point lookup. Used by panels that need
  // "current value" or topN snapshots, where running a range query and
  // picking the last point would be wasteful.
  queryMetrics: (params: { query: string; time?: number }) =>
    fetchJSON<PromVectorResponse>(
      `${API_BASE}/metrics/query${buildQuery({
        query: params.query,
        time: params.time,
      })}`
    ),

  // Cluster-wide rollout events. The Capacity dashboard uses this
  // to overlay deploy markers on the trends charts so metric shifts
  // can be correlated with "what changed". Window matches the chart
  // range — fetching 15m of deploys for a 15m chart, 7d for a 7d
  // chart — so the response stays small.
  getDeploys: (params: { windowMinutes: number }) =>
    fetchJSON<DeployEvent[]>(
      `${API_BASE}/deploys${buildQuery({ windowMinutes: params.windowMinutes })}`,
    ),

  // Flow edges (Phase 2.1, from pod_flow_events_total)
  getFlowEdges: (params?: { namespace?: string; windowMinutes?: number }) =>
    fetchJSON<FlowEdgesResponse>(
      `${API_BASE}/flows/edges${buildQuery({
        namespace: params?.namespace,
        window: params?.windowMinutes,
      })}`
    ),

  // Scrape coverage (Phase 2 Day 5) — which observability sources
  // are actively shipping samples to VM for the active cluster.
  getCoverage: () => fetchJSON<CoverageResponse>(`${API_BASE}/coverage`),

  // --- Integrations ---
  listIntegrations: () => fetchJSON<Integration[]>(`${API_BASE}/integrations`),

  getIntegration: (id: string) =>
    fetchJSON<Integration>(`${API_BASE}/integrations/${encodeURIComponent(id)}`),

  installIntegration: <T = unknown>(id: string, config: T) =>
    postJSON<Integration>(`${API_BASE}/integrations/${encodeURIComponent(id)}/install`, config),

  uninstallIntegration: (id: string, opts?: { force?: boolean }) =>
    deleteRequest<Integration>(
      `${API_BASE}/integrations/${encodeURIComponent(id)}${opts?.force ? '?force=true' : ''}`
    ),

  // Live config read of a managed integration. UI calls this to
  // pre-populate the configure form with what's actually running
  // — not what the user typed at install time.
  getIntegrationConfig: <T = unknown>(id: string) =>
    fetchJSON<T>(`${API_BASE}/integrations/${encodeURIComponent(id)}/config`),

  // Same as getIntegrationConfig but returns the response headers
  // alongside the body — needed to surface the
  // `X-Self-Targeted-Proxy` warning that the configure dialog uses
  // to render its banner. Separate function so the common path
  // stays narrow.
  getIntegrationConfigWithHeaders: async <T = unknown>(id: string): Promise<{ config: T; selfTargetedProxyClusterId?: string }> => {
    const res = await fetchWithAuth(`${API_BASE}/integrations/${encodeURIComponent(id)}/config`)
    if (!res.ok) {
      const { message, payload } = await extractErrorPayload(res)
      throw new ApiError(res.status, message, payload)
    }
    const config = (await res.json()) as T
    return {
      config,
      selfTargetedProxyClusterId: res.headers.get('X-Self-Targeted-Proxy') || undefined,
    }
  },

  configureIntegration: <T = unknown>(id: string, config: T) =>
    putJSON<Integration>(`${API_BASE}/integrations/${encodeURIComponent(id)}/config`, config),

  // Agent-specific helpers. The dialog calls these to (1) gate Save
  // when proxy is enabled but auth would mismatch the backend's
  // enforced mode, and (2) generate an ingest token + materialize a
  // K8s Secret in one click — eliminating the manual `kubectl create
  // secret` step that was the dominant install friction point.
  getAgentAuthInfo: () =>
    fetchJSON<AgentAuthInfo>(`${API_BASE}/integrations/agent/auth-info`),

  // Topology-aware defaults for the agent install / add-cluster wizards.
  // When KubeBolt is running in-cluster, surfaces the internal Service
  // DNS for same-cluster installs and the externally-reachable endpoint
  // (LoadBalancer IP / NodePort) for remote-cluster registration. Empty
  // externalEndpoint signals the caller must expose agent-ingest first.
  getAgentInstallDefaults: () =>
    fetchJSON<AgentInstallDefaults>(`${API_BASE}/integrations/agent/install-defaults`),

  // Issues a token AND materializes a K8s Secret in one round-trip.
  // Distinct from the existing `issueAgentToken` (which only issues
  // and returns plaintext for the operator to copy/paste) — this
  // wires the result straight into the cluster, leaving nothing
  // for the operator to manage manually.
  issueAgentTokenAndMaterializeSecret: (body: AgentIssueTokenRequest) =>
    postJSON<AgentIssueTokenResponse>(`${API_BASE}/integrations/agent/issue-token`, body),

  // ─── Admin → Settings (spec #09) ──────────────────────────────────
  //
  // Runtime configuration of things that used to be env-only. Every
  // domain has GET (masked view + env baseline for "what would I get if
  // I cleared this"), PUT (partial patch with secret encryption
  // happening server-side), and a reset endpoint that drops the
  // BoltDB row entirely.

  // Copilot config. GET returns the masked Copilot settings shape; PUT
  // accepts a partial patch with secrets in dedicated top-level fields
  // (kept out of the patch struct so payload logs never accidentally
  // capture a real key). Reset clears the override and falls back to
  // env-driven defaults.
  getSettingsCopilot: () =>
    fetchJSON<CopilotSettingsResponse>(`${API_BASE}/admin/settings/copilot`),

  putSettingsCopilot: (body: CopilotSettingsPutRequest) =>
    putJSON<CopilotSettingsResponse>(`${API_BASE}/admin/settings/copilot`, body),

  resetSettingsCopilot: () =>
    postJSON<{ status: string }>(`${API_BASE}/admin/settings/copilot/reset`, {}),

  // --- Settings → Notifications (spec #09) ---
  //
  // Mirrors the Copilot pattern: GET returns masked effective + stored
  // view, PUT accepts a partial patch with secrets in top-level fields,
  // RESET wipes the BoltDB override entirely. PUT hot-reloads the
  // live notifications manager — channel additions/removals take
  // effect on the next insight without a process restart.
  getSettingsNotifications: () =>
    fetchJSON<NotificationsSettingsResponse>(`${API_BASE}/admin/settings/notifications`),

  putSettingsNotifications: (body: NotificationsSettingsPutRequest) =>
    putJSON<NotificationsSettingsResponse>(`${API_BASE}/admin/settings/notifications`, body),

  resetSettingsNotifications: () =>
    postJSON<{ status: string }>(`${API_BASE}/admin/settings/notifications/reset`, {}),

  // --- Settings → Auth (spec #09) ---
  //
  // Special domain: no hot-reload. PUT persists; pendingRestart in the
  // response tells the UI to show a "Restart now" banner.
  getSettingsAuth: () =>
    fetchJSON<AuthSettingsResponse>(`${API_BASE}/admin/settings/auth`),

  putSettingsAuth: (body: AuthSettingsPutRequest) =>
    putJSON<AuthSettingsResponse>(`${API_BASE}/admin/settings/auth`, body),

  resetSettingsAuth: () =>
    postJSON<{ status: string }>(`${API_BASE}/admin/settings/auth/reset`, {}),

  // --- Settings → Ingest channel (spec #09 V2) ---
  getSettingsIngestChannel: () =>
    fetchJSON<IngestChannelSettingsResponse>(`${API_BASE}/admin/settings/ingest-channel`),

  putSettingsIngestChannel: (body: IngestChannelSettingsPutRequest) =>
    putJSON<IngestChannelSettingsResponse>(`${API_BASE}/admin/settings/ingest-channel`, body),

  resetSettingsIngestChannel: () =>
    postJSON<{ status: string }>(`${API_BASE}/admin/settings/ingest-channel/reset`, {}),

  // System actions. Restart triggers os.Exit(0) on the backend after a
  // ~1s grace period so Kubernetes (restartPolicy:Always) brings up a
  // fresh container with the persisted Auth values applied.
  systemRestart: () =>
    postJSON<{ status: string; message: string }>(`${API_BASE}/admin/system/restart`, {}),

  // --- Settings → General (spec #09) ---
  getSettingsGeneral: () =>
    fetchJSON<GeneralSettingsResponse>(`${API_BASE}/admin/settings/general`),

  putSettingsGeneral: (body: GeneralSettingsPutRequest) =>
    putJSON<GeneralSettingsResponse>(`${API_BASE}/admin/settings/general`, body),

  resetSettingsGeneral: () =>
    postJSON<{ status: string }>(`${API_BASE}/admin/settings/general/reset`, {}),

  // Public UI config — fetched once at app boot so the topbar shows the
  // operator-set display name and RefreshContext picks the right default
  // before any authenticated query fires.
  getUIConfig: () =>
    fetchJSON<UIConfigResponse>(`${API_BASE}/config/ui`),

  // /admin/settings/booted-with — read-only snapshot of KUBEBOLT_* env
  // vars at process start. Operators use it to debug "is my Helm value
  // making it into the container?" without kubectl-exec.
  getBootedWith: () =>
    fetchJSON<BootedWithResponse>(`${API_BASE}/admin/settings/booted-with`),

  // First-login wizard status. The whole wizard is just a guided pass
  // over existing per-domain PUT endpoints (auth/me/password,
  // settings/copilot, settings/notifications) plus this completion
  // flag the UI reads to decide whether to show the welcome overlay.
  getSetupStatus: () =>
    fetchJSON<{ complete: boolean }>(`${API_BASE}/admin/setup/status`),
  completeSetup: () =>
    postJSON<void>(`${API_BASE}/admin/setup/complete`, {}),
  resetSetup: () =>
    postJSON<void>(`${API_BASE}/admin/setup/complete?reset=true`, {}),
}

// ─── Admin → Settings types ──────────────────────────────────────────
//
// Mirror the Go shapes in apps/api/internal/settings/copilot.go. The
// frontend reads `effective` for "what's in effect right now" and
// `stored` for per-field "configured here vs inherits from env" badges.
// Secrets never round-trip — the API returns only masked previews.

export interface CopilotSettingsResponse {
  effective: {
    enabled: boolean
    provider: string
    model: string
    apiKeyMasked: string
    baseURL?: string
    hasFallback: boolean
    fallbackProvider?: string
    fallbackModel?: string
    fallbackApiKeyMasked?: string
    fallbackBaseURL?: string
    maxTokens: number
    autoCompact: boolean
    showToolCalls: boolean
    // Auto-compact tunables surfaced by the backend so the UI shows
    // "what's in effect right now" without a second round-trip. The
    // server emits these with `omitempty` semantics, so they may be
    // absent on a fresh install with no overrides and a model whose
    // defaults haven't materialised yet.
    sessionBudgetTokens?: number
    autoCompactThreshold?: number
    compactModel?: string
    compactPreserveTurns?: number
  }
  stored: {
    hasPrimaryOverride: boolean
    hasFallbackOverride: boolean
    primary?: {
      provider?: string
      apiKeyMasked?: string
      apiKeyConfigured: boolean
      model?: string
      baseURL?: string
    }
    fallback?: {
      provider?: string
      apiKeyMasked?: string
      apiKeyConfigured: boolean
      model?: string
      baseURL?: string
    }
    otherFields?: {
      maxTokens?: number
      autoCompact?: boolean
      sessionBudgetTokens?: number
      autoCompactThreshold?: number
      compactModel?: string
      compactPreserveTurns?: number
      showToolCalls?: boolean
    }
  }
  secretsReadable: boolean
}

// CopilotSettingsPutRequest mirrors the backend putCopilotRequest. All
// fields are optional. `patch.*` carries non-secret config; the
// `plaintextAPIKey` fields sit at the top level so the on-wire shape
// keeps secrets out of the nested patch object that error responses
// and logs may echo.
export interface CopilotSettingsPutRequest {
  patch?: {
    primary?: {
      provider?: string
      model?: string
      baseURL?: string
    }
    fallback?: {
      provider?: string
      model?: string
      baseURL?: string
    }
    maxTokens?: number
    autoCompact?: boolean
    sessionBudgetTokens?: number
    autoCompactThreshold?: number
    compactModel?: string
    compactPreserveTurns?: number
    showToolCalls?: boolean
  }
  plaintextAPIKey?: string
  plaintextFallbackAPIKey?: string
}

// ─── Admin → Settings → Notifications types ───────────────────────────
//
// Mirror the Go shapes in apps/api/internal/settings/notifications.go.
// Same layering convention as Copilot: `effective` = what's live now,
// `stored` = per-field "which fields are coming from BoltDB" markers
// for the source badge. Webhook URLs and SMTP password never round-
// trip plaintext; only masked previews on the way out, plaintext on
// the way in via dedicated top-level fields.

export interface NotificationsSettingsResponse {
  effective: {
    masterEnabled: boolean
    minSeverity: string // 'critical' | 'warning' | 'info'
    cooldownSeconds: number
    baseURL?: string
    includeResolved: boolean

    // Tri-state per channel:
    //   configured = required fields are filled
    //   enabled    = operator's toggle is on
    //   active     = configured && enabled (= what BuildNotifiers gates on)
    slackConfigured: boolean
    slackEnabled: boolean
    slackActive: boolean
    slackWebhookMasked?: string

    discordConfigured: boolean
    discordEnabled: boolean
    discordActive: boolean
    discordWebhookMasked?: string

    emailConfigured: boolean
    emailEnabled: boolean
    emailActive: boolean
    emailHost?: string
    emailPort?: number
    emailUsername?: string
    emailPasswordMasked?: string
    emailFrom?: string
    emailTo?: string[]
    emailDigestMode?: string
  }
  stored: {
    hasGlobalOverride: boolean
    hasSlackOverride: boolean
    hasDiscordOverride: boolean
    hasEmailOverride: boolean
    global?: {
      masterEnabled?: boolean
      minSeverity?: string
      cooldownSeconds?: number
      baseURL?: string
      includeResolved?: boolean
    }
    slack?: {
      webhookConfigured: boolean
      webhookMasked?: string
    }
    discord?: {
      webhookConfigured: boolean
      webhookMasked?: string
    }
    email?: {
      host?: string
      port?: number
      username?: string
      passwordConfigured: boolean
      passwordMasked?: string
      from?: string
      to?: string[]
      digestMode?: string
    }
  }
  secretsReadable: boolean
}

export interface NotificationsSettingsPutRequest {
  patch?: {
    global?: {
      masterEnabled?: boolean
      minSeverity?: string
      cooldownSeconds?: number
      baseURL?: string
      includeResolved?: boolean
    }
    slack?: {
      enabled?: boolean
    }
    discord?: {
      enabled?: boolean
    }
    email?: {
      enabled?: boolean
      host?: string
      port?: number
      username?: string
      from?: string
      to?: string[]
      digestMode?: string
    }
  }
  plaintextSlackWebhookURL?: string
  plaintextDiscordWebhookURL?: string
  plaintextSMTPPassword?: string
}

// ─── Admin → Settings → Auth types ────────────────────────────────────
//
// Mirrors apps/api/internal/settings/auth.go. UI exposes only the
// safe-to-change subset (TTLs + read-only enabled state); JWT secret /
// data dir / admin password stay out of UI editing because they're
// either security-critical (key rotation blows up every encrypted blob)
// or filesystem-bound (data dir).

export interface AuthSettingsEffective {
  enabled: boolean
  accessTokenExpirySeconds: number
  refreshTokenExpirySeconds: number
}

export interface AuthSettingsResponse {
  effective: AuthSettingsEffective
  bootSnapshot: AuthSettingsEffective
  stored: {
    hasOverride: boolean
    enabled?: boolean
    accessTokenExpirySeconds?: number
    refreshTokenExpirySeconds?: number
  }
  pendingRestart: boolean
  jwtSecretFromEnv: boolean
  jwtSecretMasked?: string
}

export interface AuthSettingsPutRequest {
  patch?: {
    enabled?: boolean
    accessTokenExpirySeconds?: number
    refreshTokenExpirySeconds?: number
  }
}

// ─── Admin → Settings → General types ─────────────────────────────────

export interface GeneralSettingsResponse {
  effective: {
    displayName: string
    defaultRefreshIntervalSeconds: number
    prodNamespacePattern: string
  }
  stored: {
    hasOverride: boolean
    displayName?: string
    defaultRefreshIntervalSeconds?: number
    prodNamespacePattern?: string
  }
}

export interface GeneralSettingsPutRequest {
  patch?: {
    displayName?: string
    defaultRefreshIntervalSeconds?: number
    prodNamespacePattern?: string
  }
}

// ─── Admin → Settings → Ingest channel types (spec #09 V2) ────────────
//
// Mirrors apps/api/internal/settings/ingest_channel.go. Three of the
// fifteen fields require a restart to apply (auth mode + audience +
// mTLS); the rest hot-reload. pendingRestart only flips on the
// restart-required subset diffing against bootSnapshot.

export interface IngestChannelEffective {
  // Channel security (restart-required).
  agentAuthMode: string
  agentTokenAudience: string
  agentRequireMTLS: boolean
  // Rate limiting.
  agentRateLimitEnabled: boolean
  agentRateLimitRPS: number
  agentRateLimitBurst: number
  // Cluster auto-registration.
  agentAutoRegisterClusters: boolean
  agentRegistryPruneHorizonSecs: number
  // Prom remote_write.
  remoteWriteEnabled: boolean
  remoteWriteAuthMode: string
  promWriteDefaultSamplesPerSec: number
  promWriteDefaultBurstSamples: number
  promWriteDefaultMaxActiveSeries: number
  promWriteDefaultMaxActiveSeriesGlobal: number
  // Tunnels.
  agentTunnelIdleTimeoutSecs: number
}

export interface IngestChannelStored {
  hasOverride: boolean
  agentAuthMode?: string
  agentTokenAudience?: string
  agentRequireMTLS?: boolean
  agentRateLimitEnabled?: boolean
  agentRateLimitRPS?: number
  agentRateLimitBurst?: number
  agentAutoRegisterClusters?: boolean
  agentRegistryPruneHorizonSecs?: number
  remoteWriteEnabled?: boolean
  remoteWriteAuthMode?: string
  promWriteDefaultSamplesPerSec?: number
  promWriteDefaultBurstSamples?: number
  promWriteDefaultMaxActiveSeries?: number
  promWriteDefaultMaxActiveSeriesGlobal?: number
  agentTunnelIdleTimeoutSecs?: number
}

export interface IngestChannelSettingsResponse {
  effective: IngestChannelEffective
  bootSnapshot: IngestChannelEffective
  stored: IngestChannelStored
  pendingRestart: boolean
}

export interface IngestChannelSettingsPutRequest {
  patch?: Partial<Omit<IngestChannelStored, 'hasOverride'>>
}

// Public UI config — readable by every authenticated user (and by
// unauthenticated requests when auth is disabled). Frontend boots up
// with this before issuing any other query.
export interface UIConfigResponse {
  displayName: string
  defaultRefreshIntervalSeconds: number
}

// /admin/settings/booted-with shape — env var snapshot from process
// start. `sensitive=true` means the value is the placeholder string
// "(set)" rather than the cleartext; the UI renders it differently
// to make it obvious which entries are redacted.
export interface BootedWithEntry {
  name: string
  value: string
  sensitive: boolean
}

export interface BootedWithResponse {
  env: BootedWithEntry[]
  count: number
}

// Backend agent auth posture. The UI uses `enforcement` to decide
// whether the dialog can save with authMode="" — when enforced, the
// Save button is disabled with a tooltip until the operator picks
// ingest-token (or tokenreview, if the backend is in-cluster).
// `tenants` populates the dropdown for the Generate Token flow.
// Rollout-history payload returned by ?detailed=true. Same shape
// across Deployment / StatefulSet / DaemonSet so the timeline UI
// is one component.
export interface RevisionImage {
  container: string
  image: string
}

export interface DetailedRevision {
  revision: number
  name: string
  createdAt: string
  age: string
  images: RevisionImage[]
  changeCause: string
  replicaCount: number
  active: boolean
}

export interface RolloutHistory {
  currentRevision: number
  revisions: DetailedRevision[]
}

// Set resources types — Tier 2 #6. The patch shape mirrors the API
// design in internal/k8s-operations/tier2-set-resources.md: every
// dimension is independently optional, so the operator can bump
// only memory limit without touching cpu request, etc.
//
// Empty strings are treated as "leave alone" in v1 (same as field
// absent). Removing a dimension is deferred to v2 — operators have
// the YAML editor for that path.
export interface ResourceQuantityInput {
  cpu?: string
  memory?: string
}

export interface ContainerResourcesPatch {
  container: string
  initContainer?: boolean
  requests?: ResourceQuantityInput
  limits?: ResourceQuantityInput
}

// ContainerResourcePair is the response-side from/to envelope. Both
// requests and limits arrive as a flat map[string]string — the
// backend pre-flattens them so the UI doesn't need to handle
// nullable nested shapes.
export interface ContainerResourcePair {
  container: string
  initContainer?: boolean
  requests?: Record<string, string>
  limits?: Record<string, string>
}

// Set env types — Tier 2 #7. Mirrors k8s.io/api/core/v1.EnvVarSource
// for the valueFrom variants (configMap / secret / field /
// resourceField); for v1 the UI primarily exercises configMap and
// secret refs.
export interface ConfigMapKeyRef {
  name: string
  key: string
  optional?: boolean
}

export interface SecretKeyRef {
  name: string
  key: string
  optional?: boolean
}

export interface ObjectFieldRef {
  fieldPath: string
}

export interface EnvVarSourcePatch {
  configMapKeyRef?: ConfigMapKeyRef
  secretKeyRef?: SecretKeyRef
  fieldRef?: ObjectFieldRef
}

export interface EnvVarPatch {
  name: string
  action: 'set' | 'remove'
  value?: string
  valueFrom?: EnvVarSourcePatch
}

export interface ContainerEnvPatch {
  container: string
  initContainer?: boolean
  env: EnvVarPatch[]
}

export interface SetEnvBody {
  containers: ContainerEnvPatch[]
  triggerRollout?: boolean
}

// Response-side: each entry's resolved kind + value or valueFrom so
// the UI can render the from/to diff without inspecting nested
// variants.
export type EnvEntryKind = 'literal' | 'configMap' | 'secret' | 'field' | 'resourceField' | 'removed'

export interface EnvEntryPair {
  name: string
  kind: EnvEntryKind
  value?: string
  valueFrom?: EnvVarSourcePatch
}

export interface ContainerEnvSnapshot {
  container: string
  initContainer?: boolean
  env: EnvEntryPair[]
}

// Edit metadata types — Tier 2 #8. Both labels and annotations use
// the same Add/Remove envelope; the backend issues a JSON merge
// patch where Remove keys appear as null values (RFC 7396 = delete).
export interface MetadataMapEdit {
  add?: Record<string, string>
  remove?: string[]
}

export interface EditMetadataBody {
  labels?: MetadataMapEdit
  annotations?: MetadataMapEdit
}

// Response-side per-map diff — the operator sees added (highlight),
// updated (from→to), and removed (strike) keys without having to
// recompute against the live state.
export interface MetadataDiff {
  from: Record<string, string>
  to: Record<string, string>
  added?: string[]
  updated?: string[]
  removed?: string[]
}

// Secret reveal types — Tier 2 #9. The backend classifies each
// revealed value as either text (UTF-8 printable) or binary (anything
// else). Binary entries deliberately omit the value field — the UI
// renders a sha256 + length descriptor with a download affordance
// instead of trying to print bytes that would crash the renderer or
// produce unhelpful gibberish.
export type SecretRevealedValueKind = 'text' | 'binary'

export interface SecretRevealedValue {
  key: string
  kind: SecretRevealedValueKind
  value?: string
  sha256?: string
  bytes?: number
}

export interface SecretRevealResponse {
  name: string
  namespace: string
  type: string
  revealedAt: string
  values: SecretRevealedValue[]
  missing: string[]
}

// Apply new manifest types — Tier 2 #10. Response is the bare
// identifying fields the UI uses to navigate to the new resource
// (kind, name, namespace, uid). The backend strips status and
// managedFields before responding so the payload stays minimal.
export interface CreateResourceResponse {
  status: 'created'
  name: string
  namespace: string
  kind: string
  apiVersion: string
  uid: string
  // Post-create detail snapshot the backend produces by polling the
  // informer cache for up to ~500ms after the apiserver Create()
  // returns. Used by NewResourceModal to seed the detail query cache
  // before navigating, eliminating the "Resource not found" flash
  // that used to appear while the cache caught up. May be null when
  // the cache never observed the create inside the retry budget; the
  // caller falls back to the regular detail fetch with retry.
  resource: ResourceItem | null
}

export type AgentAuthEnforcement = 'enforced' | 'permissive' | 'disabled'

export interface AgentAuthInfo {
  enforcement: AgentAuthEnforcement
  tenants: AgentTenantBrief[]
}

export interface AgentTenantBrief {
  id: string
  name: string
  disabled: boolean
}

export interface AgentIssueTokenRequest {
  tenantId: string
  label?: string
  namespace?: string
  secretName?: string
  ttlSeconds?: number
}

// Note: the backend deliberately omits the plaintext token — it
// lives only in the cluster Secret. The dialog uses `secretName` to
// pre-fill AgentInstallConfig.authTokenSecret.
export interface AgentIssueTokenResponse {
  secretName: string
  namespace: string
  tokenPrefix: string
  tokenLabel: string
  tenantId: string
}

// Backend topology hints for the agent install / add-cluster wizards.
// `deploymentMode` is "in-cluster" when KubeBolt is running with a SA
// token (Helm install) and "external" when it's the desktop binary or
// docker-compose. The two backendUrl variants distinguish "install
// agent in this same cluster" (internal DNS) from "register a remote
// cluster" (must use externalEndpoint, empty when agent-ingest is only
// ClusterIP-reachable).
export interface AgentInstallDefaults {
  deploymentMode: 'in-cluster' | 'external'
  selfNamespace?: string
  internalBackendUrl?: string
  externalEndpoint?: string
  agentNamespace: string
  agentIngestService?: AgentIngestServiceInfo
}

export interface AgentIngestServiceInfo {
  namespace: string
  name: string
  type: string // ClusterIP | LoadBalancer | NodePort
  port: number
  nodePort?: number
  externalIp?: string
  hostname?: string
}

// ─── Integration types ───
// Kept in sync with the Go types in apps/api/internal/integrations.

export type IntegrationStatus =
  | 'not_installed'
  | 'installed'
  | 'degraded'
  | 'unknown'

export interface IntegrationHealth {
  podsReady: number
  podsDesired: number
  message?: string
}

export interface IntegrationFeatureFlag {
  key: string
  label: string
  description?: string
  enabled: boolean
  // Optional non-boolean state (e.g. the agent's "Permission tier"
  // surfaces "Cluster-wide read" / "Operator" / "Metrics only"
  // here). When present, the panel renders it instead of the
  // on/off pill.
  value?: string
  requires?: string[]
}

export interface Integration {
  id: string
  name: string
  description: string
  docsUrl?: string
  capabilities?: string[]
  status: IntegrationStatus
  version?: string
  namespace?: string
  features?: IntegrationFeatureFlag[]
  health?: IntegrationHealth
  // True when the installed workload carries the
  // managed-by=kubebolt label — i.e. KubeBolt installed it.
  // False for workloads installed via Helm, kubectl apply, or any
  // other external path. Meaningful only when status is
  // 'installed' or 'degraded'.
  managed: boolean
  // True when the underlying provider implements Install/Uninstall
  // through the backend. False for ingest-based integrations
  // (e.g. Prometheus remote_write) that have no in-cluster
  // workload KubeBolt could create or destroy. Drives whether the
  // Manage panel renders the Danger zone + uninstall affordance.
  installable: boolean
}

// Per-integration install configs. Each one matches the provider's
// own decoding shape on the backend. For the agent, keeping this
// here means the install wizard gets type-checked end to end.
export interface AgentInstallConfig {
  namespace?: string
  backendUrl: string
  clusterName?: string
  hubbleEnabled?: boolean

  // RBACMode picks the agent SA's permission tier. Maps 1:1 to
  // helm chart values.rbac.mode and to the OSS manifests
  // deploy/agent/kubebolt-agent-{metrics,reader,operator}.yaml.
  //
  //   metrics  — narrow (kubelet stats + pods + namespaces). Proxy
  //              stays OFF; only metrics + Hubble flows ship.
  //   reader   — cluster-wide get/list/watch on `*/*`. Proxy ON
  //              (mandatory). Mutations come back 403.
  //   operator — wildcard read+write on `*/*`. Proxy ON (mandatory).
  //              Auth REQUIRED (cluster-admin scoped to SA token).
  //
  // Default in the wizard is "reader" — the typical install for the
  // SaaS-style topology where the backend reaches the cluster via
  // the agent's outbound channel.
  rbacMode?: 'metrics' | 'reader' | 'operator'

  // K8s API proxy explicit override. The backend auto-derives this
  // from rbacMode (off for metrics, on for reader/operator), so
  // most callers leave it unset. Setting it to false while
  // rbacMode is reader/operator is rejected — those modes only make
  // sense with the proxy on.
  proxyEnabled?: boolean

  // Deprecated: superseded by rbacMode. Kept for wire-compat with
  // older clients; backend folds it into rbacMode=operator when
  // rbacMode is empty.
  proxyOperatorRbac?: boolean

  // Auth wiring against the backend's gRPC channel. Empty → no
  // auth headers (only valid when backend runs auth-disabled).
  // "ingest-token" → backend admin issues a long-lived token from
  // the Agent Tokens page; user creates a Secret in the agent's
  // namespace; this field names that Secret. "tokenreview" not
  // wizard-supported yet (in-cluster scenarios use Helm directly).
  authMode?: '' | 'ingest-token' | 'tokenreview'
  authTokenSecret?: string

  imageRepo?: string
  imageTag?: string
  imagePullPolicy?: 'Always' | 'IfNotPresent' | 'Never'

  // Override the default Hubble relay target.
  hubbleRelayAddress?: string
  // TLS / mTLS material. The Secret must already exist in the
  // target namespace with keys ca.crt (+ optional tls.crt/tls.key
  // for mTLS). Install fails fast when the Secret is missing.
  hubbleRelayTls?: {
    existingSecret: string
    serverName?: string
  }

  // Scheduling. Keys must be strings; empty keys are skipped by
  // the wizard before submit.
  nodeSelector?: Record<string, string>
  priorityClassName?: string

  // K8s quantity strings (e.g. "100m", "128Mi").
  resources?: {
    cpuRequest?: string
    cpuLimit?: string
    memoryRequest?: string
    memoryLimit?: string
  }

  logLevel?: string
}

export interface FlowEdge {
  srcNamespace: string
  srcPod: string
  dstNamespace: string
  dstPod: string
  // For pod-to-external flows, dstIp carries the peer address and
  // dstFqdn (when DNS visibility is enabled) carries the observed
  // hostname. dstPod / dstNamespace are empty in that case.
  dstIp?: string
  dstFqdn?: string
  verdict: string
  ratePerSec: number
  l7?: L7Summary
}

// Present on forwarded edges when Cilium's proxy emitted HTTP L7 events
// for the destination pod. Absent on drops and on pairs without L7
// visibility enabled.
export interface L7Summary {
  requestsPerSec: number
  statusClass: Partial<Record<'info' | 'ok' | 'redir' | 'client_err' | 'server_err' | 'unknown', number>>
  avgLatencyMs?: number
}

export interface FlowEdgesResponse {
  edges: FlowEdge[]
  windowMinutes: number
  source: string
}

// --- Scrape coverage (Phase 2 Day 5) ---
// Which observability sources are actively shipping samples to VM
// for the active cluster. Drives the dashboard banner that tells
// the operator "you have agent + hubble; node-exporter is missing".
export interface CoverageSource {
  name: string         // "kubebolt-agent" | "node-exporter" | "kube-state-metrics" | "hubble"
  probe: string        // PromQL the backend ran for this source
  status: 'active' | 'inactive'
}

export interface CoverageResponse {
  sources: CoverageSource[]
  lookbackMinutes: number
}

// --- Agent ingest tokens ---
//
// The backend redacts plaintext + hashes from list/get responses.
// Plaintext appears ONLY in IssuedToken.token, returned by issue and
// rotate. The UI must surface it once and never persist it client-side.
export interface Tenant {
  id: string
  name: string
  plan: string
  disabled: boolean
  createdAt: string
  updatedAt: string
  tokenCount: number
  activeTokenCount: number
}

export interface IngestToken {
  id: string
  prefix: string // first 8 chars after "kb_" — safe to display
  label: string
  createdAt: string
  createdBy: string
  lastUsedAt?: string
  expiresAt?: string
  revokedAt?: string
  active: boolean
}

export interface TenantWithTokens extends Tenant {
  ingestTokens: IngestToken[]
}

export interface IssuedToken {
  token: string // plaintext — shown once
  info: IngestToken
}

// --- Per-tenant Prom remote_write limits ---
//
// Mirrors apps/api/internal/auth/tenant_limits.go. Each TenantLimits
// field is optional — a missing value means "inherit the system
// default". The Effective view collapses overrides + defaults so the
// enforcement layers (rate limiter, cardinality tracker) consume
// concrete numbers; the UI compares Custom against Effective to
// decide which fields render the "Default" vs "Custom" badge.
export interface TenantLimits {
  writeSamplesPerSec?: number
  writeBurstSamples?: number
  maxActiveSeries?: number
}

export interface EffectiveLimits {
  writeSamplesPerSec: number
  writeBurstSamples: number
  maxActiveSeries: number
}

export interface LimitsResponse {
  effective: EffectiveLimits
  custom?: TenantLimits
  defaults: EffectiveLimits
}

// Prometheus-compatible range query response (from VictoriaMetrics).
export interface PromRangeResponse {
  status: 'success' | 'error'
  data?: {
    resultType: 'matrix' | 'vector' | 'scalar' | 'string'
    result: Array<{
      metric: Record<string, string>
      values: Array<[number, string]> // [unix_seconds, value_as_string]
    }>
  }
  error?: string
  errorType?: string
}

// Cluster-wide rollout event. Mirrors apps/api/internal/models/types.go
// DeployEvent. `deployedAt` is RFC3339 (Go's default time.Time JSON
// encoding); the client converts to unix ms / s as needed at the
// chart layer.
export interface DeployEvent {
  namespace: string
  kind: string // "Deployment" today; "StatefulSet" / "DaemonSet" once
  // a ControllerRevision lister lands on the connector
  name: string
  deployedAt: string
  image?: string
}

// Instant query response — `value` is singular for vector results.
export interface PromVectorResponse {
  status: 'success' | 'error'
  data?: {
    resultType: 'matrix' | 'vector' | 'scalar' | 'string'
    result: Array<{
      metric: Record<string, string>
      value: [number, string] // [unix_seconds, value_as_string]
    }>
  }
  error?: string
  errorType?: string
}
