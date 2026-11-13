import { describe, expect, it } from 'vitest'
import { clusterSwitchTarget } from './useOpenInCluster'
import type { ClusterInfo } from '@/types/kubernetes'

// Finding #81: a Home link that names another cluster switches to it first.
// Which cluster that is — and when no switch is needed — is decided here.

const clusters = [
  { name: 'a', context: 'agent:uid-a', clusterId: 'uid-a', active: true, status: 'connected' },
  { name: 'b', context: 'agent:uid-b', clusterId: 'uid-b', active: false, status: 'connected' },
  { name: 'in-cluster', context: 'in-cluster', clusterId: 'uid-self', active: false, status: 'connected' },
] as unknown as ClusterInfo[]

describe('clusterSwitchTarget', () => {
  it('finds the cluster by UID (episodes) or by context name (Kobi)', () => {
    expect(clusterSwitchTarget(clusters, 'uid-b')?.context).toBe('agent:uid-b')
    expect(clusterSwitchTarget(clusters, 'agent:uid-b')?.context).toBe('agent:uid-b')
    // A direct context keyed by its UID still resolves to the context to switch to.
    expect(clusterSwitchTarget(clusters, 'uid-self')?.context).toBe('in-cluster')
    expect(clusterSwitchTarget(clusters, 'in-cluster')?.context).toBe('in-cluster')
  })

  it('needs no switch for the active cluster', () => {
    expect(clusterSwitchTarget(clusters, 'uid-a')).toBeNull()
    expect(clusterSwitchTarget(clusters, 'agent:uid-a')).toBeNull()
  })

  it('needs no switch (cannot switch) for a cluster no longer registered, or none named', () => {
    expect(clusterSwitchTarget(clusters, 'uid-gone')).toBeNull()
    expect(clusterSwitchTarget(clusters, undefined)).toBeNull()
    expect(clusterSwitchTarget(undefined, 'uid-b')).toBeNull()
  })
})
