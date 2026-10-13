import { describe, expect, it } from 'vitest'
import type { Insight } from '@/types/kubernetes'
import { caseRows, NODE_RULES, openCases, podWorkload, POD_RULES } from './insightCases'

const ins = (over: Partial<Insight>): Insight => ({
  id: Math.random().toString(36).slice(2),
  ruleId: 'crash-loop',
  severity: 'critical',
  category: 'workload',
  resource: 'Pod/shop/orders-api-755f4fd9b6-phpfj',
  namespace: 'shop',
  title: '',
  message: '',
  suggestion: '',
  firstSeen: '2026-10-12T11:51:00Z',
  lastSeen: '2026-10-12T12:20:00Z',
  ...over,
})

const NOW = Date.parse('2026-10-12T12:30:00Z')

describe('podWorkload', () => {
  it('strips a Deployment pod to its workload', () => {
    expect(podWorkload('orders-api-755f4fd9b6-phpfj')).toBe('orders-api')
  })
  it('strips a DaemonSet or Job pod to its workload', () => {
    expect(podWorkload('node-exporter-x8k2p')).toBe('node-exporter')
  })
  it('keeps a StatefulSet pod, and a word that only looks like a suffix', () => {
    expect(podWorkload('redis-0')).toBe('redis-0')
    expect(podWorkload('prometheus-node-exporter')).toBe('prometheus-node-exporter')
  })
})

describe('openCases', () => {
  it('orders worst first and, among equals, oldest first', () => {
    const cases = openCases([
      ins({ ruleId: 'frequent-restarts', severity: 'warning', resource: 'Pod/shop/pay-755f4fd9b6-x8k2p', firstSeen: '2026-10-12T11:00:00Z' }),
      ins({ ruleId: 'crash-loop', firstSeen: '2026-10-12T11:51:00Z' }),
      ins({ ruleId: 'node-not-ready', resource: 'Node/pool-b-7xk2', firstSeen: '2026-10-12T11:40:00Z' }),
    ], undefined, NOW)
    expect(cases.map((c) => c.name)).toEqual(['pool-b-7xk2', 'orders-api', 'pay'])
    expect(cases[0].to).toBe('/nodes/_/pool-b-7xk2')
  })

  it('puts what has been open for days after today, newest first', () => {
    const w = (name: string, firstSeen: string) =>
      ins({ ruleId: 'pdb-no-match', severity: 'warning', resource: `PodDisruptionBudget/shop/${name}`, firstSeen })
    const cases = openCases(
      [w('august', '2026-08-19T10:45:00Z'), w('today', '2026-10-12T11:00:00Z'), w('september', '2026-09-21T10:04:00Z')],
      undefined,
      NOW,
    )
    expect(cases.map((c) => c.name)).toEqual(['today', 'september', 'august'])
  })

  it('folds pods of one workload with the same problem into one case, keeping the oldest pod', () => {
    const cases = openCases([
      ins({ resource: 'Pod/shop/orders-api-755f4fd9b6-phpfj', firstSeen: '2026-10-12T11:55:00Z' }),
      ins({ resource: 'Pod/shop/orders-api-755f4fd9b6-qwrtz', firstSeen: '2026-10-12T11:51:00Z' }),
    ])
    expect(cases).toHaveLength(1)
    expect(cases[0].pods).toBe(2)
    expect(cases[0].since).toBe('2026-10-12T11:51:00Z')
    expect(cases[0].to).toBe('/pods/shop/orders-api-755f4fd9b6-qwrtz')
  })

  it('keeps the same workload name in two namespaces apart', () => {
    expect(openCases([ins({}), ins({ resource: 'Pod/other/orders-api-755f4fd9b6-phpfj' })])).toHaveLength(2)
  })

  it('leaves out resolved insights and rules outside the filter', () => {
    const items = [ins({ resolved: true }), ins({ ruleId: 'node-not-ready', resource: 'Node/n1' }), ins({ ruleId: 'cert-expiring', resource: 'Certificate/shop/tls' })]
    expect(openCases(items, POD_RULES)).toHaveLength(0)
    expect(openCases(items, NODE_RULES).map((c) => c.name)).toEqual(['n1'])
  })
})

describe('caseRows', () => {
  const pull = (n: number) =>
    ins({ ruleId: 'image-pull-backoff', severity: 'warning', resource: `Pod/shop/app${n}-755f4fd9b6-phpfj`, firstSeen: `2026-10-12T12:0${n}:00Z` })

  it('folds a rule with three or more cases into one row', () => {
    const cases = openCases([ins({}), pull(1), pull(2), pull(3)], undefined, NOW)
    const { rows, more } = caseRows(cases, 3)
    expect(rows).toHaveLength(2)
    expect(rows[0]).toMatchObject({ kind: 'case' })
    expect(rows[1]).toMatchObject({ kind: 'rule', rule: 'image-pull-backoff', count: 3, since: '2026-10-12T12:01:00Z' })
    expect(more).toBe(0)
  })

  it('counts what the rows leave out', () => {
    const cases = openCases([ins({}), ins({ ruleId: 'oom-killed', resource: 'Pod/shop/b-755f4fd9b6-phpfj' }), pull(1), pull(2)], undefined, NOW)
    const { rows, more } = caseRows(cases, 2)
    expect(rows).toHaveLength(2)
    expect(more).toBe(2)
  })
})
