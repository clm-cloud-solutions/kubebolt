// Kobi Plans — the plan as GET /plans/{id} returns it (Go: kobiplan.Plan plus
// the read side's view fields, api/kobiplans_routes.go).

export type PlanStatus = 'draft' | 'exported' | 'verified' | 'expired' | 'approved' | 'executing' | 'failed'
export type PlanRisk = 'low' | 'medium' | 'high'
export type PlanOwnerKind = 'provider' | 'operator' | 'helm' | 'gitops' | 'none' | 'unknown'

export interface PlanMoney {
  monthly: number
  currency: string
  source?: string
}

export interface PlanSavings {
  cpuMilli: number
  memBytes: number
  money: PlanMoney | null
  moneyUnavailable?: string
}

export interface PlanOwner {
  kind: PlanOwnerKind
  ref?: string
  // The owner's namespace when it is not the workload's (a Helm release's).
  namespace?: string
}

export interface PlanTarget {
  type: string
  namespace?: string
  name: string
  owner?: PlanOwner
}

export interface PlanQuantities {
  cpu?: string
  memory?: string
}

export interface PlanContainerResources {
  container: string
  initContainer?: boolean
  requests?: PlanQuantities
  limits?: PlanQuantities
}

export interface PlanSetResources {
  containers: PlanContainerResources[]
}

// What the plan's workloads cost before anything is applied, measured at the
// first export (P20). windowDays is the data the average covers (up to 7);
// no amounts without OpenCost.
export interface PlanBaseline {
  capturedAt: string
  windowDays?: number
  allocatedCost?: PlanMoney
  clusterCost?: PlanMoney
  // 'bill': the cluster's bill at list prices (nodes, disks, load balancers,
  // the management fee). Absent: the nodes alone (plans exported before 26-nov).
  clusterCostCovers?: 'bill'
}

export interface PlanFinding {
  state: string
  request: number
  limit: number
  p95: number
  suggest?: number
}

// A monthly cost before and after, in one currency (P20).
export interface PlanCostDelta {
  before: number
  after: number
  currency: string
}

// What a step or the plan brought, measured 7 days after it was applied
// (plans-07). clusterCost is context, never attributed to the plan.
export interface PlanObserved {
  measuredAt: string
  windowDays: number
  cpuMilliFreed: number
  memBytesFreed: number
  allocatedCost?: PlanCostDelta
  clusterCost?: PlanCostDelta & { attributed: boolean }
}

// One run of a step's checks (plans-07): each check's outcome is ok, failed,
// not_applicable or unknown.
export interface PlanVerificationResult {
  checkedAt: string
  passed: boolean
  checks: Record<string, string>
  detail?: string
}

// A consolidation step's change (plans-16, remove_nodes): the nodes it
// suggests taking out of a pool, least loaded first.
export interface PlanRemoveNodes {
  pool: string
  poolSource: 'eks' | 'aks' | 'gke' | 'karpenter' | 'none' | string
  autoscaler: 'none' | 'cluster_autoscaler' | 'karpenter' | string
  nodesBefore: number
  nodesAfter: number
  nodes: { name: string; zone?: string; instanceType?: string; providerId?: string }[]
}

export interface PlanPoolBefore {
  nodes: number
  instanceTypes: string[]
  zones: string[]
}

// One node's load as a share of its allocatable, before and after.
export interface PlanNodeLoad {
  node: string
  zone?: string
  cpuBefore: number
  memBefore: number
  cpuAfter: number
  memAfter: number
  removed?: boolean
  monthly?: number
}

export interface PlanNodeBlock {
  node: string
  reason: string
  pod?: string
  owner?: string
}

// Why a consolidation step's nodes are left over.
export interface PlanConsolidationEvidence {
  scenario: 'current' | 'planned'
  removableToday: number
  ceiling: number
  minNodes: number
  windowDays?: number
  loads: PlanNodeLoad[]
  blocked?: PlanNodeBlock[]
}

export interface PlanStep {
  id: string
  order: number
  action: string
  target: PlanTarget
  exportOnly: boolean
  exportOnlyReason?: string
  // set_resources: the containers' change; remove_nodes: RemoveNodes (read
  // through removeNodesOf).
  params?: PlanSetResources
  before?: PlanSetResources
  evidence?: {
    cpu?: PlanFinding
    mem?: PlanFinding
    pods?: number
    podsStableInWindow?: boolean
    // The admission webhooks its pods serve (plans-13).
    webhooks?: { name: string; failurePolicy: string; timeoutSeconds: number }[]
    // The pod's QoS class before and after the step, and the request that
    // keeps it Guaranteed when the step raises a limit (plans-14).
    qos?: { before: string; after: string; keepWith?: { cpu?: string; memory?: string } }
  }
  precondition: { specHash: string; generation?: number }
  verification: { window: string; checks: string[]; result?: PlanVerificationResult }
  rollback?: { action: string; params?: PlanSetResources }
  risk: PlanRisk
  riskReasons: string[]
  imposed: boolean
  // The engine's values, kept when the person applied others (plans-15).
  engineParams?: PlanSetResources
  impact: { estimated: PlanSavings; observed: PlanObserved | null }
  baseline?: PlanBaseline | null
  provenance?: string
  status: 'pending' | 'applied' | 'verified' | 'expired'
  // When it was applied (or found applied) and when it passed its checks.
  appliedAt?: string
  verifiedAt?: string
  // A consolidation step's nodes already removed (plans-16): they go one at
  // a time, and the step is applied once the pool is at its new size.
  nodesGone?: string[]
  // Its nodes still in the pool but cordoned: being emptied.
  nodesCordoned?: string[]
}

export interface PlanExclusion {
  target: PlanTarget
  reason: string
  detail?: string
  reevaluateAt?: string
}

export interface KobiPlan {
  id: string
  orgId: string
  clusterId: string
  title: string
  kind: string
  scope: { namespaces: string[]; workloads: string[] }
  version: number
  status: PlanStatus
  expiredReason?: string
  createdAt: string
  updatedAt: string
  source: { initiator: string; userId?: string; userName?: string; conversationId?: string }
  programId: string | null
  expiresAt: string
  exportedAt: string | null
  exports: { format: string; at: string; by?: string }[]
  engine: {
    name: string
    percentile?: number
    windowDays?: number
    minHistoryHours?: number
    lowConfidence: boolean
    computedAt: string
  }
  impact: { estimated: PlanSavings; observed: PlanObserved | null }
  baseline: PlanBaseline | null
  steps: PlanStep[]
  excluded: PlanExclusion[]
  notes: { version: number; at: string; text: string }[]
  contentHash: string
  // The plans whose changes this one counts on (plans-16).
  dependsOn?: string[]
}

// Another plan this one is tied to (plans-16).
export interface PlanLink {
  id: string
  title: string
  kind: string
  effectiveStatus: PlanStatus
  stepCount: number
  appliedCount: number
  estimated: PlanSavings
}

export interface KobiPlanView extends KobiPlan {
  clusterName?: string
  effectiveStatus: PlanStatus
  effectiveExpiredReason?: string
  maxRisk?: PlanRisk
  // The author's display name today, else the username the plan recorded.
  createdBy?: string
  // The plans this one depends on, and the live consolidation plans that
  // depend on it (plans-16).
  dependsOnPlans?: PlanLink[]
  leadsTo?: PlanLink[]
  // The bill a consolidation plan starts from: its baseline once exported,
  // else the bill today (none without OpenCost).
  clusterBill?: PlanMoney
}

export type PlanExportFormat = 'commands' | 'diff'

// What GET/POST /plans/{id}/export return: the text to copy, and after a POST
// the plan as it is now (exported, with its record).
export interface PlanExport {
  format: PlanExportFormat
  filename: string
  text: string
  notes: string[]
}

export interface PlanExportResponse {
  plan?: KobiPlanView
  export: PlanExport
}

// One row of GET /plans (Go: api.planSummary).
export interface KobiPlanSummary {
  id: string
  title: string
  kind: string
  clusterId: string
  clusterName?: string
  version: number
  status: PlanStatus
  effectiveStatus: PlanStatus
  effectiveExpiredReason?: string
  createdAt: string
  expiresAt: string
  createdBy?: string
  estimated: PlanSavings
  stepCount: number
  /** Steps already made in the cluster (applied or verified). */
  appliedCount: number
  excludedCount: number
  maxRisk?: PlanRisk
  // How many nodes a consolidation plan's live steps take out (plans-16).
  nodesRemoved?: number
  // Related plans the same list holds (plans-16).
  dependsOn?: { id: string; title: string }[]
  leadsTo?: { id: string; title: string }[]
}

// One cluster's bill and what its live plans do to it (GET /plans/bill-path).
export interface PlanBillPath {
  clusterId: string
  clusterName?: string
  bill: PlanMoney | null
  rightsizing: PlanLink[]
  consolidation: (PlanLink & { monthly: PlanMoney | null; pools: { pool: string; nodesBefore: number; nodesAfter: number }[] })[]
  reachable: PlanMoney | null
  // The fleet's path (no cluster asked): each priced cluster with live plans,
  // the one that takes most off first, and those without OpenCost prices,
  // left out of every figure.
  clusters?: PlanBillPathCluster[]
  unpriced?: { clusterId: string; clusterName?: string }[]
}

export interface PlanBillPathCluster {
  clusterId: string
  clusterName?: string
  bill: PlanMoney
  reachable: PlanMoney
  off: number
  nodes: number
  plans: number
}

// What a create_plan call made or found, carried to the chat's plan card in a
// field the model never reads.
export interface PlanRef {
  id: string
  // existing_same_scope: a live plan already covers the same workloads
  // (plans-06); existing_live_plan: the org keeps one per cluster (Free).
  outcome: 'created' | 'existing_live_plan' | 'existing_same_scope' | 'revised'
}
