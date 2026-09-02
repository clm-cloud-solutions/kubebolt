import { describe, expect, it } from 'vitest'
import { buildMuteIndex, partitionByMutes } from './mutes'
import type { InsightMute } from '@/services/api'
import type { Insight } from '@/types/kubernetes'

// The #54 overlay contract: a mute hides its (rule, resource) from the
// default view — UNLESS the insight's current severity is critical, which
// pierces the silence (muted-but-worsening, §5).

function ins(over: Partial<Insight>): Insight {
  return {
    id: 'i1',
    ruleId: 'crash-loop',
    severity: 'warning',
    category: '',
    resource: 'Pod/ns/x',
    namespace: 'ns',
    title: 't',
    message: 'm',
    suggestion: '',
    firstSeen: '',
    lastSeen: '',
    ...over,
  }
}

function mute(over: Partial<InsightMute>): InsightMute {
  return {
    id: 'm1',
    clusterId: 'cl-1',
    ruleId: 'crash-loop',
    resource: 'Pod/ns/x',
    createdAt: '',
    untilResolved: true,
    ...over,
  }
}

describe('partitionByMutes', () => {
  it('hides the muted key and leaves the rest visible', () => {
    const index = buildMuteIndex([mute({})])
    const { visible, hidden } = partitionByMutes(
      [ins({}), ins({ id: 'i2', resource: 'Pod/ns/other' })],
      index,
    )
    expect(hidden.map((r) => r.insight.id)).toEqual(['i1'])
    expect(visible.map((r) => r.insight.id)).toEqual(['i2'])
    expect(hidden[0].mute?.id).toBe('m1')
  })

  it('critical pierces the silence, marked', () => {
    const index = buildMuteIndex([mute({})])
    const { visible, hidden } = partitionByMutes([ins({ severity: 'critical' })], index)
    expect(hidden).toHaveLength(0)
    expect(visible[0].pierced).toBe(true)
    expect(visible[0].mute?.id).toBe('m1')
  })

  it('same resource under a different rule is NOT covered', () => {
    const index = buildMuteIndex([mute({})])
    const { visible } = partitionByMutes([ins({ ruleId: 'oom-killed' })], index)
    expect(visible[0].mute).toBeUndefined()
  })

  it('an insight without ruleId (legacy payload) never matches', () => {
    const index = buildMuteIndex([mute({})])
    const { visible, hidden } = partitionByMutes([ins({ ruleId: undefined })], index)
    expect(hidden).toHaveLength(0)
    expect(visible[0].pierced).toBe(false)
  })
})
