import { describe, expect, it } from 'vitest'
import { podsPathFor, workloadPathFrom } from './useWorkloadPath'

const hit = (resourceType: string, name: string, namespace = 'shop') => ({ resourceType, name, namespace, kind: '' })

describe('workloadPathFrom', () => {
  it('opens the workload with exactly that name and namespace', () => {
    const hits = [hit('pods', 'orders-api-755f4fd9b6-phpfj'), hit('statefulsets', 'orders-api'), hit('services', 'orders-api')]
    expect(workloadPathFrom(hits, 'shop', 'orders-api')).toBe('/statefulsets/shop/orders-api')
  })
  it('ignores the same name in another namespace and names that only start the same', () => {
    expect(workloadPathFrom([hit('deployments', 'orders-api', 'other'), hit('deployments', 'orders-api-v2')], 'shop', 'orders-api')).toBeNull()
  })
  it('prefers a Deployment when two kinds share the name', () => {
    expect(workloadPathFrom([hit('jobs', 'report'), hit('deployments', 'report')], 'shop', 'report')).toBe('/deployments/shop/report')
  })
})

describe('podsPathFor', () => {
  it('opens the pods list filtered to the workload', () => {
    expect(podsPathFor('shop', 'orders-api')).toBe('/pods?namespace=shop&search=orders-api')
  })
})
