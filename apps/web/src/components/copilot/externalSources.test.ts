import { describe, it, expect } from 'vitest'
import type { CopilotMessage } from '@/services/copilot/types'
import { computeExternalSources, externalSourcesLabel } from './externalSources'

let seq = 0
function msg(partial: Partial<CopilotMessage> & Pick<CopilotMessage, 'role'>): CopilotMessage {
  seq += 1
  return { id: `m${seq}`, content: '', timestamp: new Date(0), ...partial }
}

describe('computeExternalSources', () => {
  it('attaches to the answer, not to the preamble that issued the call', () => {
    const q = msg({ role: 'user', content: 'why is checkout failing?' })
    const pre = msg({
      role: 'assistant',
      content: 'Pods look healthy; checking what checkout talks to outside the cluster.',
      toolCalls: [
        { id: 't1', name: 'get_external_connections' },
        { id: 't2', name: 'mcp__aws__call', input: { service: 'rds', operation: 'DescribeDBInstances' } },
      ],
    })
    const results = msg({
      role: 'user',
      toolResults: [
        { toolCallId: 't1', content: '{}' },
        { toolCallId: 't2', content: '{}' },
      ],
    })
    const answer = msg({ role: 'assistant', content: 'The RDS instance is in its maintenance window.' })
    const out = computeExternalSources([q, pre, results, answer])
    expect(out.get(answer.id)).toEqual(['aws'])
    expect(out.has(pre.id)).toBe(false)
  })

  it('keeps each exchange to itself', () => {
    const q1 = msg({ role: 'user', content: 'first' })
    const a1 = msg({ role: 'assistant', content: 'read AWS', toolCalls: [{ id: 'x1', name: 'mcp__aws__call' }] })
    const q2 = msg({ role: 'user', content: 'second' })
    const a2 = msg({ role: 'assistant', content: 'cluster only', toolCalls: [{ id: 'x2', name: 'get_events' }] })
    const out = computeExternalSources([q1, a1, q2, a2])
    expect(out.get(a1.id)).toEqual(['aws'])
    expect(out.has(a2.id)).toBe(false)
  })

  it('lists every connector once, in the order Kobi used them', () => {
    const q = msg({ role: 'user', content: 'q' })
    const a = msg({
      role: 'assistant',
      content: 'done',
      toolCalls: [
        { id: 'a', name: 'mcp__gcp__sqladmin_get_instance' },
        { id: 'b', name: 'mcp__aws__call' },
        { id: 'c', name: 'mcp__gcp__monitoring_list_timeseries' },
      ],
    })
    expect(computeExternalSources([q, a]).get(a.id)).toEqual(['gcp', 'aws'])
  })

  it('does not credit a call that failed', () => {
    const q = msg({ role: 'user', content: 'q' })
    const call = msg({ role: 'assistant', toolCalls: [{ id: 'f', name: 'mcp__aws__call' }] })
    const res = msg({ role: 'user', toolResults: [{ toolCallId: 'f', content: 'AccessDenied', isError: true }] })
    const answer = msg({ role: 'assistant', content: 'I could not read AWS.' })
    expect(computeExternalSources([q, call, res, answer]).has(answer.id)).toBe(false)
  })

  it('ignores KubeBolt\'s own tools, including the Autopilot built-ins', () => {
    const q = msg({ role: 'user', content: 'q' })
    const a = msg({
      role: 'assistant',
      content: 'done',
      toolCalls: [
        { id: 'k1', name: 'mcp__kubebolt__get_pod_logs' },
        { id: 'k2', name: 'mcp__kubebolt_actions__restart_workload' },
      ],
    })
    expect(computeExternalSources([q, a]).size).toBe(0)
  })

  it('has nothing to attach to while no answer text exists yet', () => {
    const q = msg({ role: 'user', content: 'q' })
    const call = msg({ role: 'assistant', toolCalls: [{ id: 'p', name: 'mcp__aws__call' }] })
    expect(computeExternalSources([q, call]).size).toBe(0)
  })
})

describe('externalSourcesLabel', () => {
  it('reads as prose', () => {
    expect(externalSourcesLabel(['aws'])).toBe('AWS')
    expect(externalSourcesLabel(['aws', 'gcp'])).toBe('AWS and Google Cloud')
    expect(externalSourcesLabel(['aws', 'gcp', 'azure'])).toBe('AWS, Google Cloud and Azure')
  })
})
