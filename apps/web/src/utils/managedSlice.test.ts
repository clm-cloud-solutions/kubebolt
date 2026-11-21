import { describe, expect, it } from 'vitest'
import { managedSliceOf } from './managedSlice'

const slice = (labels: string) => `addressType: IPv4
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  labels:
${labels}
  name: shop-db-qm99s
  namespace: shop-data
`

describe('managedSliceOf', () => {
  it('says a mirrored slice is edited through its Endpoints', () => {
    const y = slice('    endpointslice.kubernetes.io/managed-by: endpointslicemirroring-controller.k8s.io\n    kubernetes.io/service-name: shop-db')
    expect(managedSliceOf(y)).toEqual({ source: 'endpoints', service: 'shop-db' })
  })

  it("says a selector's slice is changed through its Service", () => {
    const y = slice("    endpointslice.kubernetes.io/managed-by: 'endpointslice-controller.k8s.io'\n    kubernetes.io/service-name: checkout")
    expect(managedSliceOf(y)).toEqual({ source: 'service', service: 'checkout' })
  })

  it('leaves a slice nobody rewrites editable', () => {
    expect(managedSliceOf(slice('    kubernetes.io/service-name: shop-db'))).toBeNull()
    expect(managedSliceOf(slice('    endpointslice.kubernetes.io/managed-by: my-operator'))).toBeNull()
  })
})
