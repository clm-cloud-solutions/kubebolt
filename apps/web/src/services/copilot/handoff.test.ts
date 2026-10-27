import { describe, expect, it } from 'vitest'
import type { ClusterInfo } from '@/types/kubernetes'
import { handoffOrigin, handoffPrefix } from './handoff'

const clusters = [
  { name: 'agent:uid-front', context: 'agent:uid-front', displayName: 'kubeshop-front' },
  { name: 'kind-kubebolt-lab', context: 'kind-kubebolt-lab' },
] as ClusterInfo[]

describe('handoffPrefix', () => {
  // The real case (2026-09-25): a switch from kubeshop-front opened
  // "(From the fleet view: …)" on kubeshop-back, and that Kobi offered to
  // switch to kubeshop-back — the cluster it was on.
  it('names the cluster the switch came from', () => {
    expect(handoffPrefix('orders-api answers 500', 'kubeshop-front')).toBe(
      '(From cluster kubeshop-front: orders-api answers 500)\n\n',
    )
  })

  it('keeps the fleet wording for a switch from a global page', () => {
    expect(handoffPrefix('2 criticals on prod', null)).toBe('(From the fleet view: 2 criticals on prod)\n\n')
  })

  it('adds nothing when nothing was established', () => {
    expect(handoffPrefix('', 'kubeshop-front')).toBe('')
  })
})

describe('handoffOrigin', () => {
  it('is the display name of the active cluster on a cluster page', () => {
    expect(handoffOrigin('cluster', 'agent:uid-front', clusters)).toBe('kubeshop-front')
  })

  it('falls back to the name, then the context', () => {
    expect(handoffOrigin('cluster', 'kind-kubebolt-lab', clusters)).toBe('kind-kubebolt-lab')
    expect(handoffOrigin('cluster', 'agent:unknown', clusters)).toBe('agent:unknown')
  })

  it('is null from a global page, whatever cluster stays selected', () => {
    expect(handoffOrigin('global', 'agent:uid-front', clusters)).toBeNull()
  })

  it('is null with no cluster selected', () => {
    expect(handoffOrigin('cluster', null, clusters)).toBeNull()
  })
})
