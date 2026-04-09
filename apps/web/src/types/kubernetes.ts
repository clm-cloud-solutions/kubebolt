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
  severity: 'critical' | 'warning' | 'info'
  category: string
  resource: string
  namespace: string
  title: string
  message: string
  suggestion: string
  firstSeen: string
  lastSeen: string
}

export interface InsightCount {
  critical: number
  warning: number
  info: number
}

// Cluster health
export interface ClusterHealth {
  status: 'healthy' | 'degraded' | 'critical'
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
  kubernetesVersion?: string
  platform?: string
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
}

// API params
export interface ResourceParams {
  namespace?: string
  search?: string
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
