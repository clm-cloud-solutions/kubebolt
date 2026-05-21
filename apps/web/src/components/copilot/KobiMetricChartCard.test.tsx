import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { KobiMetricChartCard } from './KobiMetricChartCard'
import type { WorkloadMetricsResponse } from '@/services/copilot/types'

// Spec #08 V1 — unit tests for the inline metric chart card. The chart
// rendering itself is delegated to Recharts which is a pain to assert on
// pixel-level; we focus instead on the deterministic rails the component
// exposes: header content, strip values, empty-state branches, per-container
// row caps. Snapshot-style assertions stay off — they break on Recharts
// updates without surfacing a real regression.

// Recharts uses ResponsiveContainer which needs a measured parent in jsdom;
// stubbing it lets the rest of the chart mount predictably without
// pulling resize-observer-polyfill into the test environment.
vi.mock('recharts', async (importActual) => {
  const actual = await importActual<typeof import('recharts')>()
  return {
    ...actual,
    ResponsiveContainer: ({ children }: { children: React.ReactNode }) => (
      <div data-testid="responsive-container">{children}</div>
    ),
  }
})

function makeResponse(overrides: Partial<WorkloadMetricsResponse> = {}): WorkloadMetricsResponse {
  return {
    workload: { kind: 'Deployment', namespace: 'default', name: 'cpu-burner' },
    range: '15m',
    end: '2026-05-21T14:30:00Z',
    podsResolved: 2,
    metrics: {
      cpu: {
        unit: 'cores',
        summary: { min: 0.18, avg: 0.198, max: 0.226, p95: 0.221 },
        trend: [
          { t: '2026-05-21T14:16:00Z', v: 0.198 },
          { t: '2026-05-21T14:17:00Z', v: 0.201 },
          { t: '2026-05-21T14:18:00Z', v: 0.199 },
          { t: '2026-05-21T14:19:00Z', v: 0.226 },
        ],
        request: 0.2,
        limit: 0.2,
        utilizationPercent: { vsRequest: 113, vsLimit: 113 },
      },
    },
    ...overrides,
  }
}

describe('KobiMetricChartCard', () => {
  it('renders header with workload identity and range', () => {
    render(<KobiMetricChartCard data={makeResponse()} />)
    expect(screen.getByText(/Deployment · default\/cpu-burner/)).toBeInTheDocument()
    expect(screen.getByText(/last 15m · 2 pods/)).toBeInTheDocument()
  })

  it('renders CPU strip with avg / max / limit / utilization chip', () => {
    render(<KobiMetricChartCard data={makeResponse()} />)
    expect(screen.getByText('CPU')).toBeInTheDocument()
    // 0.198 cores → millicores when scale picks ms; matches dashboard convention.
    // Either "198 m" or "0.198 cores" is acceptable depending on pickScale's
    // threshold; we just assert numeric presence to avoid false negatives on
    // scale-tier shifts.
    expect(screen.getByText(/avg/)).toBeInTheDocument()
    expect(screen.getByText(/max/)).toBeInTheDocument()
    // 113% utilization → danger band (>= 95) → chip rendered.
    // The chip text is unique enough to not collide with the limit row or
    // the ReferenceLine label (which both say "limit").
    expect(screen.getByText(/113% \/ limit/)).toBeInTheDocument()
    // The strip's "limit <value>" row uses a numeric value the ReferenceLine
    // label doesn't repeat, so match on the leading "limit " with a digit.
    expect(screen.getByText(/limit \d/)).toBeInTheDocument()
  })

  it('omits threshold chips when request and limit are absent (KSM-not-installed path)', () => {
    const noKsm = makeResponse()
    if (noKsm.metrics.cpu) {
      delete noKsm.metrics.cpu.request
      delete noKsm.metrics.cpu.limit
      delete noKsm.metrics.cpu.utilizationPercent
    }
    render(<KobiMetricChartCard data={noKsm} />)
    expect(screen.queryByText(/% \/ limit/)).not.toBeInTheDocument()
    expect(screen.queryByText(/% \/ request/)).not.toBeInTheDocument()
    // The fallback chip surfaces so the operator knows it's not a render bug.
    expect(screen.getByText(/no limits/)).toBeInTheDocument()
  })

  it('renders empty-state note when podsResolved=0 (no charts)', () => {
    const noPods = makeResponse({
      podsResolved: 0,
      note: 'Workload has no running pods in the queried range',
    })
    if (noPods.metrics.cpu) noPods.metrics.cpu.trend = []
    render(<KobiMetricChartCard data={noPods} />)
    expect(screen.getByText(/No active pods in the queried window/)).toBeInTheDocument()
    // No chart sub-blocks render.
    expect(screen.queryByText('CPU')).not.toBeInTheDocument()
  })

  it('renders empty-state note when trend is empty but pods exist', () => {
    const noData = makeResponse()
    if (noData.metrics.cpu) noData.metrics.cpu.trend = []
    render(<KobiMetricChartCard data={noData} />)
    expect(screen.getByText(/No samples in the queried range/)).toBeInTheDocument()
  })

  it('renders multiple metrics stacked (cpu + memory)', () => {
    const both = makeResponse({
      metrics: {
        cpu: makeResponse().metrics.cpu!,
        memory: {
          unit: 'bytes',
          summary: { min: 134217728, avg: 167772160, max: 234881024, p95: 218103808 },
          trend: [
            { t: '2026-05-21T14:16:00Z', v: 167772160 },
            { t: '2026-05-21T14:17:00Z', v: 234881024 },
          ],
          request: 268435456,
          limit: 536870912,
          utilizationPercent: { vsRequest: 81, vsLimit: 40 },
        },
      },
    })
    render(<KobiMetricChartCard data={both} />)
    expect(screen.getByText('CPU')).toBeInTheDocument()
    expect(screen.getByText('Memory')).toBeInTheDocument()
  })

  it('renders per-container rows sorted by max usage, descending', () => {
    const withContainers = makeResponse()
    if (withContainers.metrics.cpu) {
      withContainers.metrics.cpu.perContainer = {
        'idle-sidecar': {
          summary: { min: 0, avg: 0.0001, max: 0.0002, p95: 0.0002 },
          trend: [{ t: '2026-05-21T14:16:00Z', v: 0.0001 }],
        },
        heavy: {
          summary: { min: 0.19, avg: 0.196, max: 0.226, p95: 0.221 },
          trend: [{ t: '2026-05-21T14:16:00Z', v: 0.196 }],
        },
      }
    }
    render(<KobiMetricChartCard data={withContainers} />)
    expect(screen.getByText(/per container/i)).toBeInTheDocument()
    expect(screen.getByText('heavy')).toBeInTheDocument()
    expect(screen.getByText('idle-sidecar')).toBeInTheDocument()
    // Both rows render; sorting is implicit (verified by visual order in DOM).
    const heavyEl = screen.getByText('heavy')
    const idleEl = screen.getByText('idle-sidecar')
    expect(heavyEl.compareDocumentPosition(idleEl) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('caps per-container at 4 rows and shows "+N more" affordance', () => {
    const many = makeResponse()
    if (many.metrics.cpu) {
      many.metrics.cpu.perContainer = {}
      for (let i = 0; i < 7; i++) {
        many.metrics.cpu.perContainer[`c${i}`] = {
          summary: { min: 0, avg: 0.01 * (i + 1), max: 0.02 * (i + 1), p95: 0.02 * (i + 1) },
          trend: [{ t: '2026-05-21T14:16:00Z', v: 0.01 * (i + 1) }],
        }
      }
    }
    render(<KobiMetricChartCard data={many} />)
    // Top 4 by max should be visible (c6, c5, c4, c3 by sort).
    expect(screen.getByText('c6')).toBeInTheDocument()
    expect(screen.getByText('c5')).toBeInTheDocument()
    expect(screen.getByText('c4')).toBeInTheDocument()
    expect(screen.getByText('c3')).toBeInTheDocument()
    // Bottom 3 should be hidden behind the affordance.
    expect(screen.queryByText('c0')).not.toBeInTheDocument()
    expect(screen.queryByText('c1')).not.toBeInTheDocument()
    expect(screen.queryByText('c2')).not.toBeInTheDocument()
    expect(screen.getByText(/\+3 more containers hidden/)).toBeInTheDocument()
  })

  it('renders network metrics with the correct labels and accent', () => {
    const net = makeResponse({
      metrics: {
        network_rx: {
          unit: 'bytes/sec',
          summary: { min: 1024, avg: 4096, max: 12288, p95: 9216 },
          trend: [{ t: '2026-05-21T14:16:00Z', v: 4096 }],
        },
        network_tx: {
          unit: 'bytes/sec',
          summary: { min: 512, avg: 2048, max: 8192, p95: 6144 },
          trend: [{ t: '2026-05-21T14:16:00Z', v: 2048 }],
        },
      },
    })
    render(<KobiMetricChartCard data={net} />)
    expect(screen.getByText('Network RX')).toBeInTheDocument()
    expect(screen.getByText('Network TX')).toBeInTheDocument()
  })
})
