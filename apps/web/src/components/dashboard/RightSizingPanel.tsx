import { Link } from 'react-router-dom'
import { Scale } from 'lucide-react'
import { useMetricsOnly } from '@/hooks/useMetricsOnly'
import {
  useRightSizing,
  type Recommendation,
  type ResourceFinding,
  type ResourceState,
  type Severity,
} from '@/hooks/useRightSizing'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { HoverTooltip, TooltipHeader, TooltipRow } from '@/components/shared/Tooltip'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import { formatCPU, formatMemory } from '@/utils/formatters'
import type { ClusterOverview } from '@/types/kubernetes'
import type { PanelInquiryTriggerPayload } from '@/services/copilot/triggers'

// RightSizingPanel surfaces deterministic CPU/memory recommendations
// — workloads that are over-provisioned vs their actual P95 usage,
// workloads creeping into their limit, and workloads with no
// resource specs at all (the silent wedge of "we don't know how big
// this is supposed to be").
//
// The recommendation computation lives in the shared useRightSizing
// hook (also feeding the Capacity summary strip and the Overview
// efficiency band); this component is only the list rendering. The
// user can audit the rule output via the per-row tooltip which shows
// raw P95 + spec values.

interface Props {
  installed: boolean
  overview?: ClusterOverview
}

const KIND_TO_PATH: Record<string, string> = {
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
}

export function RightSizingPanel({ installed, overview }: Props) {
  const isMetricsOnly = useMetricsOnly()
  const { recs, isLoading, error } = useRightSizing(installed, overview)

  if (!installed) return null

  const VISIBLE_LIMIT = 10
  const visible = recs.slice(0, VISIBLE_LIMIT)
  const overflow = recs.length - visible.length

  if (!isLoading && !error && recs.length === 0) {
    // Metrics-only: there are no per-workload request/limit specs to evaluate (the
    // overview's namespaceWorkloads is connector-derived, empty here). Show a note
    // instead of an empty grid cell — full right-sizing from KSM is a Phase-2 capability.
    if (isMetricsOnly) {
      return (
        <div className="rounded-lg border border-kb-border bg-kb-card p-4">
          <div className="flex items-center gap-2 mb-2">
            <span className="text-kb-text-secondary shrink-0">
              <Scale className="w-4 h-4" />
            </span>
            <h4 className="text-sm font-semibold text-kb-text-primary">Right-sizing Recommendations</h4>
          </div>
          <p className="text-[11px] text-kb-text-tertiary leading-relaxed">
            Right-sizing needs per-workload resource specs (requests/limits), which aren't
            available on a monitored-only cluster. Enable the agent-proxy (
            <code className="text-kb-text-secondary">rbac.mode=reader</code> or{' '}
            <code className="text-kb-text-secondary">operator</code>) or connect the cluster's API
            directly to surface recommendations.
          </p>
        </div>
      )
    }
    // Healthy cluster, nothing to recommend. Hide entirely rather
    // than show a "no recommendations" empty state — the absence
    // of the panel IS the all-clear signal in a dashboard already
    // dense with cards.
    return null
  }

  // Kobi panel-level payload — visible recs serialized via the
  // shared row helper so per-row and panel-level prompts can't
  // drift in shape.
  const kobiRows = visible.map(buildRowBlob)

  return (
    <div className="rounded-lg border border-kb-border bg-kb-card p-4">
      <div className="flex items-center justify-between mb-3 gap-3">
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-kb-text-secondary shrink-0">
            <Scale className="w-4 h-4" />
          </span>
          <h4 className="text-sm font-semibold text-kb-text-primary truncate">
            Right-sizing Recommendations
          </h4>
          {recs.length > 0 && (
            <AskCopilotButton
              payload={{
                type: 'panel_inquiry',
                panel: 'right_sizing',
                rangeLabel: 'P95 over 7d',
                rows: kobiRows,
                truncatedFromTotal: recs.length,
              }}
              variant="icon"
              label="Ask Kobi about right-sizing"
            />
          )}
        </div>
        <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">
          based on 7d agent samples · P95
        </span>
      </div>

      {isLoading && (
        <div className="py-6">
          <LoadingSpinner size="sm" />
        </div>
      )}

      {error && !isLoading && (
        <div className="text-[11px] text-status-warn font-mono py-3">
          Failed to compute recommendations — VM query timed out or returned no data.
          The first samples need ~15min to flow after agent install.
        </div>
      )}

      {!isLoading && !error && visible.length > 0 && (
        <ul className="space-y-1">
          {visible.map((r) => (
            <li key={`${r.namespace}/${r.kind}/${r.name}`}>
              <Row rec={r} />
            </li>
          ))}
          {overflow > 0 && (
            <li className="text-[10px] font-mono text-kb-text-tertiary text-center pt-1.5">
              +{overflow} more recommendations
            </li>
          )}
        </ul>
      )}
    </div>
  )
}

// ─── Row + tooltip ───────────────────────────────────────────────

function Row({ rec }: { rec: Recommendation }) {
  const path = KIND_TO_PATH[rec.kind]
  const sevDot = SEVERITY_DOT[rec.severity]
  // Per-item reclaim figure (mockup's "save" chip, minus the $ —
  // currency waits for OpenCost): request − suggested request on
  // over-provisioned findings. Near-limit rows have no reclaim (they
  // ask for MORE headroom), so the chip stays absent there.
  const saveParts: string[] = []
  if (rec.cpu.state === 'over' && rec.cpu.suggest > 0) {
    saveParts.push(`−${formatCPU(rec.cpu.request - rec.cpu.suggest)}`)
  }
  if (rec.mem.state === 'over' && rec.mem.suggest > 0) {
    saveParts.push(`−${formatMemory(rec.mem.request - rec.mem.suggest)}`)
  }
  // Inner Link content — keeps the navigable region tight so the
  // user can still click anywhere on the row to open the workload.
  // Hover bg lives on the outer wrapper now so the row highlight
  // covers both the Link area and the per-row Kobi button.
  const inner = (
    <div className="flex items-center gap-3 py-1.5 min-w-0 flex-1">
      <span
        className="w-1.5 h-1.5 rounded-full shrink-0"
        style={{ background: sevDot }}
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-1.5 truncate">
          <span className="text-xs text-kb-text-primary truncate">{rec.name}</span>
          <span className="text-[10px] font-mono text-kb-text-tertiary truncate">
            {rec.namespace}
          </span>
          <span className="text-[9px] font-mono uppercase tracking-[0.06em] text-kb-text-tertiary shrink-0">
            {rec.kind}
          </span>
        </div>
        <div className="text-[10px] font-mono text-kb-text-tertiary truncate mt-0.5">
          {rec.reason}
        </div>
      </div>
      {saveParts.length > 0 && (
        <span className="text-[10px] font-mono font-semibold text-kb-accent shrink-0 tabular-nums">
          {saveParts.join(' · ')}
        </span>
      )}
      <span className="text-[9px] font-mono uppercase tracking-[0.06em] shrink-0" style={{ color: sevDot }}>
        {rec.severity}
      </span>
    </div>
  )
  // Per-row Kobi payload — single-row variant of panel_inquiry, so
  // the prompt builder picks the singular phrasing and the LLM
  // focuses on this one workload instead of summarizing a list.
  const kobiPayload: PanelInquiryTriggerPayload = {
    type: 'panel_inquiry',
    panel: 'right_sizing',
    rangeLabel: 'P95 over 7d',
    rows: [buildRowBlob(rec)],
  }
  return (
    <HoverTooltip body={<RecommendationTooltip rec={rec} />}>
      <div className="group flex items-center gap-1 px-2 rounded transition-colors hover:bg-kb-card-hover focus-within:bg-kb-card-hover">
        {path ? (
          <Link
            to={`/${path}/${encodeURIComponent(rec.namespace)}/${encodeURIComponent(rec.name)}`}
            className="flex-1 min-w-0"
          >
            {inner}
          </Link>
        ) : (
          inner
        )}
        <AskCopilotButton
          payload={kobiPayload}
          variant="icon"
          label="Ask Kobi about this recommendation"
          className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity shrink-0"
        />
      </div>
    </HoverTooltip>
  )
}

// Per-row Kobi blob — same shape the panel-level button sends,
// just for a single rec. Kept as a top-level helper so the
// panel-level mapping and the row-level mapping can't drift.
function buildRowBlob(r: Recommendation): Record<string, string | number> {
  const blob: Record<string, string | number> = {
    workload: `${r.namespace}/${r.name}`,
    kind: r.kind,
    severity: r.severity,
    reason: r.reason,
  }
  if (r.cpu.state !== 'ok') {
    blob.cpu_state = r.cpu.state
    if (r.cpu.request > 0) blob.cpu_request_milli = r.cpu.request
    if (r.cpu.limit > 0) blob.cpu_limit_milli = r.cpu.limit
    if (r.cpu.p95 > 0) blob.cpu_p95_milli = r.cpu.p95
    if (r.cpu.suggest > 0) blob.cpu_suggest_milli = r.cpu.suggest
  }
  if (r.mem.state !== 'ok') {
    blob.mem_state = r.mem.state
    if (r.mem.request > 0) blob.mem_request_bytes = r.mem.request
    if (r.mem.limit > 0) blob.mem_limit_bytes = r.mem.limit
    if (r.mem.p95 > 0) blob.mem_p95_bytes = r.mem.p95
    if (r.mem.suggest > 0) blob.mem_suggest_bytes = r.mem.suggest
  }
  return blob
}

function RecommendationTooltip({ rec }: { rec: Recommendation }) {
  return (
    <>
      <TooltipHeader right={rec.severity.toUpperCase()}>
        {rec.name} · {rec.namespace}
      </TooltipHeader>
      <div className="space-y-1">
        <ResourceRows label="CPU" finding={rec.cpu} formatFn={formatCPU} />
        <div className="h-px bg-kb-border/60 my-1.5" />
        <ResourceRows label="Memory" finding={rec.mem} formatFn={formatMemory} />
      </div>
    </>
  )
}

function ResourceRows({
  label,
  finding,
  formatFn,
}: {
  label: string
  finding: ResourceFinding
  formatFn: (v: number) => string
}) {
  return (
    <>
      <TooltipRow color="#94a3b8" label={label} value={stateLabel(finding.state)} />
      {finding.request > 0 && (
        <TooltipRow color={null} label="Request" value={formatFn(finding.request)} />
      )}
      {finding.limit > 0 && (
        <TooltipRow color={null} label="Limit" value={formatFn(finding.limit)} />
      )}
      <TooltipRow color={null} label="P95 used" value={formatFn(finding.p95)} />
      {finding.suggest > 0 && (
        <TooltipRow
          color="#22d68a"
          label={finding.state === 'near-limit' ? 'Suggest limit' : 'Suggest request'}
          value={formatFn(finding.suggest)}
        />
      )}
    </>
  )
}

function stateLabel(s: ResourceState): string {
  switch (s) {
    case 'near-limit': return 'near limit'
    case 'over': return 'over-provisioned'
    case 'no-specs': return 'no specs'
    case 'ok': return 'ok'
  }
}

const SEVERITY_DOT: Record<Severity, string> = {
  critical: '#ef4056',
  warning: '#f5a623',
  info: '#4c9aff',
}
