import { describe, it, expect } from 'vitest'
import { computeMetricChartAttachments } from './metricChartAttachment'
import type { CopilotMessage, WorkloadMetricsResponse } from '@/services/copilot/types'

// Minimal renderable payload — workloadMetricsHasRenderableData requires
// podsResolved > 0 and at least one metric with a non-empty trend. We
// pin a memory metric with two points; CPU/Network are exercised by the
// KobiMetricChartCard tests, not the attachment logic.
function makeMetricsResponse(): WorkloadMetricsResponse {
  return {
    workload: { kind: 'Deployment', namespace: 'autopilot-demo', name: 'oomy-app' },
    range: '1h',
    end: '2026-05-24T05:29:00Z',
    podsResolved: 1,
    metrics: {
      memory: {
        unit: 'bytes',
        summary: { min: 73400320, avg: 87740416, max: 87740416, p95: 87740416 },
        trend: [
          { t: '2026-05-24T05:15:00Z', v: 87740416 },
          { t: '2026-05-24T05:25:00Z', v: 87740416 },
        ],
        request: 33554432,
        limit: 134217728,
        utilizationPercent: { vsRequest: 261, vsLimit: 65 },
      },
    },
  }
}

// All test messages carry timestamps because the type requires them; the
// attachment logic ignores them, so an epoch zero value is fine.
const EPOCH = new Date(0)

function msg(partial: Omit<CopilotMessage, 'timestamp'>): CopilotMessage {
  return { timestamp: EPOCH, ...partial }
}

describe('computeMetricChartAttachments', () => {
  it('attaches the chart to the analysis turn, NOT the same turn that issued the tool call', () => {
    // Regression guard for the bug fixed alongside this test (see
    // metricChartAttachment.ts header comment). Claude bundles the
    // "I'll check this" text + tool_call into a single assistant message;
    // the chart must attach to the SUBSEQUENT analysis message so the
    // chart sits next to the prose that interprets it, not above the
    // preamble.
    const metricsJson = JSON.stringify(makeMetricsResponse())
    const messages: CopilotMessage[] = [
      msg({ id: 'u1', role: 'user', content: 'Por qué oomy-app consume tanta ram?' }),
      msg({
        id: 'a1',
        role: 'assistant',
        content: 'Investigando oomy-app en autopilot-demo.',
        toolCalls: [
          { id: 'tc1', name: 'get_workload_metrics', input: { kind: 'Deployment', namespace: 'autopilot-demo', name: 'oomy-app' } },
        ],
      }),
      msg({
        id: 'u2',
        role: 'user',
        content: '',
        toolResults: [{ toolCallId: 'tc1', content: metricsJson }],
      }),
      msg({
        id: 'a2',
        role: 'assistant',
        content: 'El contenedor está consumiendo 84 MiB de 128 MiB...',
      }),
    ]

    const attachments = computeMetricChartAttachments(messages)

    // The same-turn case must NOT attach — this is the bug we are guarding.
    expect(attachments.has('a1')).toBe(false)

    // The following analysis turn IS the correct attachment site.
    expect(attachments.has('a2')).toBe(true)
    expect(attachments.get('a2')).toHaveLength(1)
    expect(attachments.get('a2')![0].id).toBe('tc1')
    expect(attachments.get('a2')![0].data.workload.name).toBe('oomy-app')
  })

  it('attaches the chart to the next text turn when the tool_call ships in a text-less assistant message', () => {
    const messages: CopilotMessage[] = [
      msg({ id: 'u1', role: 'user', content: 'memoria de oomy-app?' }),
      msg({
        id: 'a1',
        role: 'assistant',
        content: '',
        toolCalls: [{ id: 'tc1', name: 'get_workload_metrics', input: {} }],
      }),
      msg({
        id: 'u2',
        role: 'user',
        content: '',
        toolResults: [{ toolCallId: 'tc1', content: JSON.stringify(makeMetricsResponse()) }],
      }),
      msg({ id: 'a2', role: 'assistant', content: 'Análisis aquí...' }),
    ]

    const attachments = computeMetricChartAttachments(messages)
    expect(attachments.has('a1')).toBe(false)
    expect(attachments.get('a2')).toHaveLength(1)
    expect(attachments.get('a2')![0].id).toBe('tc1')
  })

  it('claims each call id once even when multiple text turns follow in the same block', () => {
    // If the LLM produces two analysis paragraphs as separate turns, the
    // chart attaches to the first and the second turn gets no duplicate.
    const messages: CopilotMessage[] = [
      msg({ id: 'u1', role: 'user', content: 'why?' }),
      msg({
        id: 'a1',
        role: 'assistant',
        content: '',
        toolCalls: [{ id: 'tc1', name: 'get_workload_metrics', input: {} }],
      }),
      msg({
        id: 'u2',
        role: 'user',
        content: '',
        toolResults: [{ toolCallId: 'tc1', content: JSON.stringify(makeMetricsResponse()) }],
      }),
      msg({ id: 'a2', role: 'assistant', content: 'First analysis paragraph.' }),
      msg({ id: 'a3', role: 'assistant', content: 'Continuing analysis.' }),
    ]

    const attachments = computeMetricChartAttachments(messages)
    expect(attachments.get('a2')).toHaveLength(1)
    expect(attachments.has('a3')).toBe(false)
  })

  it('does not cross conversation block boundaries', () => {
    // A second user-content message starts a new block; the metric from
    // block 1 must not bleed into block 2's analysis turn.
    const messages: CopilotMessage[] = [
      msg({ id: 'u1', role: 'user', content: 'first question' }),
      msg({
        id: 'a1',
        role: 'assistant',
        content: '',
        toolCalls: [{ id: 'tc1', name: 'get_workload_metrics', input: {} }],
      }),
      msg({
        id: 'u2',
        role: 'user',
        content: '',
        toolResults: [{ toolCallId: 'tc1', content: JSON.stringify(makeMetricsResponse()) }],
      }),
      msg({ id: 'a2', role: 'assistant', content: 'First analysis.' }),
      msg({ id: 'u3', role: 'user', content: 'unrelated follow-up' }),
      msg({ id: 'a3', role: 'assistant', content: 'New topic answer.' }),
    ]

    const attachments = computeMetricChartAttachments(messages)
    expect(attachments.get('a2')).toHaveLength(1)
    expect(attachments.has('a3')).toBe(false)
  })

  it('skips tool results marked isError', () => {
    const messages: CopilotMessage[] = [
      msg({ id: 'u1', role: 'user', content: 'memory?' }),
      msg({
        id: 'a1',
        role: 'assistant',
        content: 'Checking',
        toolCalls: [{ id: 'tc1', name: 'get_workload_metrics', input: {} }],
      }),
      msg({
        id: 'u2',
        role: 'user',
        content: '',
        toolResults: [{ toolCallId: 'tc1', content: 'agent unavailable', isError: true }],
      }),
      msg({ id: 'a2', role: 'assistant', content: 'No data available.' }),
    ]

    const attachments = computeMetricChartAttachments(messages)
    expect(attachments.size).toBe(0)
  })

  it('ignores non-metric tool calls', () => {
    const messages: CopilotMessage[] = [
      msg({ id: 'u1', role: 'user', content: 'list pods' }),
      msg({
        id: 'a1',
        role: 'assistant',
        content: 'Listing.',
        toolCalls: [{ id: 'tc1', name: 'list_resources', input: {} }],
      }),
      msg({
        id: 'u2',
        role: 'user',
        content: '',
        toolResults: [{ toolCallId: 'tc1', content: '[]' }],
      }),
      msg({ id: 'a2', role: 'assistant', content: 'No pods.' }),
    ]

    const attachments = computeMetricChartAttachments(messages)
    expect(attachments.size).toBe(0)
  })

  it('handles multiple metric calls across separate text turns within the same block', () => {
    // Two separate get_workload_metrics calls with their own analysis turns.
    // Each chart attaches to the next text turn following its call.
    const messages: CopilotMessage[] = [
      msg({ id: 'u1', role: 'user', content: 'memory across two workloads' }),
      msg({
        id: 'a1',
        role: 'assistant',
        content: 'Checking the first one.',
        toolCalls: [{ id: 'tc1', name: 'get_workload_metrics', input: {} }],
      }),
      msg({
        id: 'u2',
        role: 'user',
        content: '',
        toolResults: [{ toolCallId: 'tc1', content: JSON.stringify(makeMetricsResponse()) }],
      }),
      msg({
        id: 'a2',
        role: 'assistant',
        content: 'First workload analysis. Now the second one.',
        toolCalls: [{ id: 'tc2', name: 'get_workload_metrics', input: {} }],
      }),
      msg({
        id: 'u3',
        role: 'user',
        content: '',
        toolResults: [{ toolCallId: 'tc2', content: JSON.stringify(makeMetricsResponse()) }],
      }),
      msg({ id: 'a3', role: 'assistant', content: 'Second workload analysis.' }),
    ]

    const attachments = computeMetricChartAttachments(messages)
    // First chart on a2 (tc1 from a1).
    expect(attachments.get('a2')).toHaveLength(1)
    expect(attachments.get('a2')![0].id).toBe('tc1')
    // Second chart on a3 (tc2 from a2). a2 issued tc2 in the same turn, so
    // tc2 does NOT attach to a2 — same-turn rule.
    expect(attachments.get('a3')).toHaveLength(1)
    expect(attachments.get('a3')![0].id).toBe('tc2')
  })
})
