import { describe, expect, it } from 'vitest'
import type { KubeEvent } from '@/types/kubernetes'
import { groupEvents, summarize } from './eventGroups'

const ev = (over: Partial<KubeEvent>): KubeEvent => ({
  type: 'Normal',
  reason: 'Pulled',
  message: '',
  object: 'Pod/a',
  namespace: 'demo',
  timestamp: '2026-10-12T10:00:00Z',
  count: 1,
  ...over,
})

describe('groupEvents', () => {
  it('folds the events of one object into one group led by its newest warning', () => {
    const groups = groupEvents([
      ev({ reason: 'Pulling', message: 'Pulling image "nope:1"', timestamp: '2026-10-12T10:00:03Z' }),
      ev({ type: 'Warning', reason: 'Failed', message: 'Error: ErrImagePull', timestamp: '2026-10-12T10:00:02Z' }),
      ev({ type: 'Warning', reason: 'BackOff', message: 'Back-off pulling image "nope:1"', timestamp: '2026-10-12T10:00:01Z' }),
      ev({ object: 'Pod/b', reason: 'Started', timestamp: '2026-10-12T09:59:00Z' }),
    ])
    expect(groups).toHaveLength(2)
    expect(groups[0].object).toBe('Pod/a')
    expect(groups[0].type).toBe('Warning')
    expect(groups[0].events).toHaveLength(3)
    expect(groups[0].lead.reason).toBe('Failed')
    expect(groups[0].summary).toBe('Image pull failing')
    expect(groups[1].summary).toBeNull()
  })

  it('keeps two pods apart even when their names look like one workload', () => {
    const groups = groupEvents([ev({ object: 'Pod/app-7d9f-x1' }), ev({ object: 'Pod/app-7d9f-x2' })])
    expect(groups).toHaveLength(2)
  })

  it('keeps the same name in two namespaces apart', () => {
    expect(groupEvents([ev({ namespace: 'a' }), ev({ namespace: 'b' })])).toHaveLength(2)
  })

  it('orders groups by their newest event', () => {
    const groups = groupEvents([
      ev({ object: 'Pod/old', timestamp: '2026-10-12T09:00:00Z' }),
      ev({ object: 'Pod/new', timestamp: '2026-10-12T11:00:00Z' }),
    ])
    expect(groups.map((g) => g.object)).toEqual(['Pod/new', 'Pod/old'])
  })
})

describe('summarize', () => {
  const w = (reason: string, message = '') => ev({ type: 'Warning', reason, message })
  it('names the common failures', () => {
    expect(summarize([w('BackOff', 'Back-off restarting failed container app')])).toBe('Container crash-looping')
    expect(summarize([w('FailedScheduling', '0/2 nodes are available')])).toBe("Can't be scheduled")
    expect(summarize([w('Unhealthy', 'Readiness probe failed: 503')])).toBe('Readiness probe failing')
    expect(summarize([w('FailedMount', 'MountVolume.SetUp failed')])).toBe("Volume can't be mounted")
  })
  it('leaves the rest to the event message', () => {
    expect(summarize([w('SomethingNew', 'x')])).toBeNull()
    expect(summarize([])).toBeNull()
  })
})
