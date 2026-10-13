import { describe, expect, it } from 'vitest'
import type { ClusterInfo } from '@/types/kubernetes'
import { agentDown } from './cluster'

const c = (over: Partial<ClusterInfo>): ClusterInfo =>
  ({ name: 'lab', context: 'lab', status: 'connected', source: 'agent-proxy', agentConnected: true, ...over }) as ClusterInfo

describe('agentDown', () => {
  it('flags an agent cluster whose agent is gone, even while its status still says connected', () => {
    expect(agentDown(c({ agentConnected: false }))).toBe(true)
  })
  it('does not flag a live agent or a cluster reached through a kubeconfig', () => {
    expect(agentDown(c({}))).toBe(false)
    expect(agentDown(c({ source: 'file', agentConnected: false }))).toBe(false)
  })
})
