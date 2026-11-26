// Test fixture: the kyverno plan of the first live tests (25-nov), as
// GET /plans/{id} returns it. Imported by tests only.
import type { KobiPlanView } from '@/types/kobiPlans'

export function kyvernoPlan(overrides: Partial<KobiPlanView> = {}): KobiPlanView {
  const at = '2026-11-25T18:16:30Z'
  return {
    id: 'pln_d6551a4c',
    orgId: 'org-1',
    clusterId: 'c-dev',
    clusterName: 'kind-kubebolt-dev',
    title: 'Right-sizing: namespace kyverno',
    kind: 'rightsizing',
    scope: { namespaces: ['kyverno'], workloads: [] },
    version: 1,
    status: 'draft',
    effectiveStatus: 'draft',
    maxRisk: 'medium',
    createdAt: at,
    updatedAt: at,
    source: { initiator: 'chat', userId: 'u-1', userName: 'ana', conversationId: 'conv-1' },
    programId: null,
    expiresAt: new Date(Date.now() + 6.5 * 86_400_000).toISOString(),
    exportedAt: null,
    exports: [],
    engine: { name: 'rightsizing', percentile: 0.95, windowDays: 3.1, minHistoryHours: 48, lowConfidence: false, computedAt: at },
    impact: { estimated: { cpuMilli: 330, memBytes: 0, money: { monthly: 6.97, currency: 'eur', source: 'opencost' } }, observed: null },
    baseline: null,
    steps: [
      {
        id: 's3',
        order: 1,
        action: 'set_resources',
        target: { type: 'deployments', namespace: 'kyverno', name: 'kyverno-admission-controller', owner: { kind: 'helm', ref: 'kyverno' } },
        exportOnly: false,
        params: { containers: [{ container: 'kyverno', requests: { cpu: '20m' } }] },
        before: { containers: [{ container: 'kyverno', requests: { cpu: '100m', memory: '128Mi' }, limits: { memory: '384Mi' } }] },
        evidence: {
          cpu: { state: 'over', request: 100, limit: 0, p95: 16, suggest: 20 },
          mem: { state: 'ok', request: 134217728, limit: 402653184, p95: 134488883 },
          pods: 1,
          podsStableInWindow: true,
        },
        precondition: { specHash: 'sha256:dbf80b83a0d673aaf0a3818827f589f9', generation: 1 },
        verification: { window: '24h', checks: ['rollout_complete', 'no_oom_killed'] },
        rollback: { action: 'set_resources', params: { containers: [{ container: 'kyverno', requests: { cpu: '100m' } }] } },
        risk: 'medium',
        riskReasons: ['single_pod'],
        imposed: false,
        impact: { estimated: { cpuMilli: 80, memBytes: 0, money: { monthly: 1.69, currency: 'eur', source: 'opencost' } }, observed: null },
        provenance: 'rightsizing · P95 7 d · request ×1.2 · 1 pod',
        status: 'pending',
      },
    ],
    excluded: [
      {
        target: { type: 'daemonsets', namespace: 'kube-system', name: 'azure-cns', owner: { kind: 'provider', ref: 'aks' } },
        reason: 'provider_managed',
        detail: 'kubernetes.azure.com/managedby',
      },
    ],
    notes: [],
    contentHash: 'sha256:aa41',
    ...overrides,
  }
}

// A consolidation plan (plans-16): pool general of prod-eks, one node left
// over with the requests the right-sizing plan leaves.
export function consolidationPlan(overrides: Partial<KobiPlanView> = {}): KobiPlanView {
  const at = '2026-11-26T11:02:00Z'
  const price = 129.4
  const load = (node: string, zone: string, cb: number, mb: number, ca: number, ma: number, removed = false) => ({
    node, zone, cpuBefore: cb, memBefore: mb, cpuAfter: removed ? 0 : ca, memAfter: removed ? 0 : ma, removed, monthly: price,
  })
  return {
    ...kyvernoPlan(),
    id: 'pln_cons',
    clusterName: 'prod-eks',
    title: 'Consolidation · prod-eks',
    kind: 'consolidation',
    scope: { namespaces: [], workloads: [] },
    createdAt: at,
    engine: { name: 'consolidation', percentile: 0.95, windowDays: 7, lowConfidence: false, computedAt: at },
    impact: { estimated: { cpuMilli: 4000, memBytes: 16 * 2 ** 30, money: { monthly: price, currency: 'eur', source: 'opencost' } }, observed: null },
    dependsOn: ['pln_d6551a4c'],
    dependsOnPlans: [
      { id: 'pln_d6551a4c', title: 'Right-sizing · prod-eks', kind: 'rightsizing', effectiveStatus: 'exported', stepCount: 3, appliedCount: 1,
        estimated: { cpuMilli: 1690, memBytes: 2.62 * 2 ** 30, money: { monthly: 41.2, currency: 'eur' } } },
    ],
    clusterBill: { monthly: 1210, currency: 'eur', source: 'opencost' },
    steps: [
      {
        id: 's1', order: 1, action: 'remove_nodes',
        target: { type: 'nodepools', name: 'general', owner: { kind: 'unknown' } },
        exportOnly: true, exportOnlyReason: "the pool's size lives in your infrastructure",
        params: { pool: 'general', poolSource: 'eks', autoscaler: 'none', nodesBefore: 5, nodesAfter: 4,
          nodes: [{ name: 'ip-10-0-3-41', zone: 'us-east-1c', instanceType: 'm6i.xlarge', providerId: 'aws:///us-east-1c/i-0abc' }] } as never,
        before: { nodes: 5, instanceTypes: ['m6i.xlarge'], zones: ['us-east-1a', 'us-east-1b', 'us-east-1c'] } as never,
        evidence: {
          scenario: 'planned', removableToday: 0, ceiling: 0.8, minNodes: 3, windowDays: 7,
          loads: [
            load('ip-10-0-1-17', 'us-east-1a', 58, 61, 74, 77), load('ip-10-0-1-88', 'us-east-1a', 52, 49, 71, 68),
            load('ip-10-0-2-35', 'us-east-1b', 55, 57, 73, 75), load('ip-10-0-3-12', 'us-east-1c', 49, 53, 69, 72),
            load('ip-10-0-3-41', 'us-east-1c', 31, 28, 0, 0, true),
          ],
        } as never,
        precondition: { specHash: 'sha256:pool' },
        verification: { window: '7d', checks: ['pool_held', 'no_pending_pods', 'no_pressure_evictions', 'no_oom_killed'] },
        risk: 'medium', riskReasons: ['less_headroom', 'depends_on_plan'], imposed: false,
        impact: { estimated: { cpuMilli: 4000, memBytes: 16 * 2 ** 30, money: { monthly: price, currency: 'eur' } }, observed: null },
        provenance: 'consolidation', status: 'pending',
      },
    ],
    excluded: [
      { target: { type: 'nodepools', name: 'batch', owner: { kind: 'unknown' } }, reason: 'pool_blocked', detail: 'the PodDisruptionBudget etl/loader allows no disruption now, so etl/loader-0 keeps ip-10-0-9-2' },
    ],
    notes: [],
    ...overrides,
  }
}
