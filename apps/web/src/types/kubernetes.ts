// Resource counts
export interface ResourceCount {
  total: number
  ready: number
  notReady: number
  warning: number
}

// Resource usage metrics
export interface ResourceUsage {
  used: number
  requested: number
  limit: number
  allocatable: number
  percentUsed: number
  percentRequested: number
}

// Metric data point
export interface MetricPoint {
  timestamp: string
  value: number
}

// Insight from the engine
export interface Insight {
  id: string
  // Sprint 0: stable identity. `fingerprint` is the cross-restart/recurrence
  // identity; `id` is the current occurrence id (what Kobi/Autopilot
  // reference). Both optional for backward-compat with older API payloads.
  fingerprint?: string
  ruleId?: string
  severity: 'critical' | 'warning' | 'info'
  category: string
  resource: string
  namespace: string
  title: string
  message: string
  suggestion: string
  firstSeen: string
  lastSeen: string
  resolved?: boolean
  resolvedAt?: string
}

// Helm releases — read-only first-class (Sprint 4). Decoded from Helm's
// storage Secrets server-side; no write verbs in 1.14.
export interface HelmRelease {
  name: string
  namespace: string
  revision: number
  status: string
  chart: string
  chartVersion: string
  appVersion?: string
  updated: string
  firstDeployed?: string
  description?: string
}

export interface HelmReleaseRevision {
  revision: number
  status: string
  chartVersion: string
  appVersion?: string
  updated: string
  description?: string
}

export interface HelmChartDependency {
  name: string
  version?: string
  repository?: string
  condition?: string
}

export interface HelmReleaseDetail extends HelmRelease {
  values?: Record<string, unknown>
  manifest?: string
  notes?: string
  history?: HelmReleaseRevision[]
  dependencies?: HelmChartDependency[]
}

export interface InsightCount {
  critical: number
  warning: number
  info: number
}

// Cluster health
// Status mirrors what the connector emits (apps/api/internal/cluster/connector.go:GetHealth):
// "healthy", "warning", "critical". An earlier version of this type used
// "degraded" — that was wrong; the backend never produced it, so callers
// matching `=== 'degraded'` silently fell through to the default branch
// (typically the error/red treatment).
export interface ClusterHealth {
  status: 'healthy' | 'warning' | 'critical'
  score: number
  insights: InsightCount
  checks: HealthCheck[]
}

export interface HealthCheck {
  name: string
  status: 'pass' | 'warn' | 'fail'
  message: string
}

// Cluster overview
export interface ClusterOverview {
  clusterName?: string
  // kube-system namespace UID — same value the agent stamps on every
  // sample's `cluster_id` label, exposed here so the UI can render
  // a copy-pasteable Prom `external_labels.cluster_id` snippet
  // pre-filled for the active cluster.
  clusterUID?: string
  kubernetesVersion?: string
  platform?: string
  networkPolicies?: ResourceCount
  podDisruptionBudgets?: ResourceCount
  nodes?: ResourceCount
  pods?: ResourceCount
  namespaces?: ResourceCount
  services?: ResourceCount
  deployments?: ResourceCount
  statefulSets?: ResourceCount
  daemonSets?: ResourceCount
  jobs?: ResourceCount
  cronJobs?: ResourceCount
  ingresses?: ResourceCount
  // Gateway API resources — counts are 0 when the CRDs aren't
  // installed; sidebar hides the chip on zero.
  gateways?: ResourceCount
  httpRoutes?: ResourceCount
  serviceAccounts?: ResourceCount
  certificates?: ResourceCount
  argocdApps?: ResourceCount
  vpas?: ResourceCount
  ciliumNetworkPolicies?: ResourceCount
  ciliumClusterwideNetworkPolicies?: ResourceCount
  helmReleases?: ResourceCount
  // Endpoints — counted from EndpointSlices, matching what the
  // /resources/endpoints list endpoint returns.
  endpoints?: ResourceCount
  configMaps?: ResourceCount
  secrets?: ResourceCount
  pvcs?: ResourceCount
  pvs?: ResourceCount
  hpas?: ResourceCount
  cpu?: ResourceUsage
  memory?: ResourceUsage
  health?: ClusterHealth
  events?: KubeEvent[]
  namespaceWorkloads?: NamespaceWorkload[]
  permissions?: Record<string, boolean>
  // Optional-CRD keys RBAC grants but the cluster lacks — dimmed "not available"
  // in the UI, but excluded from the limited-access banner (not a restriction).
  absentResources?: string[]
}

// Events
export interface KubeEvent {
  type: 'Normal' | 'Warning'
  reason: string
  message: string
  object: string
  namespace: string
  timestamp: string
  count: number
}

// Namespace workload summary
export interface NamespaceWorkload {
  namespace: string
  workloads?: WorkloadSummary[]
}

export interface WorkloadSummary {
  name: string
  kind: string
  namespace: string
  replicas: number
  readyReplicas: number
  status: string
  cpu?: ResourceUsage
  memory?: ResourceUsage
  pods?: PodSummary[]
}

export interface PodSummary {
  name: string
  status: string
  ready: boolean
}

// Topology
export interface TopologyNode {
  id: string
  type: string
  name: string
  label: string
  namespace: string
  status: string
  kind: string
  metadata: Record<string, string>
  cpu?: ResourceUsage
  memory?: ResourceUsage
  pods?: PodSummary[]
}

export interface TopologyEdge {
  id: string
  source: string
  target: string
  type: string
  label?: string
  animated?: boolean
}

export interface Topology {
  nodes: TopologyNode[]
  edges: TopologyEdge[]
}

// Resource metrics
export interface ResourceMetrics {
  cpu: MetricPoint[]
  memory: MetricPoint[]
  network?: MetricPoint[]
}

// Generic resource list
export interface ResourceList {
  kind: string
  items: ResourceItem[]
  total: number
}

export interface ResourceItem {
  name: string
  namespace: string
  status: string
  age: string
  createdAt: string
  labels: Record<string, string>
  annotations: Record<string, string>
  [key: string]: unknown
}

// WebSocket
export interface WSMessage {
  type: 'subscribe' | 'unsubscribe' | 'ping'
  resources?: string[]
}

export interface WSEvent {
  type: 'ADDED' | 'MODIFIED' | 'DELETED'
  resource: string
  object: ResourceItem
}

// Cluster info
export interface ClusterInfo {
  name: string
  context: string
  server: string
  active: boolean
  status: 'connected' | 'disconnected' | 'error'
  error?: string
  displayName?: string
  source?: 'file' | 'uploaded' | 'in-cluster' | 'agent-proxy'
  // kube-system namespace UID — same value the agent stamps on
  // each sample's `cluster_id` label. Populated only for
  // agent-proxy contexts (we get it via the Hello message);
  // empty for direct-kubeconfig contexts we haven't probed.
  clusterId?: string
}

// API params
export interface ResourceParams {
  namespace?: string
  search?: string
  status?: string
  // Filter by node — currently only meaningful for pods (the field is
  // empty on every other resource type). Used by the Node detail page's
  // Pods tab and reusable for click-to-filter on the Node column.
  node?: string
  sort?: string
  order?: 'asc' | 'desc'
  page?: number
  limit?: number
}

export interface InsightParams {
  severity?: string
  category?: string
  namespace?: string
}

export interface EventParams {
  namespace?: string
  type?: 'Normal' | 'Warning'
  limit?: number
  involvedKind?: string
  involvedName?: string
}
