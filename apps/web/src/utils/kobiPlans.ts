// Kobi Plans — words and figures for the plan card and the plan view. The
// plan carries codes (owner kinds, risk reasons, exclusion reasons); the
// person reads them here, in words. Every figure is the plan's own: nothing
// here computes a saving. What a plan frees is priced at list prices and is
// not a saving: the bill falls only once nodes or disks are removed.

import type {
  KobiPlanView,
  PlanConsolidationEvidence,
  PlanContainerResources,
  PlanLink,
  PlanNodeBlock,
  PlanPoolBefore,
  PlanRemoveNodes,
  PlanExclusion,
  PlanOwner,
  PlanRisk,
  PlanSavings,
  PlanSetResources,
  PlanStatus,
  PlanStep,
} from '@/types/kobiPlans'

export const STATUS_LABEL: Record<PlanStatus, string> = {
  draft: 'Draft',
  exported: 'Exported',
  verified: 'Verified',
  expired: 'Expired',
  approved: 'Approved',
  executing: 'Executing',
  failed: 'Failed',
}

// statusWords is the status a person reads: a plan someone discarded says
// so, rather than «Expired» (plans-06).
export function statusWords(status: PlanStatus, expiredReason?: string): string {
  if (status === 'expired' && expiredReason === 'discarded') return 'Discarded'
  return STATUS_LABEL[status]
}

// The status chip's fill, in the grammar of Fleet's health chips.
export const STATUS_BADGE_CLASS: Record<PlanStatus, string> = {
  draft: 'bg-kb-elevated text-kb-text-secondary',
  exported: 'bg-status-info-dim text-status-info',
  verified: 'bg-status-ok-dim text-status-ok',
  expired: 'bg-kb-elevated text-kb-text-tertiary',
  approved: 'bg-status-info-dim text-status-info',
  executing: 'bg-status-info-dim text-status-info',
  failed: 'bg-status-error-dim text-status-error',
}

export const RISK_LABEL: Record<PlanRisk, string> = { low: 'low risk', medium: 'medium risk', high: 'high risk' }

const RISK_REASON: Record<string, string> = {
  single_pod: 'a single replica',
  admission_webhook: 'serves an admission webhook that fails closed',
  qos_change: 'the pod stops being Guaranteed',
  lowers_memory_request: 'less memory reserved',
  daemonset: 'a DaemonSet: every node restarts',
  statefulset: 'a StatefulSet: pods restart in order',
  low_confidence: 'under two days of history',
  imposed: 'a value set by a person',
  below_engine_value: 'below what the engine measured as needed',
  less_headroom: 'one node less of headroom',
  short_history: 'under a week of use',
  depends_on_plan: 'waits for right-sizing',
}

export function riskReasonWords(code: string): string {
  return RISK_REASON[code] ?? code.replace(/_/g, ' ')
}

const EXCLUSION_REASON: Record<string, string> = {
  provider_managed: 'Managed by the provider',
  operator_managed: 'Managed by an operator',
  hpa_target: 'Scaled by an HPA',
  multi_container: 'Several containers',
  pods_varied: 'Pod count changed',
  no_engine_value: 'Nothing to resize',
  gitops_unknown: 'GitOps, source unknown',
  owner_unknown: 'Owner unknown',
  in_live_plan: 'In another live plan',
  pool_min_nodes: 'At its fewest nodes',
  pool_headroom: 'No room to spare',
  pool_blocked: 'A pod that cannot move',
}

export function exclusionReasonWords(code: string): string {
  return EXCLUSION_REASON[code] ?? code.replace(/_/g, ' ')
}

const PROVIDER: Record<string, string> = { aks: 'AKS', eks: 'EKS', gke: 'GKE', 'addon-manager': 'the addon manager' }

// ownerWords says who manages a workload: «Helm · release kyverno».
export function ownerWords(owner?: PlanOwner): string {
  if (!owner) return 'not identified'
  const ref = owner.ref ?? ''
  switch (owner.kind) {
    case 'provider':
      return `provider · ${PROVIDER[ref] ?? ref}`
    case 'operator':
      return `operator · ${ref}`
    case 'helm':
      return ref ? `Helm · release ${ref}` : 'Helm'
    case 'gitops': {
      const [tool, app] = ref.split(':')
      const name = tool === 'argocd' ? 'Argo CD' : tool === 'flux' ? 'Flux' : 'GitOps'
      return app ? `${name} · ${app}` : name
    }
    case 'none':
      return 'nobody'
    default:
      return 'not identified'
  }
}

// ownerNote is what the owner means for applying the change, or null.
export function ownerNote(owner?: PlanOwner): string | null {
  switch (owner?.kind) {
    case 'helm':
      return `Managed by Helm: the next helm upgrade undoes a change made in the cluster. The value belongs in the values of release ${owner.ref ?? ''}.`.replace(' .', '.')
    case 'gitops':
      return 'Managed by GitOps: the next sync undoes a change made in the cluster. The value belongs in the repository.'
    case 'unknown':
    case undefined:
      return 'KubeBolt could not tell who manages this workload. Check it before applying the change.'
  }
  return null
}

// formatCpuMilli writes millicores the way a person reads them.
export function formatCpuMilli(m: number): string {
  if (m >= 1000) return `${(m / 1000).toFixed(2).replace(/\.?0+$/, '')} cores`
  return `${Math.round(m)}m`
}

// formatMemBytes writes bytes in binary units.
export function formatMemBytes(b: number): string {
  const mi = b / (1024 * 1024)
  if (mi >= 1024) return `${(mi / 1024).toFixed(2).replace(/\.?0+$/, '')} GiB`
  return `${Math.round(mi)} MiB`
}

// formatPlanMoney writes the plan's amount as it is stored, in its currency.
export function formatPlanMoney(monthly: number, currency: string): string {
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency: currency.toUpperCase(), minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(monthly)
  } catch {
    return `${monthly.toFixed(2)} ${currency.toUpperCase()}`
  }
}

// MINUS is the minus sign joined to what follows (U+2060, word joiner): a
// narrow card broke «−» and the amount onto two lines.
export const MINUS = '\u2212\u2060'

// signedPlanMoney writes a change with its sign: «−4,00 €», «+0,00 €».
export function signedPlanMoney(change: number, currency: string): string {
  const text = formatPlanMoney(Math.abs(change), currency)
  return change < 0 ? `${MINUS}${text}` : `+\u2060${text}`
}

// freedUnits names only what is freed: «330m CPU», «120 MiB memory», both, or
// null when nothing is (a raised limit).
export function freedUnits(s: PlanSavings): string | null {
  const parts = freedParts(s)
  return parts.length > 0 ? parts.join(' · ') : null
}

// freedParts is the same, one unit per entry: a narrow cell breaks between
// them, never inside «100 MiB».
export function freedParts(s: PlanSavings): string[] {
  const parts: string[] = []
  if (s.cpuMilli > 0) parts.push(`${formatCpuMilli(s.cpuMilli)} CPU`)
  if (s.memBytes > 0) parts.push(`${formatMemBytes(s.memBytes)} memory`)
  return parts
}

// historyWords: «P95 of the last 7 days», and how much of it the data covers
// when the cluster is younger (a fact, never a warning).
export function historyWords(plan: Pick<KobiPlanView, 'engine'>): string {
  const days = plan.engine.windowDays
  const base = 'P95 of the last 7 days'
  if (days === undefined || days >= 7) return base
  return `${base} (${days.toLocaleString(undefined, { maximumFractionDigits: 1 })} days of data)`
}

// scopeWords says what the plan looked at: «whole cluster», «namespace
// kyverno», «namespace kyverno · 4 workloads». Workloads are counted past two;
// the steps name them.
export function scopeWords(plan: Pick<KobiPlanView, 'scope'>): string {
  const { namespaces, workloads } = plan.scope
  if (namespaces.length === 0 && workloads.length === 0) return 'whole cluster'
  const parts: string[] = []
  if (namespaces.length > 0) parts.push(`${namespaces.length === 1 ? 'namespace' : 'namespaces'} ${namespaces.join(', ')}`)
  if (workloads.length > 2) {
    parts.push(`${workloads.length} workloads`)
  } else if (workloads.length > 0) {
    // namespace/name or namespace/kind/name: the name is the last segment,
    // and the namespace is said once when it is the scope's.
    parts.push(
      workloads
        .map((w) => {
          const seg = w.split('/')
          return namespaces.includes(seg[0]) ? seg[seg.length - 1] : w
        })
        .join(', '),
    )
  }
  return parts.join(' · ')
}

// sharedNamespace is the one namespace every step and exclusion is in, or
// null when they span several (then each workload carries its own).
export function sharedNamespace(plan: Pick<KobiPlanView, 'steps' | 'excluded'>): string | null {
  const ns = new Set<string>()
  for (const s of plan.steps) ns.add(s.target.namespace ?? '')
  for (const x of plan.excluded) ns.add(x.target.namespace ?? '')
  if (ns.size !== 1) return null
  const [only] = [...ns]
  return only || null
}

// workloadName names a target, with its namespace unless the plan already
// said it once.
export function workloadName(target: { namespace?: string; name: string }, shared: string | null): string {
  if (!target.namespace || target.namespace === shared) return target.name
  return `${target.namespace} / ${target.name}`
}

export interface ChangeLine {
  container: string
  text: string
}

// changeLines says each change as «cpu request 100m → 10m», per container.
export function changeLines(step: Pick<PlanStep, 'params' | 'before'>): ChangeLine[] {
  const before = new Map((step.before?.containers ?? []).map((c) => [c.container, c]))
  const out: ChangeLine[] = []
  for (const c of step.params?.containers ?? []) {
    const was: PlanContainerResources | undefined = before.get(c.container)
    const add = (res: 'cpu' | 'memory', field: 'request' | 'limit', now?: string, old?: string) => {
      if (!now) return
      out.push({ container: c.container, text: `${res} ${field} ${old ?? 'unset'} → ${now}` })
    }
    add('cpu', 'request', c.requests?.cpu, was?.requests?.cpu)
    add('memory', 'request', c.requests?.memory, was?.requests?.memory)
    add('cpu', 'limit', c.limits?.cpu, was?.limits?.cpu)
    add('memory', 'limit', c.limits?.memory, was?.limits?.memory)
  }
  return out
}

// A value a person may edit in a pending step (plans-06): only what the step
// already changes, with the engine's value, the floor under which the plan
// refuses it (the per-pod P95, rounded up) and today's value, which a request
// must stay under and a limit above (the step's direction).
export interface EditableValue {
  key: string
  container: string
  initContainer?: boolean
  field: 'requests' | 'limits'
  res: 'cpu' | 'memory'
  value: string
  engine?: string
  floor?: string
  today?: string
}

export function editableValues(step: Pick<PlanStep, 'params' | 'engineParams' | 'evidence' | 'before'>): EditableValue[] {
  const engine = new Map((step.engineParams?.containers ?? []).map((c) => [`${c.container}|${!!c.initContainer}`, c]))
  const before = new Map((step.before?.containers ?? []).map((c) => [`${c.container}|${!!c.initContainer}`, c]))
  const pods = step.evidence?.pods ?? 0
  const floor = (res: 'cpu' | 'memory'): string | undefined => {
    const p95 = res === 'cpu' ? step.evidence?.cpu?.p95 : step.evidence?.mem?.p95
    if (!p95 || pods < 1) return undefined
    return res === 'cpu' ? `${Math.ceil(p95 / pods)}m` : `${Math.ceil(p95 / pods / (1024 * 1024))}Mi`
  }
  const out: EditableValue[] = []
  for (const c of step.params?.containers ?? []) {
    const eng = engine.get(`${c.container}|${!!c.initContainer}`)
    const was = before.get(`${c.container}|${!!c.initContainer}`)
    for (const field of ['requests', 'limits'] as const) {
      for (const res of ['cpu', 'memory'] as const) {
        const value = c[field]?.[res]
        if (!value) continue
        out.push({
          key: `${c.container}|${field}|${res}`,
          container: c.container,
          initContainer: c.initContainer,
          field,
          res,
          value,
          engine: eng?.[field]?.[res],
          floor: floor(res),
          today: was?.[field]?.[res],
        })
      }
    }
  }
  return out
}

// valueRange says what the plan takes for one value, in words: «at least 7m
// (P95 per pod) · under 100m (today)».
export function valueRange(r: EditableValue): string {
  const parts: string[] = []
  if (r.floor) parts.push(`at least ${r.floor} (P95 per pod)`)
  if (r.today) parts.push(`${r.field === 'requests' ? 'under' : 'above'} ${r.today} (today)`)
  return parts.join(' · ')
}

// valuesBody is the step's change with the person's values, set-resources'
// shape, for PUT /plans/{id}/steps/{step}.
export function valuesBody(rows: EditableValue[], edits: Record<string, string>): PlanSetResources {
  const byContainer = new Map<string, PlanContainerResources>()
  for (const r of rows) {
    const id = `${r.container}|${!!r.initContainer}`
    const c = byContainer.get(id) ?? { container: r.container, ...(r.initContainer ? { initContainer: true } : {}) }
    c[r.field] = { ...c[r.field], [r.res]: (edits[r.key] ?? r.value).trim() }
    byContainer.set(id, c)
  }
  return { containers: [...byContainer.values()] }
}

const KIND_FOR_TYPE: Record<string, string> = {
  deployments: 'deployment',
  statefulsets: 'statefulset',
  daemonsets: 'daemonset',
}

// rollbackCommand is the kubectl command that puts the values back, from the
// step's own rollback. Only for a workload nobody reconciles: see rollbackFor.
export function rollbackCommand(step: Pick<PlanStep, 'target' | 'rollback'>): string | null {
  const kind = KIND_FOR_TYPE[step.target.type]
  const containers = step.rollback?.params?.containers ?? []
  if (!kind || containers.length === 0) return null
  return containers
    .map((c) => {
      const flags: string[] = []
      const req = [c.requests?.cpu && `cpu=${c.requests.cpu}`, c.requests?.memory && `memory=${c.requests.memory}`].filter(Boolean)
      const lim = [c.limits?.cpu && `cpu=${c.limits.cpu}`, c.limits?.memory && `memory=${c.limits.memory}`].filter(Boolean)
      if (req.length > 0) flags.push(`--requests=${req.join(',')}`)
      if (lim.length > 0) flags.push(`--limits=${lim.join(',')}`)
      const ns = step.target.namespace ? `-n ${step.target.namespace} ` : ''
      return `kubectl ${ns}set resources ${kind}/${step.target.name} -c ${c.container} ${flags.join(' ')}`
    })
    .join('\n')
}

// previousValues says the values the rollback puts back, «cpu request 100m».
function previousValues(step: Pick<PlanStep, 'rollback'>): string {
  const out: string[] = []
  for (const c of step.rollback?.params?.containers ?? []) {
    if (c.requests?.cpu) out.push(`cpu request ${c.requests.cpu}`)
    if (c.requests?.memory) out.push(`memory request ${c.requests.memory}`)
    if (c.limits?.cpu) out.push(`cpu limit ${c.limits.cpu}`)
    if (c.limits?.memory) out.push(`memory limit ${c.limits.memory}`)
  }
  return out.join(', ')
}

export interface Rollback {
  command: string | null
  text: string
}

// rollbackFor says how to undo a step where its owner lets it be undone. A
// workload Helm or GitOps reconciles is undone where it was changed: a
// kubectl command there would be overwritten by the next upgrade or sync,
// back to the plan's value.
export function rollbackFor(step: Pick<PlanStep, 'target' | 'rollback'>): Rollback {
  const owner = step.target.owner
  const was = previousValues(step)
  if (owner?.kind === 'helm' && owner.ref) {
    const ns = owner.namespace || step.target.namespace
    return {
      command: `helm rollback ${owner.ref}${ns ? ` -n ${ns}` : ''}`,
      text: `Rolls back the upgrade that carried the change, to the release's previous revision. Or put ${was || 'the previous values'} back in the values of release ${owner.ref}.`,
    }
  }
  if (owner?.kind === 'gitops') {
    return {
      command: null,
      text: `Revert the change in the repository: the next sync puts ${was || 'the previous values'} back.`,
    }
  }
  return { command: rollbackCommand(step), text: 'Puts back the values the workload has today.' }
}

// applyWhere says where a step's change is made, by its owner.
export function applyWhere(owner?: PlanOwner): string | null {
  if (owner?.kind === 'helm') return `in the values of release ${owner.ref ?? ''}`.trim()
  if (owner?.kind === 'gitops') return 'in the repository'
  return null
}

const CHECK_LABEL: Record<string, string> = {
  rollout_complete: 'rollout completes',
  no_oom_killed: 'no OOMKilled',
  restarts_not_up: 'restarts do not go up',
  p95_under_80pct_of_limit: 'P95 stays under 80% of the limit',
  pool_held: 'the pool stays at its new size',
  no_pending_pods: 'no pod waits for room',
  no_pressure_evictions: 'no pod evicted for pressure',
}

export function checkWords(code: string): string {
  return CHECK_LABEL[code] ?? code.replace(/_/g, ' ')
}

// A check's outcome as a mark and, when it is not a plain pass or fail, why.
export const CHECK_OUTCOME: Record<string, { mark: string; className: string; note?: string }> = {
  ok: { mark: '✓', className: 'text-status-ok' },
  failed: { mark: '✗', className: 'text-status-error' },
  not_applicable: { mark: '–', className: 'text-kb-text-tertiary', note: 'no limit' },
  unknown: { mark: '?', className: 'text-kb-text-tertiary', note: 'no data' },
}

// verifyWindowMs reads a step's verification window («24h»).
export function verifyWindowMs(window: string): number {
  const m = /^(\d+)(h|m|d)$/.exec(window.trim())
  if (!m) return 24 * 3_600_000
  const n = Number(m[1])
  return n * (m[2] === 'd' ? 86_400_000 : m[2] === 'h' ? 3_600_000 : 60_000)
}

// What the observed-impact card draws besides its figure: the measured
// capacity against the estimate, the bill before and after, or how far the
// week of measurement is.
export interface ObservedImpactCard {
  value: string
  unit?: string
  description: string
  caption: string
  highlight?: string
  compare?: { dim: 'cpu' | 'mem'; estimated: number; measured: number }
  bills?: { before: number; after: number; currency: string }
  progress?: { day: number; of: number }
}

// observedImpact is the plan's «observed impact» card (plans-07, P20): the
// measured figures once every applied step is 7 days old; until then how
// far the measurement is («measuring · day 3 of 7»); before anything is
// applied, the baseline.
//
// What the plan freed is not a saving: freed requests sit idle on the same
// nodes (26-nov). So the measured headline is the cluster's bill before and
// after, with every change on the cluster in it, not only the plan's; what
// the plan freed rides in the caption. Without the bill, the freed capacity
// is the figure and its list price is what now sits idle.
export function observedImpact(
  plan: Pick<KobiPlanView, 'impact' | 'steps' | 'baseline' | 'exportedAt'>,
  now = Date.now(),
): ObservedImpactCard {
  const o = plan.impact.observed
  if (o) {
    const freed = freedUnits({ cpuMilli: o.cpuMilliFreed, memBytes: o.memBytesFreed, money: null })
    const bill = o.clusterCost
    if (bill) {
      const change = Math.round((bill.after - bill.before) * 100) / 100
      const what = plan.baseline?.clusterCostCovers === 'bill' ? 'cluster bill' : "cluster's nodes"
      return {
        value: signedPlanMoney(change, bill.currency),
        unit: '/ month',
        bills: { before: bill.before, after: bill.after, currency: bill.currency },
        description: change < 0 ? `on the ${what}, every change on it counted` : `on the ${what}: no saving yet`,
        caption: [
          `${formatPlanMoney(bill.before, bill.currency)} → ${formatPlanMoney(bill.after, bill.currency)}`,
          freed && `${freed} freed`,
          measuredWords(o.windowDays),
        ]
          .filter(Boolean)
          .join(' · '),
      }
    }
    // Without the bill, the figure is how much of the estimate was measured:
    // the «capacity it frees» card already says the estimate, and the same
    // number twice read as two copies of one card. What was freed rides in
    // the sentence, its second unit set apart.
    const fig = freedFigure(o.cpuMilliFreed, o.memBytesFreed)
    const est = plan.impact.estimated
    const dim: 'cpu' | 'mem' = est.cpuMilli > 0 || (est.memBytes <= 0 && o.cpuMilliFreed > 0) ? 'cpu' : 'mem'
    const estimated = dim === 'cpu' ? est.cpuMilli : est.memBytes
    const measured = dim === 'cpu' ? o.cpuMilliFreed : o.memBytesFreed
    const share = estimated > 0 ? Math.round((measured / estimated) * 100) : null
    const head = share !== null ? { value: `${share}%`, unit: 'of the estimate' } : { value: fig.value, unit: fig.unit }
    const said = fig.unit ? `measured: ${fig.value} ${fig.unit} freed${fig.plus ? `, plus ${fig.plus}` : ''}` : 'measured: nothing freed'
    const compare = estimated > 0 ? { dim, estimated, measured } : undefined
    if (o.allocatedCost) {
      const idle = Math.round((o.allocatedCost.before - o.allocatedCost.after) * 100) / 100
      return {
        ...head,
        description: `${said}, idle while the nodes stay`,
        highlight: fig.plus,
        compare,
        caption: [measuredWords(o.windowDays), `${formatPlanMoney(idle, o.allocatedCost.currency)}/month at list prices`].join(' · '),
      }
    }
    return { ...head, description: said, highlight: fig.plus, compare, caption: `${measuredWords(o.windowDays)} · no OpenCost prices` }
  }
  const applied = plan.steps
    .filter((s) => (s.status === 'applied' || s.status === 'verified') && s.appliedAt)
    .map((s) => new Date(s.appliedAt as string).getTime())
  if (applied.length > 0) {
    const day = Math.min(7, Math.floor((now - Math.min(...applied)) / 86_400_000) + 1)
    return { value: '—', description: 'observed impact', caption: `measuring · day ${day} of 7`, progress: { day, of: 7 } }
  }
  return { value: '—', description: 'observed impact', caption: baselineWords(plan) }
}

// freedFigure splits what was freed into a card's figure and unit, and the
// other unit for its description: CPU first, as the «capacity it frees» card.
export function freedFigure(cpuMilli: number, memBytes: number): { value: string; unit?: string; plus?: string } {
  if (cpuMilli > 0) return { value: formatCpuMilli(cpuMilli), unit: 'CPU', plus: memBytes > 0 ? `${formatMemBytes(memBytes)} memory` : undefined }
  if (memBytes > 0) return { value: formatMemBytes(memBytes), unit: 'memory' }
  return { value: '—' }
}

// Kubernetes quantities as millicores and bytes, for the plan's own values
// («100m», «1», «512Mi», «1Gi»); null when one cannot be read.
const BYTE_SUFFIX: Record<string, number> = {
  Ki: 2 ** 10, Mi: 2 ** 20, Gi: 2 ** 30, Ti: 2 ** 40, k: 1e3, K: 1e3, M: 1e6, G: 1e9, T: 1e12, '': 1,
}

export function quantityMilli(q?: string): number | null {
  const m = /^([0-9.]+)(m?)$/.exec(q?.trim() ?? '')
  if (!m) return null
  const n = Number(m[1])
  return Number.isFinite(n) ? (m[2] ? n : n * 1000) : null
}

export function quantityBytes(q?: string): number | null {
  const m = /^([0-9.]+)([KMGT]i|[kKMGT])?$/.exec(q?.trim() ?? '')
  if (!m) return null
  const n = Number(m[1]) * BYTE_SUFFIX[m[2] ?? '']
  return Number.isFinite(n) ? n : null
}

// requestedBefore: what the plan's workloads request today in one dimension,
// per pod times the pods, over the steps the estimate counts. The «capacity
// it frees» chart splits it into what stays and what the plan frees; null
// when a step's value cannot be read.
export function requestedBefore(plan: Pick<KobiPlanView, 'steps'>, dim: 'cpu' | 'mem'): number | null {
  let total = 0
  for (const s of plan.steps) {
    if (s.action !== 'set_resources' || s.status === 'expired') continue
    const pods = Math.max(1, s.evidence?.pods ?? 1)
    // Only the containers whose request in this dimension the step changes:
    // a step that raises a limit frees nothing and is not part of the whole.
    const changed = new Set(
      (s.params?.containers ?? []).filter((c) => (dim === 'cpu' ? c.requests?.cpu : c.requests?.memory)).map((c) => c.container),
    )
    for (const c of s.before?.containers ?? []) {
      if (!changed.has(c.container)) continue
      const raw = dim === 'cpu' ? c.requests?.cpu : c.requests?.memory
      if (!raw) continue
      const v = dim === 'cpu' ? quantityMilli(raw) : quantityBytes(raw)
      if (v === null) return null
      total += v * pods
    }
  }
  return total
}

// measuredWords says how long the impact was measured over («7 days
// measured»); a window under a day (a test stack's) just says measured.
function measuredWords(days: number): string {
  if (days < 1) return 'measured'
  return `${days} ${days === 1 ? 'day' : 'days'} measured`
}

// excludedByReason groups exclusions by reason, in the plan's order.
export function excludedByReason(excluded: PlanExclusion[]): { reason: string; items: PlanExclusion[] }[] {
  const groups = new Map<string, PlanExclusion[]>()
  for (const x of excluded) {
    const list = groups.get(x.reason) ?? []
    list.push(x)
    groups.set(x.reason, list)
  }
  return [...groups.entries()].map(([reason, items]) => ({ reason, items }))
}

// expiresIn: «in 6 days», «today», or null once past.
export function expiresIn(expiresAt: string, now = Date.now()): string | null {
  const ms = new Date(expiresAt).getTime() - now
  if (ms <= 0) return null
  const days = Math.floor(ms / 86_400_000)
  if (days === 0) return 'today'
  return days === 1 ? 'in 1 day' : `in ${days} days`
}

export function planPath(id: string): string {
  return `/plans/${encodeURIComponent(id)}`
}

// baselineWords: what the observed-impact card says about the cost before.
// Before the first export there is none yet; after it, the cluster's bill
// (or, in a plan without it, its workloads' cost) and the days of data it
// averages; without OpenCost the baseline is each step's requests.
export function baselineWords(plan: Pick<KobiPlanView, 'baseline' | 'steps' | 'exportedAt'>): string {
  if (!plan.exportedAt || !plan.baseline) return 'baseline taken at export'
  const days = plan.baseline.windowDays
  const dataWords = days ? ` · ${days} ${days === 1 ? 'day' : 'days'} of data` : ''
  // The cluster's bill is what the measured impact is read against.
  const bill = plan.baseline.clusterCost
  if (bill) {
    const what = plan.baseline.clusterCostCovers === 'bill' ? 'cluster bill' : "cluster's nodes"
    return `${what} before: ${formatPlanMoney(bill.monthly, bill.currency)}/month${dataWords}`
  }
  const costs = plan.steps.map((s) => s.baseline?.allocatedCost).filter((m): m is NonNullable<typeof m> => !!m)
  if (costs.length === 0) return "before: each step's requests"
  const total = Math.round(costs.reduce((sum, m) => sum + m.monthly, 0) * 100) / 100
  return `before: ${formatPlanMoney(total, costs[0].currency)}/month${dataWords}`
}

// ---- Consolidation (plans-16) ----

export const isConsolidation = (plan: Pick<KobiPlanView, 'kind'>) => plan.kind === 'consolidation'

// removeNodesOf reads a consolidation step's change, or null for any other.
export function removeNodesOf(step: Pick<PlanStep, 'action' | 'params'>): PlanRemoveNodes | null {
  if (step.action !== 'remove_nodes' || !step.params) return null
  return step.params as unknown as PlanRemoveNodes
}

export function poolBeforeOf(step: Pick<PlanStep, 'action' | 'before'>): PlanPoolBefore | null {
  if (step.action !== 'remove_nodes' || !step.before) return null
  return step.before as unknown as PlanPoolBefore
}

export function consolidationEvidenceOf(step: Pick<PlanStep, 'action' | 'evidence'>): PlanConsolidationEvidence | null {
  if (step.action !== 'remove_nodes' || !step.evidence) return null
  return step.evidence as unknown as PlanConsolidationEvidence
}

// poolLabel names a pool: «pool general», or «the cluster's nodes» when the
// nodes carry no pool label.
export function poolLabel(name: string): string {
  return name === 'nodes' || !name ? "the cluster's nodes" : `pool ${name}`
}

const POOL_KIND: Record<string, string> = {
  eks: 'node group',
  aks: 'node pool',
  gke: 'node pool',
  karpenter: 'Karpenter NodePool',
  none: 'nodes',
}

export function poolKindWords(source: string): string {
  return POOL_KIND[source] ?? 'node pool'
}

// autoscalerWords: what removes such a node on its own, in words.
export function autoscalerWords(autoscaler: string): string {
  switch (autoscaler) {
    case 'cluster_autoscaler':
      return 'The Cluster Autoscaler runs here: it removes such a node on its own once its requests stay under its threshold (50% by default).'
    case 'karpenter':
      return 'Karpenter sizes this pool: with consolidation on it removes such a node on its own.'
  }
  return 'No autoscaler: nobody removes it unless a person does.'
}

// blockWords says why a node keeps a pod, as the plan's exclusions do.
export function blockWords(b: PlanNodeBlock): string {
  switch (b.reason) {
    case 'pdb':
      return `the PodDisruptionBudget ${b.owner} allows no disruption now, so ${b.pod} keeps ${b.node}`
    case 'no_controller':
      return `${b.pod} has no controller to start it again elsewhere, so it keeps ${b.node}`
    case 'local_storage':
      return `${b.owner} keeps data on the node's own disk, so it keeps ${b.node}`
    case 'safe_to_evict_false':
      return `${b.owner} is marked not to be evicted, so it keeps ${b.node}`
    case 'no_place':
      return `a pod of ${b.owner} has nowhere to go among the nodes that stay: its scheduling rules (one per node, node affinity, host ports, selector or taints) leave none, so it keeps ${b.node}`
    case 'unsimulated_rules':
      return `${b.owner} has scheduling rules the plan does not simulate (pod affinity, a hard topology spread or anti-affinity across zones), so it keeps ${b.node}`
  }
  return `${b.pod} keeps ${b.node}`
}

// pendingDependencies: the plans this one waits for with steps still to do.
export function pendingDependencies(plan: Pick<KobiPlanView, 'dependsOnPlans'>): PlanLink[] {
  return (plan.dependsOnPlans ?? []).filter(
    (d) => (d.effectiveStatus === 'draft' || d.effectiveStatus === 'exported') && d.appliedCount < d.stepCount,
  )
}

// stepWaits: a consolidation step computed on a right-sizing plan's requests
// waits while that plan has steps to do.
export function stepWaits(plan: Pick<KobiPlanView, 'dependsOnPlans'>, step: PlanStep): boolean {
  return step.status === 'pending' && step.riskReasons.includes('depends_on_plan') && pendingDependencies(plan).length > 0
}

// nodesWords: «5 → 4 nodes» over a plan's steps.
export function nodeCounts(plan: Pick<KobiPlanView, 'steps'>): { before: number; after: number; pools: string[] } {
  let before = 0
  let after = 0
  const pools: string[] = []
  for (const s of plan.steps) {
    const p = removeNodesOf(s)
    if (!p || s.status === 'expired') continue
    before += p.nodesBefore
    after += p.nodesAfter
    pools.push(p.pool)
  }
  return { before, after, pools }
}

// windowWords: a verification window in words, «7d» → «7 days».
export function windowWords(window: string): string {
  const m = /^(\d+)(d|h)$/.exec(window.trim())
  if (!m) return window
  const n = Number(m[1])
  return m[2] === 'd' ? `${n} ${n === 1 ? 'day' : 'days'}` : `${n} ${n === 1 ? 'hour' : 'hours'}`
}

// formatShare writes an engine share (0–100) as a percentage.
export function formatShare(v: number): string {
  return `${Math.round(v)}%`
}
