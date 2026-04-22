import { describe, it, expect } from 'vitest'
import { buildTriggerPrompt } from './triggers'
import type {
  InsightTriggerPayload,
  NotReadyResourceTriggerPayload,
  WarningEventTriggerPayload,
} from './triggers'

describe('buildTriggerPrompt — insight', () => {
  it('includes severity, title, namespace/resource and message', () => {
    const payload: InsightTriggerPayload = {
      type: 'insight',
      insight: {
        severity: 'critical',
        title: 'Pod in CrashLoopBackOff',
        message: 'Container api restarted 12 times',
        resource: 'Pod',
        name: 'api-xyz',
        namespace: 'production',
        suggestion: 'Check logs',
        lastSeen: '2026-04-21T10:00:00Z',
      },
    }
    const out = buildTriggerPrompt(payload)
    expect(out).toContain('critical')
    expect(out).toContain('Pod in CrashLoopBackOff')
    expect(out).toContain('production/api-xyz')
    expect(out).toContain('Container api restarted 12 times')
    expect(out).toContain('Check logs')
    expect(out).toContain('Last seen: 2026-04-21T10:00:00Z')
  })

  it('handles missing optional fields gracefully', () => {
    const payload: InsightTriggerPayload = {
      type: 'insight',
      insight: {
        severity: 'warning',
        title: 'T',
        message: 'M',
      },
    }
    const out = buildTriggerPrompt(payload)
    expect(out).toContain('warning')
    expect(out).toContain('T')
    expect(out).not.toContain('Resource:') // no resource/namespace
    expect(out).not.toContain('Last seen:')
    expect(out).not.toContain('Existing suggestion:')
  })
})

describe('buildTriggerPrompt — not_ready_resource', () => {
  it('renders resource kind, namespace/name and details', () => {
    const payload: NotReadyResourceTriggerPayload = {
      type: 'not_ready_resource',
      resource: {
        kind: 'Deployment',
        namespace: 'production',
        name: 'api',
        status: 'progressing',
        details: { replicas: '0/3', condition: 'Available=false' },
      },
    }
    const out = buildTriggerPrompt(payload)
    expect(out).toContain('Deployment: production/api')
    expect(out).toContain('Status: progressing')
    expect(out).toContain('replicas: 0/3')
    expect(out).toContain('condition: Available=false')
  })

  it('filters out empty/null detail values', () => {
    const payload: NotReadyResourceTriggerPayload = {
      type: 'not_ready_resource',
      resource: {
        kind: 'Pod',
        namespace: 'default',
        name: 'p',
        details: { a: 'x', b: '', c: 'y' },
      },
    }
    const out = buildTriggerPrompt(payload)
    expect(out).toContain('a: x')
    expect(out).toContain('c: y')
    expect(out).not.toContain('b: ')
  })
})

describe('buildTriggerPrompt — warning_event', () => {
  it('renders reason, object, message, count and timestamps', () => {
    const payload: WarningEventTriggerPayload = {
      type: 'warning_event',
      event: {
        reason: 'FailedScheduling',
        message: '0/3 nodes available: insufficient memory',
        object: 'Pod/api',
        namespace: 'production',
        count: 17,
        firstSeen: '2026-04-21T09:00:00Z',
        lastSeen: '2026-04-21T10:00:00Z',
      },
    }
    const out = buildTriggerPrompt(payload)
    expect(out).toContain('Reason: FailedScheduling')
    expect(out).toContain('Object: production/Pod/api')
    expect(out).toContain('0/3 nodes available')
    expect(out).toContain('Count: 17 occurrences')
    expect(out).toContain('First seen')
    expect(out).toContain('Last seen')
  })

  it('omits count when undefined', () => {
    const payload: WarningEventTriggerPayload = {
      type: 'warning_event',
      event: {
        reason: 'NodeNotReady',
        message: 'node down',
        object: 'Node/n1',
      },
    }
    const out = buildTriggerPrompt(payload)
    expect(out).not.toContain('Count:')
    expect(out).toContain('Object: Node/n1')
  })
})

describe('buildTriggerPrompt — ends with actionable CTA', () => {
  it('every trigger type ends with a concrete instruction or question', () => {
    const payloads = [
      { type: 'insight', insight: { severity: 'info', title: 't', message: 'm' } },
      { type: 'not_ready_resource', resource: { kind: 'Pod', namespace: 'd', name: 'p' } },
      { type: 'warning_event', event: { reason: 'r', message: 'm', object: 'Pod/p' } },
    ] as const
    for (const p of payloads) {
      const out = buildTriggerPrompt(p).trimEnd()
      // Either a question or an actionable imperative — never hanging mid-sentence.
      expect(out).toMatch(/[?.]$/)
      expect(out.length).toBeGreaterThan(20)
    }
  })
})
