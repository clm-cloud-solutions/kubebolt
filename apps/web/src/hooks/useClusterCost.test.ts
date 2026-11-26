import { describe, it, expect } from 'vitest'
import { estimateMonthlySavings, HOURS_PER_MONTH, type NodeRates } from './useClusterCost'

// Rates captured from the live kind cluster's OpenCost custom pricing
// (node_cpu_hourly_cost / node_ram_hourly_cost) so the assertions are
// grounded in a real scenario, not invented numbers.
const KIND_RATES: NodeRates = {
  cpuCoreHourly: 0.03161,
  ramGiBHourly: 0.00424,
  available: true,
}

const GIB = 1024 * 1024 * 1024

describe('HOURS_PER_MONTH', () => {
  it('is OpenCost’s 730 (365×24/12)', () => {
    expect(HOURS_PER_MONTH).toBe(730)
  })
})

describe('estimateMonthlySavings', () => {
  it('prices reclaimable CPU at the cluster’s core-hour rate × 730', () => {
    // 1 core (1000m) reclaimed → 1 × 0.03161 × 730 ≈ $23.08/mo
    const s = estimateMonthlySavings(1000, 0, KIND_RATES)
    expect(s).toBeCloseTo(0.03161 * HOURS_PER_MONTH, 5)
    expect(s).toBeGreaterThan(23)
    expect(s).toBeLessThan(24)
  })

  it('prices reclaimable memory at the GiB-hour rate × 730', () => {
    // 1 GiB reclaimed → 1 × 0.00424 × 730 ≈ $3.10/mo
    const s = estimateMonthlySavings(0, GIB, KIND_RATES)
    expect(s).toBeCloseTo(0.00424 * HOURS_PER_MONTH, 5)
  })

  it('sums CPU and memory contributions', () => {
    const cpuOnly = estimateMonthlySavings(2000, 0, KIND_RATES)
    const memOnly = estimateMonthlySavings(0, 4 * GIB, KIND_RATES)
    const both = estimateMonthlySavings(2000, 4 * GIB, KIND_RATES)
    expect(both).toBeCloseTo(cpuOnly + memOnly, 6)
  })

  it('returns 0 when node rates are unavailable (no OpenCost)', () => {
    const noRates: NodeRates = { cpuCoreHourly: 0, ramGiBHourly: 0, available: false }
    expect(estimateMonthlySavings(4000, 8 * GIB, noRates)).toBe(0)
  })

  it('returns 0 when there is nothing to reclaim', () => {
    expect(estimateMonthlySavings(0, 0, KIND_RATES)).toBe(0)
  })
})

// OpenCost's disk and network families are prices per GiB, not costs: a
// sum of them is not money. These pin the shapes that price them right.
describe('the cost queries beyond the nodes', () => {
  it('prices each volume at its size, one series per volume', async () => {
    const { STORAGE_COST_QUERY } = await import('./useClusterCost')
    expect(STORAGE_COST_QUERY).toMatch(/max by \(persistentvolume\) \(pv_hourly_cost\)\s+\* on \(persistentvolume\)/)
    expect(STORAGE_COST_QUERY).toContain('kube_persistentvolume_capacity_bytes')
    expect(STORAGE_COST_QUERY).toContain('or max by (persistentvolume) (pod_pvc_allocation)')
    expect(STORAGE_COST_QUERY).toContain('/ 1024 / 1024 / 1024')
  })

  it('never adds the network egress prices up as an hourly cost', async () => {
    const mod = await import('./useClusterCost')
    for (const q of [mod.STORAGE_COST_QUERY, mod.LOAD_BALANCER_COST_QUERY, mod.MANAGEMENT_COST_QUERY]) {
      expect(q).not.toMatch(/kubecost_network_/)
    }
    expect(mod.LOAD_BALANCER_COST_QUERY).toBe('sum(max by (namespace, service_name) (kubecost_load_balancer_cost))')
  })
})
