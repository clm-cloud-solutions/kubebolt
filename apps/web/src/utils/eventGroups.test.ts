import { describe, expect, it } from 'vitest'
import type { KubeEvent } from '@/types/kubernetes'
import { groupEvents, rolloutOf, summarize } from './eventGroups'

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

describe('rollouts', () => {
  const scaled = (message: string, timestamp: string) =>
    ev({ object: 'Deployment/orders-api', namespace: 'shop', reason: 'ScalingReplicaSet', message, timestamp })
  const rollout = [
    scaled('Scaled down replica set orders-api-6b8f9c7d5 to 0 from 1', '2026-10-12T11:49:30Z'),
    scaled('Scaled up replica set orders-api-755f4fd9b6 to 1', '2026-10-12T11:49:00Z'),
  ]
  const crash = (pod: string, timestamp: string) =>
    ev({ object: `Pod/${pod}`, namespace: 'shop', type: 'Warning', reason: 'BackOff', message: 'Back-off restarting failed container app', timestamp })

  it('reads two ReplicaSets scaled on one Deployment as a rollout of the new one', () => {
    expect(rolloutOf(rollout)).toMatchObject({ replicaSet: 'orders-api-755f4fd9b6', at: '2026-10-12T11:49:00Z' })
  })

  it('does not read scaling one ReplicaSet as a rollout', () => {
    expect(rolloutOf([scaled('Scaled up replica set orders-api-755f4fd9b6 to 3', '2026-10-12T11:49:00Z')])).toBeNull()
  })

  it('reads a scale-up right after the Deployment created a ReplicaSet as a rollout, even with the old one still up', () => {
    // The new pods never get ready, so the old ReplicaSet is never scaled down.
    const stuck = [scaled('Scaled up replica set orders-api-755f4fd9b6 from 0 to 1', '2026-10-12T11:49:02Z')]
    const deploys = [{ namespace: 'shop', name: 'orders-api', deployedAt: '2026-10-12T11:49:00Z' }]
    expect(rolloutOf(stuck, deploys)).toMatchObject({ replicaSet: 'orders-api-755f4fd9b6' })
  })

  it('does not read waking an old ReplicaSet from zero as a rollout', () => {
    const wake = [scaled('Scaled up replica set orders-api-755f4fd9b6 from 0 to 1', '2026-10-12T11:49:02Z')]
    const deploys = [{ namespace: 'shop', name: 'orders-api', deployedAt: '2026-08-01T09:00:00Z' }]
    expect(rolloutOf(wake, deploys)).toBeNull()
  })

  it('links the first pod of the new ReplicaSet that fails soon after', () => {
    const groups = groupEvents([...rollout, crash('orders-api-755f4fd9b6-phpfj', '2026-10-12T11:51:00Z')])
    const r = groups.find((g) => g.object === 'Deployment/orders-api')?.rollout
    expect(r?.then).toEqual({ summary: 'Container crash-looping', afterS: 120 })
  })

  it('leaves out pods of another ReplicaSet, failures before the rollout and failures long after', () => {
    for (const other of [
      crash('orders-api-6b8f9c7d5-x8k2p', '2026-10-12T11:51:00Z'),
      crash('orders-api-755f4fd9b6-phpfj', '2026-10-12T11:40:00Z'),
      crash('orders-api-755f4fd9b6-phpfj', '2026-10-12T12:30:00Z'),
    ]) {
      const r = groupEvents([...rollout, other]).find((g) => g.object === 'Deployment/orders-api')?.rollout
      expect(r?.then).toBeNull()
    }
  })
})
