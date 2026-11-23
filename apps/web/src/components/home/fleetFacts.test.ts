import { describe, it, expect } from 'vitest'
import { facts } from './FleetBreakdown'
import { buildRows } from './fleetGroups'
import type { ClusterInfo } from '@/types/kubernetes'

const twoDaysAgo = new Date(Date.now() - 2 * 86400_000).toISOString()

function row(over: Partial<ClusterInfo>) {
  const c = { name: 'a', context: 'a', server: 'https://x', active: false, status: 'connected', source: 'agent-proxy', ...over } as ClusterInfo
  return buildRows([c], {})[0]
}

describe('facts: el latido del agente', () => {
  it('un cluster conectado dice desde cuándo, no «visto hace»', () => {
    expect(facts(row({ agentConnected: true, connectedSince: twoDaysAgo }))).toContain('live · connected 2d')
  })

  it('conectado sin fecha de registro dice sólo live', () => {
    expect(facts(row({ agentConnected: true }))).toMatch(/(^|· )live$/)
  })

  it('desconectado dice cuándo se le vio por última vez', () => {
    expect(facts(row({ agentConnected: false, lastSeen: twoDaysAgo }))).toContain('seen 2d ago')
  })
})
