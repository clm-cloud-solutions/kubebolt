import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Scale } from 'lucide-react'
import { api } from '@/services/api'
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
// Data sources:
//   - VM, two PromQL queries: P95 of CPU/memory per workload over a
//     7d window. Same ReplicaSet→Deployment label_replace transform
//     as TopWorkloadsCpu so the recommendations bucket by the
//     user-visible workload kind.
//   - Existing `overview.namespaceWorkloads[].workloads[].cpu/memory`
//     for the current request/limit values — already aggregated by
//     the connector, no new round-trip.
//
// Logic is intentionally deterministic, not LLM-driven: a constant
// rule set means recommendations are predictable and don't drift
// between runs. The user can audit the rule output via the per-row
// tooltip which shows raw P95 + spec values.
//
// Rules (per resource, evaluated independently for CPU and memory):
//   1. NEAR-LIMIT  if limit > 0 && P95 >= 0.8 × limit                → critical
//   2. OVER-PROV   if request > 0 && P95 < 0.5 × request &&
//                     (request - P95) above absolute floor          → warning
//   3. NO-SPECS    if request == 0 && limit == 0 && P95 > 0          → info
//
// Absolute floors prevent flagging tiny workloads where the
// percentage is technically high but the absolute waste is
// irrelevant: 50m CPU, 100Mi memory.

interface Props {
  installed: boolean
  overview?: ClusterOverview
}

type Severity = 'critical' | 'warning' | 'info'

interface Recommendation {
  namespace: string
  kind: string
  name: string
  severity: Severity
  reason: string
  // CPU values are in millicores; memory in bytes.
  cpu: ResourceFinding
  mem: ResourceFinding
}

interface ResourceFinding {
  request: number
  limit: number
  p95: number
  // 'over' | 'near-limit' | 'no-specs' | 'ok'
  state: ResourceState
  // Recommended new value when state is over/near-limit; 0 otherwise.
  // For 'over' it's the suggested request; for 'near-limit' the
  // suggested limit.
  suggest: number
}

type ResourceState = 'over' | 'near-limit' | 'no-specs' | 'ok'

const KIND_TO_PATH: Record<string, string> = {
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
}

// Floors below which a percentage-based finding is suppressed —
// prevents false-positives on near-idle controllers (kube-system
// daemons that legitimately sit at 5m / 20Mi).
const CPU_ABS_FLOOR_MILLI = 50
const MEM_ABS_FLOOR_BYTES = 100 * 1024 * 1024 // 100Mi

// Headroom multipliers when computing the suggested value: enough
// buffer that the workload doesn't get OOM-killed by a small spike,
// not so much that we just shift the over-provisioning down.
const REQUEST_HEADROOM = 1.2
const LIMIT_HEADROOM = 1.5

// PromQL: P95 over 7d, grouped by workload (Deployment / StatefulSet
// / DaemonSet). The label_replace pair collapses ReplicaSet →
// Deployment same way TopWorkloadsCpu does.
function buildP95Query(metric: string): string {
  return [
    `quantile_over_time(0.95,`,
    `  sum by (workload_kind, workload_name, pod_namespace) (`,
    `    label_replace(`,
    `      label_replace(`,
    `        ${metric}{workload_kind="ReplicaSet",workload_name!=""},`,
    `        "workload_name", "$1", "workload_name", "^(.+)-[a-z0-9]{6,12}$"`,
    `      ),`,
    `      "workload_kind", "Deployment", "workload_kind", "ReplicaSet"`,
    `    )`,
    `    or ${metric}{workload_kind=~"StatefulSet|DaemonSet",workload_name!=""}`,
    `  )[7d:5m]`,
    `)`,
  ].join(' ')
}

const CPU_P95_QUERY = buildP95Query('container_cpu_usage_cores')
const MEM_P95_QUERY = buildP95Query('container_memory_working_set_bytes')

export function RightSizingPanel({ installed, overview }: Props) {
  // P95 queries are heavy (subqueries over 7d), so cache 5m and
  // only refetch on user-driven invalidation. Polling like the
  // other panels would saturate VM for marginal value — these
  // recommendations don't shift minute-to-minute.
  const cpuQ = useQuery({
    queryKey: ['rightsizing', 'cpu-p95'],
    queryFn: () => api.queryMetrics({ query: CPU_P95_QUERY }),
    staleTime: 5 * 60_000,
    refetchInterval: 5 * 60_000,
    enabled: installed,
    retry: false,
  })
  const memQ = useQuery({
    queryKey: ['rightsizing', 'mem-p95'],
    queryFn: () => api.queryMetrics({ query: MEM_P95_QUERY }),
    staleTime: 5 * 60_000,
    refetchInterval: 5 * 60_000,
    enabled: installed,
    retry: false,
  })

  if (!installed) return null

  const isLoading = cpuQ.isLoading || memQ.isLoading
  const error = cpuQ.error ?? memQ.error

  // P95 lookups keyed by namespace/kind/name. CPU is in cores from
  // VM; convert to millicores so it lines up with the overview's
  // request/limit which are millicores.
  const cpuP95 = buildP95Index(cpuQ.data?.data?.result, (v) => Math.round(v * 1000))
  const memP95 = buildP95Index(memQ.data?.data?.result, (v) => Math.round(v))

  // Walk the overview's workloads, evaluate each. Workloads not yet
  // shipping samples (just deployed, or restricted by RBAC) get
  // their P95 as 0; that's fine — no-specs and near-limit branches
  // gate on either request/limit being set, and over-provisioned
  // requires P95 > 0 implicitly via the absolute floor.
  const recs: Recommendation[] = []
  for (const nsw of overview?.namespaceWorkloads ?? []) {
    for (const w of nsw.workloads ?? []) {
      const key = `${w.namespace}/${w.kind}/${w.name}`
      const cpu = evaluateResource(
        w.cpu?.requested ?? 0,
        w.cpu?.limit ?? 0,
        cpuP95.get(key) ?? 0,
        CPU_ABS_FLOOR_MILLI,
      )
      const mem = evaluateResource(
        w.memory?.requested ?? 0,
        w.memory?.limit ?? 0,
        memP95.get(key) ?? 0,
        MEM_ABS_FLOOR_BYTES,
      )
      const sev = combinedSeverity(cpu.state, mem.state)
      if (sev === null) continue
      recs.push({
        namespace: w.namespace,
        kind: w.kind,
        name: w.name,
        severity: sev,
        reason: describeReason(cpu, mem),
        cpu,
        mem,
      })
    }
  }

  // Sort by severity, then by absolute waste magnitude (CPU
  // millicores + memory MiB normalized) so the most actionable
  // workloads land at the top.
  const SEVERITY_ORDER: Record<Severity, number> = { critical: 0, warning: 1, info: 2 }
  recs.sort((a, b) => {
    const s = SEVERITY_ORDER[a.severity] - SEVERITY_ORDER[b.severity]
    if (s !== 0) return s
    return wasteMagnitude(b) - wasteMagnitude(a)
  })

  const VISIBLE_LIMIT = 10
  const visible = recs.slice(0, VISIBLE_LIMIT)
  const overflow = recs.length - visible.length

  if (!isLoading && !error && recs.length === 0) {
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

// ─── Pure logic helpers (testable in isolation) ──────────────────

function evaluateResource(
  request: number,
  limit: number,
  p95: number,
  absFloor: number,
): ResourceFinding {
  // Near-limit takes precedence — it's the urgent signal. P95
  // ≥ 80% of limit means a small spike will OOM/throttle.
  if (limit > 0 && p95 >= 0.8 * limit && p95 - limit > -absFloor) {
    return {
      request,
      limit,
      p95,
      state: 'near-limit',
      suggest: roundResource(p95 * LIMIT_HEADROOM),
    }
  }
  // Over-provisioned: P95 well below request, AND the absolute
  // gap is meaningful (not 5m on a 10m request).
  if (request > 0 && p95 < 0.5 * request && request - p95 > absFloor) {
    return {
      request,
      limit,
      p95,
      state: 'over',
      suggest: roundResource(p95 * REQUEST_HEADROOM),
    }
  }
  // No specs but observable usage — best-practice violation worth
  // flagging once. Skip if there's no usage either (truly idle
  // workload with no spec is fine; we don't have data to suggest
  // anything).
  if (request === 0 && limit === 0 && p95 > absFloor) {
    return { request, limit, p95, state: 'no-specs', suggest: 0 }
  }
  return { request, limit, p95, state: 'ok', suggest: 0 }
}

function combinedSeverity(cpuState: ResourceState, memState: ResourceState): Severity | null {
  if (cpuState === 'near-limit' || memState === 'near-limit') return 'critical'
  if (cpuState === 'over' || memState === 'over') return 'warning'
  if (cpuState === 'no-specs' || memState === 'no-specs') return 'info'
  return null
}

function describeReason(cpu: ResourceFinding, mem: ResourceFinding): string {
  const parts: string[] = []
  if (cpu.state === 'near-limit') parts.push('CPU near limit')
  else if (cpu.state === 'over') parts.push('CPU over-provisioned')
  if (mem.state === 'near-limit') parts.push('Memory near limit')
  else if (mem.state === 'over') parts.push('Memory over-provisioned')
  if (parts.length === 0 && (cpu.state === 'no-specs' || mem.state === 'no-specs')) {
    const which: string[] = []
    if (cpu.state === 'no-specs') which.push('CPU')
    if (mem.state === 'no-specs') which.push('memory')
    parts.push(`No ${which.join(' / ')} specs defined`)
  }
  return parts.join(' · ')
}

function stateLabel(s: ResourceState): string {
  switch (s) {
    case 'near-limit': return 'near limit'
    case 'over': return 'over-provisioned'
    case 'no-specs': return 'no specs'
    case 'ok': return 'ok'
  }
}

function wasteMagnitude(r: Recommendation): number {
  // Normalize CPU + memory into a comparable scalar for sorting:
  // 1m CPU ≈ 1Mi memory in importance for "should I reduce this".
  // The exact ratio is arbitrary — what matters is the ordering
  // within a single sort, not the units.
  const cpuWaste = r.cpu.state === 'over' ? r.cpu.request - r.cpu.p95 : 0
  const memWaste = r.mem.state === 'over' ? (r.mem.request - r.mem.p95) / (1024 * 1024) : 0
  return cpuWaste + memWaste
}

function roundResource(v: number): number {
  // Round CPU to nearest 10m, memory to nearest 10Mi. Keeps
  // recommendations human-friendly ("125m" not "127.4m").
  if (v < 1024) {
    // CPU territory (millicores)
    return Math.max(10, Math.round(v / 10) * 10)
  }
  // Memory territory (bytes); round to 10Mi
  const tenMi = 10 * 1024 * 1024
  return Math.max(tenMi, Math.round(v / tenMi) * tenMi)
}

const SEVERITY_DOT: Record<Severity, string> = {
  critical: '#ef4056',
  warning: '#f5a623',
  info: '#4c9aff',
}

// Build a workload→P95 lookup from a Prom vector response.
// `convert` lets the caller scale (e.g. cores → millicores).
function buildP95Index(
  result: Array<{ metric: Record<string, string>; value: [number, string] }> | undefined,
  convert: (v: number) => number,
): Map<string, number> {
  const map = new Map<string, number>()
  if (!result) return map
  for (const s of result) {
    const ns = s.metric.pod_namespace
    const kind = s.metric.workload_kind
    const name = s.metric.workload_name
    if (!ns || !kind || !name) continue
    const v = parseFloat(s.value?.[1] ?? '0')
    if (Number.isNaN(v)) continue
    map.set(`${ns}/${kind}/${name}`, convert(v))
  }
  return map
}

