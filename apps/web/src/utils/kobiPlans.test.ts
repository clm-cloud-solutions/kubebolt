import { describe, expect, it } from 'vitest'
import {
  blockWords,
  changeLines,
  editableValues,
  excludedByReason,
  expiresIn,
  freedUnits,
  historyWords,
  ownerWords,
  applyWhere,
  baselineWords,
  observedImpact,
  quantityBytes,
  quantityMilli,
  requestedBefore,
  rollbackCommand,
  rollbackFor,
  scopeWords,
  sharedNamespace,
  valueRange,
  valuesBody,
  workloadName,
} from './kobiPlans'
import { kyvernoPlan } from '@/components/kobiPlans/planFixture'

describe('a step\'s editable values', () => {
  it('offers only what the step changes, with the engine\'s value and the per-pod P95 floor', () => {
    const step = kyvernoPlan().steps[0]
    const rows = editableValues({ ...step, engineParams: { containers: [{ container: 'kyverno', requests: { cpu: '20m' } }] } })
    expect(rows).toHaveLength(1)
    expect(rows[0]).toMatchObject({ container: 'kyverno', field: 'requests', res: 'cpu', value: '20m', engine: '20m', floor: '16m', today: '100m' })
    expect(valueRange(rows[0])).toBe('at least 16m (P95 per pod) · under 100m (today)')
    expect(valuesBody(rows, { [rows[0].key]: ' 30m ' })).toEqual({ containers: [{ container: 'kyverno', requests: { cpu: '30m' } }] })
    // Untouched values go back as they are.
    expect(valuesBody(rows, {})).toEqual({ containers: [{ container: 'kyverno', requests: { cpu: '20m' } }] })
  })
})

describe('kobiPlans words', () => {
  it('names the owner in words', () => {
    expect(ownerWords({ kind: 'helm', ref: 'kyverno' })).toBe('Helm · release kyverno')
    expect(ownerWords({ kind: 'gitops', ref: 'argocd:shop' })).toBe('Argo CD · shop')
    expect(ownerWords({ kind: 'provider', ref: 'aks' })).toBe('provider · AKS')
    expect(ownerWords({ kind: 'unknown' })).toBe('not identified')
    expect(ownerWords(undefined)).toBe('not identified')
  })

  it('names only what a step frees', () => {
    expect(freedUnits({ cpuMilli: 330, memBytes: 0, money: null })).toBe('330m CPU')
    expect(freedUnits({ cpuMilli: 90, memBytes: 120 * 1024 * 1024, money: null })).toBe('90m CPU · 120 MiB memory')
    expect(freedUnits({ cpuMilli: 1500, memBytes: 3 * 1024 ** 3, money: null })).toBe('1.5 cores CPU · 3 GiB memory')
    expect(freedUnits({ cpuMilli: 0, memBytes: 0, money: null })).toBeNull()
  })

  it('says the window and, for a younger cluster, how much data it covers', () => {
    expect(historyWords({ engine: { name: 'rightsizing', windowDays: 7, lowConfidence: false, computedAt: '' } })).toBe('P95 of the last 7 days')
    expect(historyWords({ engine: { name: 'rightsizing', windowDays: 3.1, lowConfidence: false, computedAt: '' } })).toMatch(/^P95 of the last 7 days \(3.1 days of data\)$/)
  })

  it('says the scope', () => {
    expect(scopeWords({ scope: { namespaces: [], workloads: [] } })).toBe('whole cluster')
    expect(scopeWords({ scope: { namespaces: ['kyverno'], workloads: [] } })).toBe('namespace kyverno')
    expect(
      scopeWords({ scope: { namespaces: ['kyverno'], workloads: ['kyverno/a', 'kyverno/b', 'kyverno/c', 'kyverno/d'] } }),
    ).toBe('namespace kyverno · 4 workloads')
    expect(scopeWords({ scope: { namespaces: ['kyverno'], workloads: ['kyverno/deployment/a'] } })).toBe('namespace kyverno · a')
    expect(scopeWords({ scope: { namespaces: [], workloads: ['shop/api'] } })).toBe('shop/api')
  })

  it('says the namespace once when every workload shares it', () => {
    const t = (namespace: string, name: string) => ({ target: { type: 'deployments', namespace, name } })
    const one = { steps: [t('kyverno', 'a'), t('kyverno', 'b')], excluded: [] } as never
    const two = { steps: [t('kyverno', 'a')], excluded: [t('kube-system', 'c')] } as never
    expect(sharedNamespace(one)).toBe('kyverno')
    expect(sharedNamespace(two)).toBeNull()
    expect(workloadName({ namespace: 'kyverno', name: 'a' }, 'kyverno')).toBe('a')
    expect(workloadName({ namespace: 'kyverno', name: 'a' }, null)).toBe('kyverno / a')
  })

  it('says each change with what it was', () => {
    const lines = changeLines({
      params: { containers: [{ container: 'api', requests: { cpu: '120m', memory: '300Mi' } }] },
      before: { containers: [{ container: 'api', requests: { cpu: '500m', memory: '1Gi' }, limits: { cpu: '1' } }] },
    })
    expect(lines.map((l) => l.text)).toEqual(['cpu request 500m → 120m', 'memory request 1Gi → 300Mi'])
  })

  it('writes the rollback as a kubectl command', () => {
    expect(
      rollbackCommand({
        target: { type: 'deployments', namespace: 'payments', name: 'api' },
        rollback: { action: 'set_resources', params: { containers: [{ container: 'api', requests: { cpu: '500m', memory: '1Gi' } }] } },
      }),
    ).toBe('kubectl -n payments set resources deployment/api -c api --requests=cpu=500m,memory=1Gi')
    expect(rollbackCommand({ target: { type: 'pvcs', name: 'x' }, rollback: undefined })).toBeNull()
  })

  // A Helm or GitOps workload is undone where it was changed: a kubectl
  // command would be overwritten by the next upgrade or sync.
  it('undoes a step where its owner lets it be undone', () => {
    const rollback = { action: 'set_resources', params: { containers: [{ container: 'controller', requests: { cpu: '100m' } }] } }
    const helm = rollbackFor({ target: { type: 'deployments', namespace: 'kyverno', name: 'cleanup', owner: { kind: 'helm', ref: 'kyverno' } }, rollback })
    expect(helm.command).toBe('helm rollback kyverno -n kyverno')
    expect(helm.text).toMatch(/put cpu request 100m back in the values of release kyverno/)
    const other = rollbackFor({ target: { type: 'deployments', namespace: 'apps', name: 'x', owner: { kind: 'helm', ref: 'rel', namespace: 'infra' } }, rollback })
    expect(other.command).toBe('helm rollback rel -n infra')
    const gitops = rollbackFor({ target: { type: 'deployments', namespace: 'shop', name: 'web', owner: { kind: 'gitops', ref: 'argocd:shop' } }, rollback })
    expect(gitops.command).toBeNull()
    expect(gitops.text).toMatch(/Revert the change in the repository/)
    const nobody = rollbackFor({ target: { type: 'deployments', namespace: 'shop', name: 'web', owner: { kind: 'unknown' } }, rollback })
    expect(nobody.command).toBe('kubectl -n shop set resources deployment/web -c controller --requests=cpu=100m')
    expect(applyWhere({ kind: 'helm', ref: 'kyverno' })).toBe('in the values of release kyverno')
    expect(applyWhere({ kind: 'unknown' })).toBeNull()
  })

  it('groups exclusions by reason', () => {
    const groups = excludedByReason([
      { target: { type: 'deployments', name: 'a' }, reason: 'hpa_target' },
      { target: { type: 'deployments', name: 'b' }, reason: 'provider_managed' },
      { target: { type: 'deployments', name: 'c' }, reason: 'hpa_target' },
    ])
    expect(groups.map((g) => [g.reason, g.items.length])).toEqual([['hpa_target', 2], ['provider_managed', 1]])
  })

  it('counts the days to expiry', () => {
    const now = Date.parse('2026-11-25T18:00:00Z')
    expect(expiresIn('2026-12-02T18:00:00Z', now)).toBe('in 7 days')
    expect(expiresIn('2026-11-25T20:00:00Z', now)).toBe('today')
    expect(expiresIn('2026-11-24T18:00:00Z', now)).toBeNull()
  })

  // The impact card says what the plan's workloads cost before: none until
  // the first export, then their monthly cost and the data it averages.
  it('says the baseline', () => {
    const eur = (monthly: number) => ({ monthly, currency: 'eur', source: 'opencost' })
    const steps = [
      { baseline: { capturedAt: 'x', windowDays: 3.3, allocatedCost: eur(4.61) } },
      { baseline: { capturedAt: 'x', windowDays: 3.3, allocatedCost: eur(2.66) } },
    ] as never
    expect(baselineWords({ exportedAt: null, baseline: null, steps })).toBe('baseline taken at export')
    expect(baselineWords({ exportedAt: 'x', baseline: { capturedAt: 'x', windowDays: 3.3 }, steps })).toMatch(/^before: €?7[.,]27(\s?€)?\/month · 3\.3 days of data$/)
    const noMoney = [{ baseline: { capturedAt: 'x' } }] as never
    expect(baselineWords({ exportedAt: 'x', baseline: { capturedAt: 'x' }, steps: noMoney })).toBe("before: each step's requests")
    // With the cluster's bill, that is the baseline the impact is read against.
    expect(baselineWords({ exportedAt: 'x', baseline: { capturedAt: 'x', windowDays: 7, clusterCost: eur(96.5), clusterCostCovers: 'bill' }, steps })).toMatch(
      /^cluster bill before: €?96[.,]50(\s?€)?\/month · 7 days of data$/,
    )
  })

  // What a plan frees is not a saving (26-nov): measured, the headline is the
  // cluster's bill before and after; without it, the capacity freed, idle
  // while the nodes stay.
  it('reads the observed impact on the cluster bill, never as a saving', () => {
    const base = { steps: [], baseline: { capturedAt: 'x', clusterCostCovers: 'bill' as const }, exportedAt: 'x' }
    const observed = { measuredAt: 'x', windowDays: 7, cpuMilliFreed: 1140, memBytesFreed: 0, allocatedCost: { before: 20, after: 6.5, currency: 'eur' } }
    const flat = observedImpact({ ...base, impact: { estimated: { cpuMilli: 1140, memBytes: 0, money: null }, observed: { ...observed, clusterCost: { before: 100, after: 100, currency: 'eur', attributed: false } } } })
    expect(flat.value).toMatch(/^\+/)
    expect(flat.description).toBe('on the cluster bill: no saving yet')
    expect(flat.caption).toContain('1.14 cores CPU freed')
    const fell = observedImpact({ ...base, impact: { estimated: { cpuMilli: 1140, memBytes: 0, money: null }, observed: { ...observed, clusterCost: { before: 100, after: 96, currency: 'eur', attributed: false } } } })
    // The sign is joined to the amount: a narrow card broke them apart.
    expect(fell.value).toMatch(/^−\u2060€?4[.,]00/)
    expect(fell.description).toBe('on the cluster bill, every change on it counted')
    const noBill = observedImpact({ ...base, impact: { estimated: { cpuMilli: 1140, memBytes: 0, money: null }, observed } })
    // Without the bill: how much of the estimate was measured, never the
    // estimate's own figure again.
    expect([noBill.value, noBill.unit, noBill.description]).toEqual(['100%', 'of the estimate', 'measured: 1.14 cores CPU freed, idle while the nodes stay'])
    expect(noBill.compare).toEqual({ dim: 'cpu', estimated: 1140, measured: 1140 })
    expect(fell.bills).toEqual({ before: 100, after: 96, currency: 'eur' })
    expect(noBill.caption).toMatch(/13[.,]50.*\/month at list prices$/)
    for (const c of [flat, fell, noBill]) expect(`${c.description} ${c.caption}`).not.toMatch(/savings/)
    // Both units freed: CPU is the figure, memory rides in the description,
    // never the whole phrase as the figure (it wrapped onto two lines).
    const both = observedImpact({ ...base, impact: { estimated: { cpuMilli: 150, memBytes: 0, money: null }, observed: { ...observed, cpuMilliFreed: 150, memBytesFreed: 100 * 2 ** 20, allocatedCost: undefined } } })
    expect([both.value, both.unit, both.description, both.highlight]).toEqual(['100%', 'of the estimate', 'measured: 150m CPU freed, plus 100 MiB memory', '100 MiB memory'])
    const withCost = observedImpact({ ...base, impact: { estimated: { cpuMilli: 150, memBytes: 0, money: null }, observed: { ...observed, cpuMilliFreed: 150, memBytesFreed: 100 * 2 ** 20 } } })
    expect([withCost.value, withCost.unit, withCost.description]).toEqual(['100%', 'of the estimate', 'measured: 150m CPU freed, plus 100 MiB memory, idle while the nodes stay'])
  })

  // The «capacity it frees» chart: today's requests of the plan's workloads,
  // per pod times the pods, in the plan's own quantities.
  it("reads what the plan's workloads request today", () => {
    expect([quantityMilli('100m'), quantityMilli('1'), quantityMilli('0.5'), quantityMilli('x')]).toEqual([100, 1000, 500, null])
    expect([quantityBytes('512Mi'), quantityBytes('1Gi'), quantityBytes('1G'), quantityBytes('?')]).toEqual([512 * 2 ** 20, 2 ** 30, 1e9, null])
    const step = (status: string, before: string, pods: number) => ({
      action: 'set_resources', status, evidence: { pods },
      params: { containers: [{ container: 'app', requests: { cpu: '50m' } }] },
      before: { containers: [{ container: 'app', requests: { cpu: before } }, { container: 'sidecar', requests: { cpu: '1' } }] },
    })
    const limitOnly = { ...step('pending', '300m', 2), params: { containers: [{ container: 'app', limits: { memory: '1Gi' } }] } }
    const plan = { steps: [step('pending', '200m', 3), step('applied', '1', 1), step('expired', '5', 1), limitOnly] } as never
    expect(requestedBefore(plan, 'cpu')).toBe(1600)
  })
})

describe('blockWords', () => {
  it('says which scheduling rule keeps a node, in words', () => {
    expect(blockWords({ node: 'node-3', reason: 'no_place', pod: 'shop/cache-3', owner: 'shop/cache' })).toBe(
      'a pod of shop/cache has nowhere to go among the nodes that stay: its scheduling rules (one per node, node affinity, host ports, selector or taints) leave none, so it keeps node-3',
    )
    expect(blockWords({ node: 'node-2', reason: 'unsimulated_rules', pod: 'shop/api-1', owner: 'shop/api' })).toBe(
      'shop/api has scheduling rules the plan does not simulate (pod affinity, a hard topology spread or anti-affinity across zones), so it keeps node-2',
    )
  })
})
