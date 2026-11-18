import { describe, expect, it } from 'vitest'
import type { ClusterProgress } from '@/types/kubernetes'
import { etaSeconds, kindPlural, prepareEvents, prepareView } from './preparing'

const base: ClusterProgress = {
  context: 'agent:c1',
  state: 'connecting',
  agents: 0,
  accessChecked: false,
  readable: 0,
  probed: 0,
  types: null,
}

const loading = (rsLoaded: number, rsSynced = false): ClusterProgress => ({
  ...base,
  agents: 2,
  accessChecked: true,
  readable: 29,
  probed: 31,
  types: [
    { kind: 'Pod', synced: true, loaded: 0, total: 0 },
    { kind: 'Deployment', synced: true, loaded: 612, total: 612 },
    { kind: 'ReplicaSet', synced: rsSynced, loaded: rsLoaded, total: 5632 },
  ],
})

describe('kindPlural', () => {
  it('pluralizes the Kubernetes kinds the cache loads', () => {
    expect(kindPlural('ReplicaSet')).toBe('ReplicaSets')
    expect(kindPlural('Ingress')).toBe('Ingresses')
    expect(kindPlural('NetworkPolicy')).toBe('NetworkPolicies')
    expect(kindPlural('StorageClass')).toBe('StorageClasses')
  })
})

describe('prepareView', () => {
  it('says what happens and why at each stage', () => {
    expect(prepareView(base, 'prod').what).toBe('Reaching prod through its agents')
    expect(prepareView({ ...base, agents: 1 }, 'prod').what).toBe('Checking what KubeBolt may read in prod')
    const big = prepareView(loading(2000), 'prod')
    expect(big.what).toBe('Loading 5,632 ReplicaSets, the largest collection in this cluster')
    expect(big.why).toMatch(/live copy/)
    expect(big.steps.find((s) => s.key === 'load')?.detail).toBe('ReplicaSets · 2,000 of 5,632')
    expect(prepareView(loading(5632, true), 'prod').what).toBe('Running the first health check')
    expect(prepareView({ ...loading(5632, true), state: 'ready' }, 'prod').pct).toBe(100)
  })

  it('names the last types the load waits on, however small', () => {
    const types = ['Pod', 'Service', 'Node', 'Secret'].map((kind) => ({ kind, synced: kind !== 'Secret', loaded: 0, total: 0 }))
    const v = prepareView({ ...loading(0), types }, 'prod')
    expect(v.what).toBe('Loading Secrets, the last of 4 resource types')
    expect(v.steps.find((s) => s.key === 'load')?.detail).toBe('Secrets · 3 of 4 types')
    const many = types.map((t) => ({ ...t, synced: t.kind === 'Pod' }))
    expect(prepareView({ ...loading(0), types: many }, 'prod').what).toBe("Loading the cluster's resource types · 1 of 4 ready")
  })

  it('weighs the load by objects, so the big collection drives the bar', () => {
    const early = prepareView(loading(500), 'prod').pct
    const late = prepareView(loading(5000), 'prod').pct
    expect(early).toBeLessThan(35)
    expect(late).toBeGreaterThan(80)
    expect(late).toBeLessThan(100)
  })

  it('has no agent step for a cluster reached through a kubeconfig context', () => {
    const v = prepareView({ ...base, viaAgent: false }, 'prod')
    expect(v.what).toBe('Checking what KubeBolt may read in prod')
    expect(v.steps.map((s) => s.key)).toEqual(['access', 'load', 'health'])
    expect(prepareEvents(base, { ...base, viaAgent: false, agents: 0 }, 'prod')).toEqual([])
  })

  it('never moves the bar back', () => {
    expect(prepareView(loading(500), 'prod', 60).pct).toBe(60)
  })

  it('marks the steps in order', () => {
    const states = prepareView(loading(2000), 'prod').steps.map((s) => s.state)
    expect(states).toEqual(['done', 'done', 'active', 'pending'])
  })
})

describe('prepareEvents', () => {
  it('logs agents, access, loaded types, the halfway point and the end', () => {
    expect(prepareEvents(base, { ...base, agents: 2 }, 'prod')).toEqual(['2 agents connected'])
    expect(prepareEvents({ ...base, agents: 2 }, { ...base, agents: 2, accessChecked: true, readable: 29, probed: 31 }, 'prod')).toEqual([
      'Access checked · 29 of 31 resource types readable',
    ])
    const before = { ...loading(1000), types: loading(1000).types!.map((t) => (t.kind === 'Deployment' ? { ...t, synced: false, loaded: 500 } : t)) }
    expect(prepareEvents(before, loading(1000), 'prod')).toEqual(['Deployments loaded · 612 objects'])
    expect(prepareEvents(loading(2000), loading(3000), 'prod')).toEqual(['ReplicaSets halfway · 3,000 of 5,632'])
    expect(prepareEvents(loading(5000), loading(5632, true), 'prod')).toEqual(['ReplicaSets loaded · 5,632 objects', 'All 3 resource types loaded'])
    expect(prepareEvents(loading(5632, true), { ...loading(5632, true), state: 'ready' }, 'prod')).toEqual(['prod is ready'])
  })

  it('summarizes what had already loaded before the first reading', () => {
    const many: ClusterProgress = {
      ...loading(100),
      types: ['Pod', 'Service', 'Node', 'Namespace', 'Event'].map((kind) => ({ kind, synced: true, loaded: 0, total: 0 })),
    }
    expect(prepareEvents(undefined, many, 'prod')).toContain('5 resource types loaded')
  })
})

describe('etaSeconds', () => {
  it('estimates from the arrival rate, or says nothing', () => {
    const s = [
      { at: 0, loaded: 0 },
      { at: 1000, loaded: 100 },
      { at: 2000, loaded: 200 },
    ]
    expect(etaSeconds(s, 1000)).toBe(10)
    expect(etaSeconds(s.slice(0, 2), 1000)).toBeNull()
    expect(etaSeconds([{ at: 0, loaded: 5 }, { at: 1000, loaded: 5 }, { at: 2000, loaded: 5 }], 10)).toBeNull()
  })
})
