import { describe, it, expect } from 'vitest'
import { generateCopilotSuggestions } from './copilotSuggestions'
import type { ClusterOverview, Insight } from '@/types/kubernetes'

function insight(severity: 'critical' | 'warning' | 'info', resource = 'Pod/default/api'): Insight {
  return {
    id: `${severity}-${resource}`,
    severity,
    category: 'workload',
    resource,
    namespace: 'default',
    title: 'T',
    message: 'M',
    suggestion: 'S',
    firstSeen: new Date().toISOString(),
    lastSeen: new Date().toISOString(),
  } as unknown as Insight
}

describe('generateCopilotSuggestions', () => {
  it('returns defaults when no data', () => {
    const s = generateCopilotSuggestions(undefined, undefined)
    expect(Array.isArray(s)).toBe(true)
    expect(s.length).toBeGreaterThan(0)
  })

  it('singular critical insight yields a specific question', () => {
    const s = generateCopilotSuggestions(undefined, [insight('critical', 'Pod/production/api')])
    expect(s.some((x) => x.toLowerCase().includes('critical'))).toBe(true)
  })

  it('multiple critical insights use aggregate wording', () => {
    const s = generateCopilotSuggestions(undefined, [
      insight('critical', 'Pod/a/1'),
      insight('critical', 'Pod/a/2'),
    ])
    expect(s.some((x) => x.includes('2 critical'))).toBe(true)
  })

  it('caps total suggestions', () => {
    const many = Array.from({ length: 20 }, (_, i) => insight('warning', `Pod/a/${i}`))
    const s = generateCopilotSuggestions(undefined, many)
    expect(s.length).toBeLessThanOrEqual(5)
  })

  it('mixes in overview-derived prompts when insights are light', () => {
    const overview = {
      health: { status: 'warning' },
      nodes: { total: 5, ready: 4 },
      pods: { total: 100, running: 90, failed: 5, pending: 5 },
    } as unknown as ClusterOverview
    const s = generateCopilotSuggestions(overview, [])
    expect(s.length).toBeGreaterThan(0)
  })
})
