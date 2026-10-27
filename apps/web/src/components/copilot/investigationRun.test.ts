import { describe, it, expect } from 'vitest'
import {
  cloudBoundaries,
  cloudScope,
  couldNotRead,
  formatClock,
  guideLabel,
  investigationRuns,
  liveInvestigation,
  turnEndings,
} from './investigationRun'
import type { CopilotMessage } from '@/services/copilot/types'

const at = (s: number) => new Date(Date.UTC(2026, 9, 26, 10, 0, s))
let seq = 0
const msg = (m: Partial<CopilotMessage> & Pick<CopilotMessage, 'role'>): CopilotMessage => ({
  id: `m${seq++}`,
  content: '',
  timestamp: at(0),
  ...m,
})
const call = (id: string, name: string, input: Record<string, unknown> = {}) => ({ id, name, input })
const res = (id: string, status?: string, isError?: boolean) => ({ toolCallId: id, content: '{}', status, isError })

// The nat-route-removed run, shortened: cluster reads, a guide, AWS reads (one
// refused, one never sent), then the answer.
function run(): CopilotMessage[] {
  return [
    msg({ id: 'q', role: 'user', content: 'why is checkout failing?', timestamp: at(0) }),
    msg({ id: 'a0', role: 'assistant', content: 'Checking the pods.', toolCalls: [call('c1', 'get_workload_pods'), call('c2', 'get_events')] }),
    msg({ id: 'u0', role: 'user', toolResults: [res('c1', 'ok'), res('c2', 'empty')] }),
    msg({
      id: 'a1',
      role: 'assistant',
      toolCalls: [
        call('g1', 'load_skill', { name: 'aws-investigation' }),
        call('w1', 'mcp__aws__call', { service: 'ec2', operation: 'DescribeRouteTables', region: 'us-east-2' }),
        call('w2', 'mcp__aws__call', { service: 'kms', operation: 'DescribeKey' }),
      ],
    }),
    msg({ id: 'u1', role: 'user', toolResults: [res('g1', 'ok'), res('w1', 'ok'), res('w2', 'denied', true)] }),
    msg({ id: 'a2', role: 'assistant', toolCalls: [call('w3', 'mcp__aws__call', { service: 's3', operation: 'GetBucketPolicy' })] }),
    msg({ id: 'u2', role: 'user', toolResults: [res('w3', 'not_sent', true)] }),
    msg({ id: 'ans', role: 'assistant', content: 'The NAT route is gone.', turnId: 't1', timestamp: at(72) }),
  ]
}

describe('investigationRuns', () => {
  it('sums up a finished run and folds its steps above the answer', () => {
    const r = investigationRuns(run()).get('ans')!
    expect(r).toMatchObject({ reads: 5, guides: 1, unread: 2, elapsedMs: 72_000 })
    expect(r.byCloud).toEqual([['aws', 3]])
    expect(r.stepIds).toEqual(['a0', 'u0', 'a1', 'u1', 'a2', 'u2'])
  })

  it('leaves a run without its turn id unfolded: still running, stopped or failed', () => {
    const live = run()
    live[live.length - 1] = { ...live[live.length - 1], turnId: undefined }
    expect(investigationRuns(live).size).toBe(0)
  })

  it('does not summarise a plain answer', () => {
    const plain = [
      msg({ id: 'q', role: 'user', content: 'hi' }),
      msg({ id: 'a', role: 'assistant', content: 'Hello.', turnId: 't' }),
    ]
    expect(investigationRuns(plain).size).toBe(0)
  })

  it('keeps proposals and notices out of the fold', () => {
    const m = run()
    m.splice(2, 0, msg({ id: 'n', role: 'system', kind: 'compact-notice' }))
    m.push(msg({ id: 'q2', role: 'user', content: 'restart it' }))
    m.push(msg({ id: 'p0', role: 'assistant', toolCalls: [call('p', 'propose_restart_workload'), call('c9', 'get_events')] }))
    m.push(
      msg({ id: 'pu', role: 'user', toolResults: [{ toolCallId: 'p', content: '{"kind":"action_proposal"}' }, res('c9', 'ok')] }),
    )
    m.push(msg({ id: 'ans2', role: 'assistant', content: 'Proposed.', turnId: 't2' }))
    const runs = investigationRuns(m)
    expect(runs.get('ans')!.stepIds).not.toContain('n')
    expect(runs.get('ans2')).toMatchObject({ reads: 1, stepIds: ['p0'], proposals: 1, proposalIds: ['pu'] })
  })

  it('counts the charts the answer brings', () => {
    const metrics = (trend: number[]) =>
      JSON.stringify({
        workload: { kind: 'Deployment', namespace: 'shop', name: 'orders' },
        metrics: { cpu: { trend: trend.map((v, i) => ({ t: i, v })) } },
      })
    const m = run()
    m.splice(
      2,
      1,
      msg({ id: 'u0', role: 'user', toolResults: [res('c1', 'ok'), res('c2', 'empty')] }),
      msg({ id: 'am', role: 'assistant', content: 'Its CPU.', toolCalls: [call('m1', 'get_workload_metrics'), call('m2', 'get_workload_metrics')] }),
      msg({
        id: 'um',
        role: 'user',
        toolResults: [
          { toolCallId: 'm1', content: metrics([0.2, 0.3]), status: 'ok' },
          { toolCallId: 'm2', content: metrics([]), status: 'ok' },
        ],
      }),
    )
    expect(investigationRuns(m).get('ans')).toMatchObject({ reads: 7, charts: 1 })
  })

  it('says the run paused at the step limit while its notice is there', () => {
    const m = run()
    m.push(msg({ id: 'limit', role: 'system', kind: 'maxrounds-notice', maxRoundsLimit: 20 }))
    expect(investigationRuns(m).get('ans')!.pausedAtLimit).toBe(20)
    expect(investigationRuns(run()).get('ans')!.pausedAtLimit).toBeUndefined()
  })
})

describe('turnEndings', () => {
  it('says which unfinished question was stopped and which failed', () => {
    const stopped = run().slice(0, 4)
    stopped.push(msg({ id: 'end', role: 'assistant', cancelled: true }))
    const failed = [
      msg({ id: 'q2', role: 'user', content: 'and the database?' }),
      msg({ id: 'b0', role: 'assistant', toolCalls: [call('d1', 'get_events')] }),
      msg({ id: 'b1', role: 'assistant', failure: { message: 'The AI provider is overloaded.' } }),
    ]
    const endings = turnEndings([...stopped, ...failed])
    expect(endings.get('a1')).toBe('stopped')
    expect(endings.get('b0')).toBe('failed')
    expect(turnEndings(run()).size).toBe(0)
  })
})

describe('cloudBoundaries', () => {
  it('marks the first read of each connector in each question', () => {
    const m = run()
    m.push(msg({ id: 'q2', role: 'user', content: 'and the bucket?' }))
    m.push(msg({ id: 'b0', role: 'assistant', toolCalls: [call('w9', 'mcp__aws__call', { service: 's3', operation: 'GetBucketPolicy' })] }))
    expect([...cloudBoundaries(m)]).toEqual([
      ['w1', 'aws'],
      ['w9', 'aws'],
    ])
  })
})

describe('helpers', () => {
  it('says where a call went', () => {
    expect(cloudScope('aws', { region: 'eu-west-1' }, { region: 'us-east-2' })).toBe('eu-west-1')
    expect(cloudScope('aws', {}, { region: 'us-east-2' })).toBe('us-east-2')
    expect(cloudScope('aws', {})).toBe('')
    expect(cloudScope('gcp', { parent: 'projects/tienda-lab/locations/us-central1' })).toBe('tienda-lab')
    expect(cloudScope('azure', {}, { subscriptionId: '00000000-1111-2222-3333-444444444444' })).toBe('subscription 00000000…')
  })

  it('counts as unread only what came back without an answer', () => {
    expect(couldNotRead(res('x', 'empty'))).toBe(false)
    expect(couldNotRead(res('x', 'not_found'))).toBe(false)
    expect(couldNotRead(res('x', 'timeout'))).toBe(true)
    expect(couldNotRead(res('x', undefined, true))).toBe(true)
    expect(couldNotRead(undefined)).toBe(false)
  })

  it('formats a clock', () => {
    expect(formatClock(7_400)).toBe('0:07')
    expect(formatClock(72_000)).toBe('1:12')
  })
})

describe('the question in flight', () => {
  it('counts its reads and says which cloud is being read now', () => {
    const m = run().slice(0, 6) // up to a2: w3 (S3) has no result yet
    const live = liveInvestigation(m)!
    expect(live.startedAt).toEqual(at(0))
    expect(live.reads).toBe(5)
    expect(live.byCloud).toEqual([['aws', 3]])
    expect(live.cloudsNow).toEqual(['aws'])
    expect(liveInvestigation(run().slice(0, 5))!.cloudsNow).toEqual([])
  })

  it('has nothing to say before the first question', () => {
    expect(liveInvestigation([])).toBeUndefined()
  })
})

describe('guideLabel', () => {
  it('names the skill, or the Azure command group listed', () => {
    expect(guideLabel(call('g', 'load_skill', { name: 'aws-investigation' }))).toBe('aws-investigation')
    expect(guideLabel(call('g', 'mcp__azure__monitor', { learn: true }))).toBe('Azure monitor commands')
  })
})
