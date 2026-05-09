import { useCoverage, type CoverageSource } from '@/hooks/useCoverage'
import { Activity, CheckCircle2, MinusCircle } from 'lucide-react'

// CoverageBanner surfaces which observability sources are actively
// shipping samples to VictoriaMetrics for the current cluster. Phase
// 2 Day 5 of the Universal Data Plane Plan. The banner is
// informational, not gating — the UI panels themselves have their
// own empty-state copy when their underlying source is silent. This
// banner exists so the operator can see "agent ✓, hubble ✓,
// node-exporter ✗" without leaving the dashboard.
//
// Hidden when nothing is active yet (greenfield install pre-Phase 2,
// no agent installed) so it doesn't add visual noise to the
// cluster-unreachable empty state. Also hidden when ALL known
// sources are active — the all-green case doesn't need a banner
// taking up space.
//
// The intermediate state — at least one active, at least one
// inactive — is where the banner pulls its weight. Operator sees
// at a glance what they don't have yet.
export function CoverageBanner() {
  const { data, isLoading, error } = useCoverage()

  if (isLoading || error || !data) return null

  const active = data.sources.filter((s) => s.status === 'active')
  const inactive = data.sources.filter((s) => s.status === 'inactive')

  // Hide when nothing is active (greenfield) — the install banner
  // elsewhere on the page already covers this case.
  if (active.length === 0) return null

  // Hide when everything is green — no signal, no noise.
  if (inactive.length === 0) return null

  return (
    <div className="rounded-lg border border-kb-border bg-kb-card px-4 py-2.5 flex items-center gap-3 text-[11px]">
      <span className="text-kb-text-secondary shrink-0 flex items-center gap-1.5">
        <Activity className="w-3.5 h-3.5" />
        Coverage
      </span>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 flex-1 min-w-0">
        {data.sources.map((s) => (
          <SourceChip key={s.name} source={s} />
        ))}
      </div>
      <span className="text-[10px] text-kb-text-tertiary shrink-0 hidden sm:inline">
        last seen ≤ {data.lookbackMinutes}m
      </span>
    </div>
  )
}

function SourceChip({ source }: { source: CoverageSource }) {
  const isActive = source.status === 'active'
  return (
    <span
      className={[
        'inline-flex items-center gap-1 font-mono text-[10.5px]',
        isActive ? 'text-kb-text-primary' : 'text-kb-text-tertiary',
      ].join(' ')}
      title={source.probe}
    >
      {isActive ? (
        <CheckCircle2 className="w-3 h-3 text-status-ok" />
      ) : (
        <MinusCircle className="w-3 h-3" />
      )}
      {source.name}
    </span>
  )
}
