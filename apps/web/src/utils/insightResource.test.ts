import { describe, expect, it } from 'vitest'
import { insightResourcePath } from './insightResource'

describe('insightResourcePath', () => {
  it('routes a namespaced object to its detail page', () => {
    expect(insightResourcePath('Pod/demo/image-app-7d9f-x1')).toBe('/pods/demo/image-app-7d9f-x1')
    expect(insightResourcePath('Deployment/shop/store-frontend')).toBe('/deployments/shop/store-frontend')
  })

  it('uses the _ placeholder for a cluster-scoped object', () => {
    expect(insightResourcePath('Node/worker-1')).toBe('/nodes/_/worker-1')
  })

  it('returns null for a kind without a detail page or a malformed value', () => {
    expect(insightResourcePath('HelmRelease/argocd/argo')).toBeNull()
    expect(insightResourcePath('Pod')).toBeNull()
    expect(insightResourcePath('')).toBeNull()
    expect(insightResourcePath(undefined)).toBeNull()
  })
})
