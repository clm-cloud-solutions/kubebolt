import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import type { NamespaceWorkload } from '@/types/kubernetes'
import { NamespaceTiles, TILE_LIMIT, tileStats } from './NamespaceTiles'

vi.mock('@/hooks/useNamespaceQuotas', () => ({
  useNamespaceQuotas: () => ({ quotas: {} }),
  formatQuotaValue: () => '',
}))

// One namespace with a single one-pod Deployment, ready or not.
function ns(name: string, ready: boolean): NamespaceWorkload {
  return {
    namespace: name,
    workloads: [{
      name: `${name}-app`, kind: 'Deployment', namespace: name,
      replicas: 1, readyReplicas: ready ? 1 : 0, status: ready ? 'Running' : 'Degraded',
      pods: [{ name: `${name}-pod`, status: ready ? 'Running' : 'CrashLoopBackOff', ready } as never],
    }],
  } as NamespaceWorkload
}

const tiles = () => screen.queryAllByRole('link').map((a) => a.textContent ?? '')

describe('NamespaceTiles', () => {
  it('shows the first TILE_LIMIT namespaces, the ones needing attention first, and the rest on demand', () => {
    const list = [
      ...Array.from({ length: 128 }, (_, i) => ns(`app-${String(i).padStart(3, '0')}`, true)),
      ns('zz-broken', false), ns('zz-broken-2', false), ns('zz-broken-3', false),
    ]
    render(<MemoryRouter><NamespaceTiles namespaceWorkloads={list} /></MemoryRouter>)

    expect(tiles()).toHaveLength(TILE_LIMIT)
    // Alphabetically last, but critical: they lead.
    expect(tiles().slice(0, 3).every((t) => t.startsWith('zz-broken'))).toBe(true)

    fireEvent.click(screen.getByText('Show all 131 namespaces'))
    expect(tiles()).toHaveLength(131)
    fireEvent.click(screen.getByText('Show fewer'))
    expect(tiles()).toHaveLength(TILE_LIMIT)
  })

  it('says when the cut still hides namespaces that need attention', () => {
    const list = Array.from({ length: TILE_LIMIT + 3 }, (_, i) => ns(`broken-${String(i).padStart(2, '0')}`, false))
    render(<MemoryRouter><NamespaceTiles namespaceWorkloads={list} /></MemoryRouter>)
    expect(screen.getByText(`Show all ${TILE_LIMIT + 3} namespaces · 3 more need attention`)).toBeTruthy()
  })

  it('has no button when everything fits', () => {
    render(<MemoryRouter><NamespaceTiles namespaceWorkloads={[ns('a', true), ns('b', true)]} /></MemoryRouter>)
    expect(tiles()).toHaveLength(2)
    expect(screen.queryByText(/Show all/)).toBeNull()
  })
})

// A namespace's pods, by status and readiness, under one Deployment.
function nsPods(name: string, pods: Array<{ status: string; ready: boolean }>): NamespaceWorkload {
  return {
    namespace: name,
    workloads: [{
      name: `${name}-app`, kind: 'Deployment', namespace: name,
      replicas: 2, readyReplicas: 2, status: 'Running',
      pods: pods.map((p, i) => ({ name: `${name}-${i}`, ...p }) as never),
    }],
  } as NamespaceWorkload
}

describe('tileStats: a finished pod is not a warning', () => {
  it('a Succeeded pod left by a rollout does not degrade a healthy namespace', () => {
    // envoy-gateway-system on 2026-11-23: two live pods and one from the old
    // ReplicaSet in Succeeded 0/1, which painted the namespace amber.
    const s = tileStats(nsPods('envoy', [
      { status: 'Running', ready: true }, { status: 'Running', ready: true }, { status: 'Succeeded', ready: false },
    ]))
    expect(s.health).toBe('healthy')
    expect([s.totalPods, s.readyPods, s.completedPods]).toEqual([2, 2, 1])
  })

  it('finished pods that outnumber the live ones do not turn it red', () => {
    const s = tileStats(nsPods('default', [
      { status: 'Running', ready: true }, { status: 'Succeeded', ready: false }, { status: 'Succeeded', ready: false },
    ]))
    expect(s.health).toBe('healthy')
  })

  it('a namespace of finished pods only is healthy, not empty', () => {
    expect(tileStats(nsPods('jobs', [{ status: 'Succeeded', ready: false }])).health).toBe('healthy')
  })

  it('a Failed pod still counts as not ready', () => {
    const s = tileStats(nsPods('evicted', [{ status: 'Running', ready: true }, { status: 'Failed', ready: false }]))
    expect(s.health).toBe('degraded')
  })

  it('the tile counts every pod the namespace lists, finished ones included', () => {
    render(<MemoryRouter><NamespaceTiles namespaceWorkloads={[nsPods('envoy', [
      { status: 'Running', ready: true }, { status: 'Running', ready: true }, { status: 'Succeeded', ready: false },
    ])]} /></MemoryRouter>)
    expect(screen.getByText('3 pods')).toBeTruthy()
  })
})
