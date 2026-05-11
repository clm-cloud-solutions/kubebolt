import {
  useServiceEndpoints,
  serviceKey,
  classifyEndpoints,
  type ServiceEndpointSummary,
} from '@/hooks/useServiceEndpoints'
import { HoverTooltip, TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'

interface Props {
  namespace: string
  name: string
  // Optional context that lets the cell suppress the "no endpoints"
  // alarm for Service types where empty endpoints is by-design.
  serviceType?: string  // 'ClusterIP' | 'NodePort' | 'LoadBalancer' | 'ExternalName'
  clusterIP?: string    // 'None' for headless
  // Render variant. 'compact' is the table cell; 'inline' is a slim
  // text + tooltip used inside a status overview field.
  variant?: 'compact' | 'inline'
}

// EndpointHealthCell renders a single Service's endpoint summary,
// gated by Service type so the alarm only fires where it's actionable:
//
//   - ExternalName  → muted "—" (no endpoints by design)
//   - Headless      → muted "—" (DNS-only; empty is legit)
//   - All others    → ready/total with severity color, tooltip with
//                     ready/notReady breakdown
//
// The actionable bands match the backend serviceNoEndpointsRule
// (P25-05): zero ready → 'down' (red), some-not-ready → 'partial'
// (amber), all ready → 'healthy' (muted text). 'empty' (no series
// at all) renders muted because we can't distinguish "KSM hasn't
// scraped" from "Service legitimately has no endpoints" from this
// vantage point.
export function EndpointHealthCell({
  namespace,
  name,
  serviceType,
  clusterIP,
  variant = 'compact',
}: Props) {
  const { endpoints } = useServiceEndpoints()

  // For Service shapes that don't carry kube-endpoint addresses, just
  // render an em-dash — no point asking "are endpoints ready" for an
  // ExternalName.
  if (serviceType === 'ExternalName' || clusterIP === 'None') {
    return <span className="text-[11px] font-mono text-kb-text-tertiary">—</span>
  }

  const summary = endpoints[serviceKey(namespace, name)]
  // Once we've passed the type guards (not ExternalName, not Headless),
  // we're looking at a Service that's *expected* to back traffic. So
  // total=0 is a real problem (selector matches no pods → silently
  // black-holes traffic), not a benign empty. We override the
  // classifier's 'empty' to 'down' for these types — an explicit red
  // signal is worth more than a muted dash here.
  const rawHealth = classifyEndpoints(summary)
  const health =
    summary && summary.total === 0 && rawHealth === 'empty' ? 'down' : rawHealth

  const colorClass =
    health === 'down'
      ? 'text-status-error'
      : health === 'partial'
        ? 'text-status-warn'
        : health === 'healthy'
          ? 'text-status-ok'
          : 'text-kb-text-tertiary'

  const text = (() => {
    if (!summary) return '—'
    if (summary.total === 0) return '0'
    if (summary.ready === summary.total) return `${summary.ready}`
    return `${summary.ready} / ${summary.total}`
  })()

  const tooltipBody = (
    <EndpointTooltip namespace={namespace} name={name} summary={summary} health={health} />
  )

  const wrapper =
    variant === 'compact' ? (
      <span className={`text-[11px] font-mono tabular-nums ${colorClass}`}>{text}</span>
    ) : (
      <span className={`text-[12px] font-mono tabular-nums ${colorClass}`}>{text}</span>
    )

  // No tooltip when KSM hasn't scraped at all — there's nothing to
  // show beyond what the em-dash already conveys, and we don't want
  // the cell to invite a hover that produces nothing.
  if (!summary) return wrapper
  return <HoverTooltip body={tooltipBody}>{wrapper}</HoverTooltip>
}

function EndpointTooltip({
  namespace,
  name,
  summary,
  health,
}: {
  namespace: string
  name: string
  summary?: ServiceEndpointSummary
  health: ReturnType<typeof classifyEndpoints>
}) {
  if (!summary) {
    return (
      <>
        <TooltipHeader>{namespace}/{name}</TooltipHeader>
        <TooltipNote>No endpoint data — kube-state-metrics may not be scraping yet.</TooltipNote>
      </>
    )
  }
  const right =
    health === 'down'
      ? 'down'
      : health === 'partial'
        ? 'degraded'
        : health === 'empty'
          ? 'no endpoints'
          : 'healthy'
  // Two distinct down narratives, separately actionable:
  //   total=0  → selector matches nothing in the cluster (config bug)
  //   total>0  → backing pods exist but aren't ready (workload bug)
  // The hint text differs accordingly so the operator's first action
  // is correct for the case at hand.
  const downIsConfigBug = health === 'down' && summary.total === 0
  return (
    <>
      <TooltipHeader right={right}>{namespace}/{name}</TooltipHeader>
      <div className="space-y-1">
        <TooltipRow color="#22d68a" label="ready" value={String(summary.ready)} />
        <TooltipRow color="#f5a623" label="not ready" value={String(summary.notReady)} />
        <TooltipRow color={null} label="total" value={String(summary.total)} />
      </div>
      {downIsConfigBug && (
        <TooltipNote>
          Selector matches no pods — check spec.selector.
        </TooltipNote>
      )}
      {health === 'down' && !downIsConfigBug && (
        <TooltipNote>
          Pods exist but none ready — check probes & logs.
        </TooltipNote>
      )}
      {health === 'partial' && (
        <TooltipNote>
          Some pods not ready — degraded capacity.
        </TooltipNote>
      )}
    </>
  )
}
