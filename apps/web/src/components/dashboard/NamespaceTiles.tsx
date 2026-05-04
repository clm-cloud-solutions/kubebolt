import { Link } from 'react-router-dom'
import type { NamespaceWorkload, WorkloadSummary } from '@/types/kubernetes'
import { HoverTooltip, TooltipHeader, TooltipRow } from '@/components/shared/Tooltip'

interface Props {
  namespaceWorkloads: NamespaceWorkload[]
}

// NamespaceTiles renders a compact-but-informative grid summary of
// every namespace. The earlier version was deliberately minimal (just
// pod count + health dots) which made the section thinner than the
// per-namespace expandable detail it replaced. This iteration adds:
//
//   - Workload kind counts (e.g. "3 dep · 1 sts · 2 ds") so each
//     tile carries enough texture to convey *what* is in the
//     namespace, not just *how many pods*.
//   - An unhealthy count chip when something is degraded, instead of
//     burying that signal in the dot pattern.
//   - A hover tooltip listing the unhealthy workloads with their
//     ready/replicas — the "drill detail" that previously required
//     navigating to /namespaces.
//
// Click still navigates to /namespaces for the full per-workload page.
//
// Health rule (unchanged): pod readiness ratio across the namespace.
// All ready → healthy, some ready → degraded, majority not ready →
// critical. Empty namespaces (no workloads) render muted/idle.
export function NamespaceTiles({ namespaceWorkloads }: Props) {
  if (!namespaceWorkloads || namespaceWorkloads.length === 0) return null

  return (
    <section className="space-y-3">
      <div className="flex items-baseline justify-between">
        <div>
          <div className="text-sm text-kb-text-primary font-medium">Workloads by namespace</div>
          <div className="text-[11px] font-mono text-kb-text-tertiary mt-0.5">
            Pod status + workload mix · click to filter pods by namespace
          </div>
        </div>
      </div>

      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6 gap-3">
        {namespaceWorkloads.map((nsw) => (
          <NamespaceTile key={nsw.namespace} nsw={nsw} />
        ))}
      </div>
    </section>
  )
}

type Health = 'healthy' | 'degraded' | 'critical' | 'empty'

const KIND_SHORT: Record<string, string> = {
  Deployment: 'dep',
  StatefulSet: 'sts',
  DaemonSet: 'ds',
  Job: 'job',
  CronJob: 'cj',
}

// Kind dot colors used in the tooltip rows. Same hex constants as
// the rest of the dashboard tooltips.
const HEALTH_DOT_COLOR = {
  healthy: '#22d68a',
  degraded: '#f5a623',
  critical: '#ef4056',
  empty: '#555770',
}

function NamespaceTile({ nsw }: { nsw: NamespaceWorkload }) {
  const workloads = nsw.workloads ?? []

  let totalPods = 0
  let readyPods = 0
  const kindCounts: Record<string, number> = {}
  const unhealthy: WorkloadSummary[] = []
  for (const w of workloads) {
    kindCounts[w.kind] = (kindCounts[w.kind] ?? 0) + 1
    for (const p of w.pods ?? []) {
      totalPods++
      if (p.ready) readyPods++
    }
    if ((w.readyReplicas ?? 0) < (w.replicas ?? 0)) {
      unhealthy.push(w)
    }
  }

  let health: Health
  if (totalPods === 0) health = 'empty'
  else if (readyPods === totalPods) health = 'healthy'
  else if (readyPods >= totalPods / 2) health = 'degraded'
  else health = 'critical'

  const ratio = totalPods === 0 ? 0 : readyPods / totalPods

  const dotColors = {
    healthy: 'bg-status-ok',
    degraded: 'bg-status-warn',
    critical: 'bg-status-error',
    empty: 'bg-kb-text-tertiary',
  }
  const barColor = dotColors[health]

  // Three-dot health glyph: severity expressed by how many dots are
  // "lit". Healthy = ●●●, degraded = ●●○, critical = ●○○, empty =
  // ○○○. The eye reads the ramp before the chips below load, so
  // it's the fastest "is this namespace OK?" signal in the tile.
  const dotPattern: Array<'on' | 'half' | 'off'> =
    health === 'healthy'
      ? ['on', 'on', 'on']
      : health === 'degraded'
        ? ['on', 'on', 'half']
        : health === 'critical'
          ? ['on', 'half', 'off']
          : ['off', 'off', 'off']

  // Order kinds by what's most likely interesting: workloads first
  // (dep / sts / ds), then jobs, skipping any with zero count to
  // keep the strip readable on small clusters.
  const kindOrder = ['Deployment', 'StatefulSet', 'DaemonSet', 'Job', 'CronJob']
  const kindChips = kindOrder
    .filter((k) => (kindCounts[k] ?? 0) > 0)
    .map((k) => `${kindCounts[k]} ${KIND_SHORT[k] ?? k}`)

  // Tooltip lists every unhealthy workload (capped) so a glance
  // tells the user not just "this namespace is degraded" but
  // "redis-cache 1/3, payments-api 2/4". When everything's healthy,
  // the tooltip falls back to a kind-by-kind summary so it still
  // adds context beyond what's already in the tile.
  const tooltipBody =
    unhealthy.length > 0 ? (
      <>
        <TooltipHeader right={`${unhealthy.length} degraded`}>{nsw.namespace}</TooltipHeader>
        <div className="space-y-1">
          {unhealthy.slice(0, 6).map((w) => (
            <TooltipRow
              key={`${w.kind}/${w.name}`}
              color={HEALTH_DOT_COLOR.degraded}
              label={`${KIND_SHORT[w.kind] ?? w.kind.toLowerCase()} · ${w.name}`}
              value={`${w.readyReplicas ?? 0}/${w.replicas ?? 0}`}
            />
          ))}
          {unhealthy.length > 6 && (
            <div className="text-[10px] text-kb-text-tertiary pt-1">
              +{unhealthy.length - 6} more — click to see all
            </div>
          )}
        </div>
      </>
    ) : workloads.length > 0 ? (
      <>
        <TooltipHeader right="all healthy">{nsw.namespace}</TooltipHeader>
        <div className="space-y-1">
          {kindOrder
            .filter((k) => (kindCounts[k] ?? 0) > 0)
            .map((k) => (
              <TooltipRow
                key={k}
                color={HEALTH_DOT_COLOR.healthy}
                label={k.toLowerCase()}
                value={`${kindCounts[k]}`}
              />
            ))}
          <TooltipRow color={null} label="pods" value={`${readyPods}/${totalPods}`} />
        </div>
      </>
    ) : null

  // Drill target: /pods filtered to this namespace. Pods are the
  // universal workload unit (every Deployment / StatefulSet / DaemonSet
  // / Job produces them) so this is the most useful single landing
  // page when the user clicks "what's running in this namespace?".
  // Linking to /namespaces was the previous default; the user found
  // that page too far from actionable so we go straight to the list.
  // The query param is consumed by ResourceListPage's useSearchParams
  // wiring, which initializes the namespace filter from the URL.
  const drillTarget = `/pods?namespace=${encodeURIComponent(nsw.namespace)}`

  const tile = (
    <Link
      to={drillTarget}
      className="group block bg-kb-card border border-kb-border rounded-lg p-3 transition-colors hover:border-kb-border-active hover:bg-kb-card-hover"
    >
      <div className="flex items-baseline justify-between gap-2">
        <span
          className="text-xs font-mono text-kb-text-primary truncate"
          title={nsw.namespace}
        >
          {nsw.namespace}
        </span>
        <span className="flex items-center gap-2 shrink-0">
          <span className="text-[10px] font-mono text-kb-text-tertiary tabular-nums">
            {totalPods} {totalPods === 1 ? 'pod' : 'pods'}
          </span>
          <span className="inline-flex items-center gap-0.5">
            {dotPattern.map((state, i) => (
              <span
                key={i}
                className={`w-1.5 h-1.5 rounded-full ${
                  state === 'on'
                    ? dotColors[health]
                    : state === 'half'
                      ? `${dotColors[health]} opacity-40`
                      : 'bg-kb-elevated'
                }`}
              />
            ))}
          </span>
        </span>
      </div>

      {/* Kind-count strip + unhealthy chip. The mono dots between
          chips keep the strip scannable even when several kinds are
          present. Hides entirely on namespaces with no workloads to
          avoid a stretched empty row. */}
      {kindChips.length > 0 && (
        <div className="mt-1.5 flex items-center gap-1.5 flex-wrap text-[10px] font-mono text-kb-text-tertiary leading-none">
          {kindChips.map((chip, i) => (
            <span key={chip} className="flex items-center gap-1.5">
              {i > 0 && <span className="text-kb-text-tertiary/50">·</span>}
              <span>{chip}</span>
            </span>
          ))}
          {unhealthy.length > 0 && (
            <span className="ml-auto px-1.5 py-0.5 rounded bg-status-warn-dim text-status-warn text-[9px] font-semibold uppercase tracking-[0.04em]">
              {unhealthy.length} degraded
            </span>
          )}
        </div>
      )}

      {/* Bottom progress bar shows the ready/total fraction. The
          floor for "critical" prevents a 0% bar from rendering as
          empty — a sliver still signals "something exists, it's
          just broken". */}
      <div className="mt-2.5 h-[3px] rounded-full bg-kb-elevated overflow-hidden">
        <div
          className={`h-full ${barColor}`}
          style={{ width: `${Math.max(ratio * 100, health === 'critical' ? 12 : 0)}%` }}
        />
      </div>
    </Link>
  )

  if (!tooltipBody) return tile
  return <HoverTooltip body={tooltipBody}>{tile}</HoverTooltip>
}
