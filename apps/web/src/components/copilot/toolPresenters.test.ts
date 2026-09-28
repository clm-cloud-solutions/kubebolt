import { describe, it, expect } from 'vitest'
import { presentTool } from './toolPresenters'

describe('presentTool — get_operational_episodes', () => {
  it('names the window so the operator sees Kobi\'s intent collapsed', () => {
    expect(presentTool('get_operational_episodes', { sinceHours: 6 }).summary)
      .toBe('last 6h · operational bursts')
  })

  it('falls back to the default window when the model omits it', () => {
    expect(presentTool('get_operational_episodes', {}).summary)
      .toBe('last 24h · operational bursts')
    // A nonsense value must not leak into the chip as "last NaNh".
    expect(presentTool('get_operational_episodes', { sinceHours: 'ayer' }).summary)
      .toBe('last 24h · operational bursts')
  })

  it('lands on the Bursts view, carrying the window Kobi asked for', () => {
    expect(presentTool('get_operational_episodes', { sinceHours: 168 }).link)
      .toEqual({ href: '/insights?view=bursts&range=7d', label: 'Open Bursts' })
    expect(presentTool('get_operational_episodes', { sinceHours: 720 }).link?.href)
      .toBe('/insights?view=bursts&range=30d')
  })

  it('opens on the default window rather than inventing a range the view ignores', () => {
    // 24h IS the view's default, so it needs no param.
    expect(presentTool('get_operational_episodes', { sinceHours: 24 }).link?.href)
      .toBe('/insights?view=bursts')
    // 13h is not a preset: the view cannot honour it, so do not pretend.
    expect(presentTool('get_operational_episodes', { sinceHours: 13 }).link?.href)
      .toBe('/insights?view=bursts')
    expect(presentTool('get_operational_episodes', {}).link?.href)
      .toBe('/insights?view=bursts')
  })

  it('still lists the arguments, so a wrong window is visible before the conclusion is', () => {
    expect(presentTool('get_operational_episodes', { sinceHours: 168 }).inputLines)
      .toEqual([{ key: 'sinceHours', value: '168' }])
  })
})

describe('presentTool — insight history', () => {
  it('surfaces the window and the scope, since those change what came back', () => {
    expect(presentTool('get_insight_episodes', { sinceHours: 72, cluster: 'all', rule: 'crash-loop' }).summary)
      .toBe('last 72h · all clusters · crash-loop')
    expect(presentTool('get_insight_episodes', {}).summary).toBe('last 24h')
  })

  it('links the list to History and the detail to that exact episode', () => {
    expect(presentTool('get_insight_episodes', {}).link)
      .toEqual({ href: '/insights?view=history', label: 'Open Insight History' })
    expect(presentTool('get_insight_episode', { id: 'abc12345-6789' }).link)
      .toEqual({ href: '/insights/episodes/abc12345-6789', label: 'Open episode' })
  })

  it('offers no episode link without an id, rather than a route to nowhere', () => {
    expect(presentTool('get_insight_episode', {}).link).toBeUndefined()
    expect(presentTool('get_insight_episode', {}).summary).toBe('one episode')
  })
})

describe('presentTool — security findings', () => {
  it('says whether Kobi asked for the posture or for rows', () => {
    expect(presentTool('get_findings', {}).summary).toBe('posture')
    expect(presentTool('get_findings', { severity: 'critical', image: 'reg/app:v1' }).summary)
      .toBe('critical · reg/app:v1')
  })

  it('flags the widened scope and the history view, which change what came back', () => {
    expect(presentTool('get_findings', { cluster: 'all' }).summary).toBe('posture · all clusters')
    expect(presentTool('get_findings', { status: 'resolved' }).summary).toBe('posture · resolved')
  })

  it('links to the Security pillar', () => {
    expect(presentTool('get_findings', {}).link).toEqual({ href: '/security', label: 'Open Security' })
  })

  it('names the lens of the workload ranking and links to that lens', () => {
    const p = presentTool('get_finding_workloads', { group: 'configuration', severity: 'high', cluster: 'all' })
    expect(p.summary).toBe('configuration by workload · high · all clusters')
    expect(p.link).toEqual({ href: '/security/configuration', label: 'Open Security' })
    expect(presentTool('get_finding_workloads', {}).summary).toBe('by workload')
    expect(presentTool('get_finding_workloads', { group: 'rbac' }).link?.href).toBe('/security/permissions')
  })

  it('right-sizing links to the Capacity screen it shares an engine with', () => {
    const p = presentTool('get_right_sizing', { namespace: 'shop' })
    expect(p.summary).toBe('7d P95 · shop')
    expect(p.link?.href).toBe('/capacity')
  })

  it('coverage and query_metrics say what they are', () => {
    expect(presentTool('get_coverage', {}).summary).toBe('what KubeBolt can see')
    const q = presentTool('query_metrics', { query: 'sum(up)', range: '6h' })
    expect(q.summary).toBe('PromQL · last 6h')
    expect(q.command).toBe('sum(up)')
    expect(presentTool('query_metrics', { query: 'up' }).summary).toBe('PromQL · now')
  })

  it('runtime events name their window and link to the Runtime lens', () => {
    const p = presentTool('get_runtime_events', { priority: 'Critical', cluster: 'all' })
    expect(p.summary).toBe('last 24h · Critical · all clusters')
    expect(p.link?.href).toBe('/security/runtime')
  })

  it('recent deploys give the kubectl equivalent and link to Capacity', () => {
    const p = presentTool('get_recent_deploys', { sinceHours: 6, namespace: 'shop' })
    expect(p.summary).toBe('last 6h · shop')
    expect(p.command).toContain('-n shop')
    expect(p.link?.href).toBe('/capacity')
  })

  it('shows which finding the detail re-read', () => {
    expect(presentTool('get_finding_detail', { fingerprint: 'abcdef0123456789' }).summary).toBe('finding abcdef01')
    expect(presentTool('get_finding_detail', {}).summary).toBe('one finding')
  })
})

describe('presentTool — fleet', () => {
  it('marks itself as the cross-cluster tool and lands on Fleet', () => {
    const p = presentTool('get_fleet_summary', {})
    expect(p.summary).toBe('every cluster')
    expect(p.link).toEqual({ href: '/fleet', label: 'Open Fleet' })
    // It takes no arguments, so an Input table would be an empty box.
    expect(p.inputLines).toEqual([])
  })
})
