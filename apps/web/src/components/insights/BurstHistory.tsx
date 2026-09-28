import { useMemo } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Layers, RotateCw, Rocket, HelpCircle } from 'lucide-react'
import { api, type OperationalBurst } from '@/services/api'
import { fmtDur } from '@/components/home/ShiftReportCard'

// Bursts answer a question nothing else in the product answers: "what happened
// at 3am last Wednesday?". The shift report knows about bursts but is anchored
// to the reader's own last visit, so it can only ever say "since you were
// away" — useless three days later, when the postmortem gets written. The
// episode history lists each episode separately, which is the same blindness
// that made 48 evictions from one node rotation read as 48 problems.
//
// So the window lives in the URL, always. A burst view whose range is local
// state cannot be linked, and "come look at this" is most of its value.

export type BurstWindow = { from: Date; to: Date; preset: Preset | null; day: string | null }
export type Preset = '24h' | '7d' | '30d'

const PRESET_HOURS: Record<Preset, number> = { '24h': 24, '7d': 24 * 7, '30d': 24 * 30 }

// resolveWindow — URL to range, in precedence order:
//   from+to  explicit RFC3339, what a machine link uses (Kobi's tool card
//            passes the exact window it queried, so the page shows what the
//            answer was based on)
//   day      YYYY-MM-DD, the whole local day — the readable human form,
//            /insights?view=bursts&day=2026-09-16
//   preset   a relative window, default 24h
// Anything unparseable falls back to the default rather than rendering an
// empty range, so a mangled link still shows something true.
export function resolveWindow(params: URLSearchParams, now: Date): BurstWindow {
  const rawFrom = params.get('from')
  const rawTo = params.get('to')
  if (rawFrom && rawTo) {
    const from = new Date(rawFrom)
    const to = new Date(rawTo)
    if (!isNaN(from.getTime()) && !isNaN(to.getTime()) && from < to) {
      return { from, to, preset: null, day: null }
    }
  }
  const day = params.get('day')
  if (day && /^\d{4}-\d{2}-\d{2}$/.test(day)) {
    const [y, m, d] = day.split('-').map(Number)
    const from = new Date(y, m - 1, d, 0, 0, 0, 0)
    if (!isNaN(from.getTime())) {
      const to = new Date(y, m - 1, d + 1, 0, 0, 0, 0)
      return { from, to, preset: null, day }
    }
  }
  const raw = params.get('range')
  const preset: Preset = raw === '7d' || raw === '30d' ? raw : '24h'
  return { from: new Date(now.getTime() - PRESET_HOURS[preset] * 3600_000), to: now, preset, day: null }
}

// onsetSeconds — how long the breaking took. windowFrom..onsetTo, NOT
// windowTo: the latter is the last time any member was seen, which one chronic
// member stretches to weeks. Conflating them is what showed a four-minute node
// rotation as a "69-day window".
export function onsetSeconds(b: OperationalBurst): number {
  const from = new Date(b.windowFrom).getTime()
  const to = new Date(b.onsetTo ?? b.windowFrom).getTime()
  if (isNaN(from) || isNaN(to) || to < from) return 0
  return (to - from) / 1000
}

export function isStillOpen(b: OperationalBurst): boolean {
  return b.blast.stillFiring > 0
}

const KIND_META: Record<OperationalBurst['kind'], { label: string; hint: string; Icon: typeof RotateCw; tone: string }> = {
  node_rotation: {
    label: 'Node rotation',
    hint: 'Nodes were replaced or rebooted. Workloads that failed here are usually victims of the rotation, not its cause.',
    Icon: RotateCw,
    tone: 'bg-status-warn-dim text-status-warn',
  },
  node_pressure: {
    label: 'Node pressure',
    hint: 'A node ran out of memory or disk and started evicting.',
    Icon: AlertTriangle,
    tone: 'bg-status-error-dim text-status-error',
  },
  mass_rollout: {
    label: 'Mass rollout',
    hint: 'Many workloads were redeployed at once.',
    Icon: Rocket,
    tone: 'bg-status-info-dim text-status-info',
  },
  unknown_burst: {
    label: 'Unattributed',
    hint: 'These failed together, but nothing in the group names a common cause. Real malfunctions — configuration findings never form a burst.',
    Icon: HelpCircle,
    tone: 'bg-kb-elevated text-kb-text-tertiary',
  },
}

function fmtMoment(iso: string): string {
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '—'
  return d.toLocaleString(undefined, {
    day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit',
  })
}

function toDayInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

export function BurstHistory() {
  const [searchParams, setSearchParams] = useSearchParams()
  // `now` is pinned per render batch so the preset window does not slide
  // between the query key and the label the operator is reading.
  const now = useMemo(() => new Date(), [searchParams.toString()])
  const win = resolveWindow(searchParams, now)

  const since = win.from.toISOString()
  const until = win.to.toISOString()
  const { data, isLoading, error } = useQuery({
    queryKey: ['operational-bursts', since, until],
    queryFn: () => api.getOperationalBursts({ since, until }),
    refetchInterval: win.preset ? 60_000 : false, // a fixed date does not move
    retry: false,
  })

  const names = data?.clusterNames ?? {}
  // The clusterer emits oldest-first; the reader wants the latest thing that
  // happened at the top.
  const bursts = useMemo(() => [...(data?.episodes ?? [])].reverse(), [data])

  const setParam = (mutate: (p: URLSearchParams) => void) =>
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      next.set('view', 'bursts')
      next.delete('from'); next.delete('to'); next.delete('day'); next.delete('range')
      mutate(next)
      return next
    }, { replace: true })

  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <div className="flex rounded-lg border border-kb-border overflow-hidden text-[11px] font-mono">
          {(['24h', '7d', '30d'] as const).map((p) => (
            <button
              key={p}
              onClick={() => setParam((n) => { if (p !== '24h') n.set('range', p) })}
              className={`px-3 py-1 ${win.preset === p ? 'bg-kb-elevated text-kb-text-primary font-semibold' : 'text-kb-text-secondary hover:text-kb-text-primary'}`}
            >
              Last {p}
            </button>
          ))}
        </div>
        <label className="flex items-center gap-1.5 text-[11px] font-mono text-kb-text-tertiary">
          On a day
          <input
            type="date"
            value={win.day ?? ''}
            max={toDayInput(now)}
            onChange={(e) => {
              const v = e.target.value
              setParam((n) => { if (v) n.set('day', v) })
            }}
            className="bg-kb-card border border-kb-border rounded px-1.5 py-0.5 text-kb-text-secondary text-[11px] font-mono focus:outline-none focus:border-kb-border-active"
          />
        </label>
        <span className="text-[11px] font-mono text-kb-text-tertiary">
          {fmtMoment(since)} → {fmtMoment(until)}
        </span>
      </div>

      {isLoading && <div className="text-sm text-kb-text-tertiary font-mono">Looking for bursts…</div>}

      {error && (
        <div className="text-sm text-kb-text-tertiary font-mono">
          Burst history is not available on this install.
        </div>
      )}

      {!isLoading && !error && bursts.length === 0 && (
        <div className="py-16 text-center">
          <Layers className="w-8 h-8 mx-auto mb-3 text-kb-text-tertiary" aria-hidden />
          <p className="text-sm text-kb-text-secondary">Nothing broke together in this window.</p>
          <p className="mt-1 text-[11px] font-mono text-kb-text-tertiary">
            A burst needs several workloads failing within minutes of each other.
          </p>
        </div>
      )}

      <div className="space-y-2">
        {bursts.map((b) => {
          const meta = KIND_META[b.kind] ?? KIND_META.unknown_burst
          const { Icon } = meta
          const onset = onsetSeconds(b)
          const clusters = b.clusters.map((uid) => names[uid] || uid)
          return (
            <div key={b.id} className="rounded-lg border border-kb-border bg-kb-card p-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[10px] font-mono font-semibold uppercase ${meta.tone}`} title={meta.hint}>
                  <Icon className="w-3 h-3" aria-hidden />
                  {meta.label}
                </span>
                <span className="text-sm text-kb-text-primary font-semibold">{fmtMoment(b.windowFrom)}</span>
                <span className="text-[11px] font-mono text-kb-text-tertiary">
                  {onset > 0 ? `broke over ${fmtDur(onset)}` : 'broke at once'}
                </span>
                {isStillOpen(b) ? (
                  <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-status-info-dim text-status-info text-[10px] font-mono font-semibold">
                    <span className="w-1.5 h-1.5 rounded-full bg-status-info animate-pulse" aria-hidden />
                    {b.blast.stillFiring} STILL ACTIVE
                  </span>
                ) : (
                  <span className="px-2 py-0.5 rounded-full bg-status-ok-dim text-status-ok text-[10px] font-mono font-semibold">✓ OVER</span>
                )}
              </div>

              <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] font-mono text-kb-text-secondary">
                <span><span className="text-kb-text-primary font-semibold">{b.blast.affected}</span> affected</span>
                {b.blast.autoRecovered > 0 && <span>{b.blast.autoRecovered} recovered on their own</span>}
                {b.blast.remediated > 0 && <span>{b.blast.remediated} needed a hand</span>}
                {b.blast.expired > 0 && <span>{b.blast.expired} went silent</span>}
                {clusters.length > 0 && <span>on {clusters.join(', ')}</span>}
              </div>

              {b.blast.worstResource && (
                <div className="mt-1 text-[11px] font-mono text-kb-text-tertiary">
                  Longest out: {b.blast.worstResource}
                  {b.blast.worstSeconds > 0 && ` · ${fmtDur(b.blast.worstSeconds)}`}
                </div>
              )}

              <div className="mt-2">
                {/* The window travels: the history opens on the same range this
                    burst covers, which is the only way to see its members
                    without a per-burst endpoint. */}
                <Link
                  to={`/insights?view=history&from=${encodeURIComponent(b.windowFrom)}&to=${encodeURIComponent(b.windowTo)}`}
                  className="text-[11px] font-mono text-kb-accent hover:underline"
                >
                  See the {b.memberIds.length} episodes →
                </Link>
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}
