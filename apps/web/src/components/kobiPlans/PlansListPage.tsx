import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ClipboardList } from 'lucide-react'
import { api } from '@/services/api'
import { useCopilot } from '@/contexts/CopilotContext'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { KPI_COLOR } from '@/components/shared/kpi/MiniCharts'
import { TD, TD_NUM, TR, Th } from '@/components/shared/table/Th'
import type { KobiPlanSummary, PlanRisk, PlanStatus } from '@/types/kobiPlans'
import { MINUS, STATUS_BADGE_CLASS, formatPlanMoney, freedParts, planPath, statusWords } from '@/utils/kobiPlans'
import { ResourceTypeIcon } from '@/utils/resourceIcons'
import { BetaPill, PlansFeedbackLink } from './PlansBeta'
import { BillPathPanel } from './BillPathPanel'
import { eeConsolidationPlans } from '@/ee/registry'

// PlansListPage — every Kobi Plan the person may read (GET /plans). A bridge:
// the design puts plans in the Kobi Workspace's case file (plans-10, after
// R3); until then this page is where a plan is found again once its
// conversation is closed. Global, so it answers when a cluster is down.

const LIVE: PlanStatus[] = ['draft', 'exported']
const SOON_MS = 48 * 3_600_000
const RISK_COLOR: Record<PlanRisk, string> = { low: KPI_COLOR.ok, medium: KPI_COLOR.warn, high: KPI_COLOR.err }

type Filter = 'live' | 'all'

// Everything a plan frees: the money when there is a price, and the CPU and
// memory always (they are the change itself; the money is their list price,
// not a saving: the bill falls only once nodes or disks go). Each unit holds
// together, so a narrow column breaks between them.
function Frees({ plan }: { plan: KobiPlanSummary }) {
  const money = plan.estimated.money
  // A consolidation plan's figure is the bill: the nodes it removes.
  if (plan.kind === 'consolidation') {
    const n = plan.nodesRemoved ?? 0
    return (
      <>
        {money && (
          <div className="whitespace-nowrap text-status-ok" title="The removed nodes' list price: less with Savings Plans or reservations">
            {MINUS}
            {formatPlanMoney(money.monthly, money.currency)}/mo bill
          </div>
        )}
        <div className={money ? 'text-[10px] text-kb-text-tertiary' : 'text-kb-text-primary'}>
          {n} {n === 1 ? 'node' : 'nodes'}
        </div>
      </>
    )
  }
  const parts = freedParts(plan.estimated)
  if (!money && parts.length === 0) return <span className="text-kb-text-tertiary">—</span>
  return (
    <>
      {money && (
        <div className="text-kb-text-primary whitespace-nowrap" title="List price of the capacity freed: a saving only once nodes or disks are removed">
          {formatPlanMoney(money.monthly, money.currency)}/mo
        </div>
      )}
      {parts.length > 0 && (
        <div className={money ? 'text-[10px] text-kb-text-tertiary' : 'text-kb-text-primary'}>
          {parts.map((u, i) => (
            <span key={u}>
              {i > 0 && ' · '}
              <span className="whitespace-nowrap">{u}</span>
            </span>
          ))}
        </div>
      )}
    </>
  )
}

// How many steps there are, and below how many are already made: a plan
// under way reads apart from one nobody touched.
function Steps({ plan, waits }: { plan: KobiPlanSummary; waits: boolean }) {
  const applied = plan.appliedCount ?? 0
  const all = applied > 0 && applied >= plan.stepCount
  return (
    <>
      <div className="text-kb-text-secondary">{plan.stepCount}</div>
      {waits && applied === 0 && (
        <div className="text-[10px]" style={{ color: KPI_COLOR.warn }}>
          waits for right-sizing
        </div>
      )}
      {applied > 0 && (
        <div className={`text-[10px] ${all ? 'text-status-ok' : 'text-kb-text-tertiary'}`}>{all ? 'all applied' : `${applied} applied`}</div>
      )}
    </>
  )
}

// Related: the plans a row is tied to (plans-16), each opening its plan.
function Related({ label, plans }: { label: string; plans?: { id: string; title: string }[] }) {
  if (!plans?.length) return null
  return (
    <div className="text-[11px] text-kb-text-tertiary">
      {label}{' '}
      {plans.map((r, i) => (
        <span key={r.id}>
          {i > 0 && ', '}
          <Link to={planPath(r.id)} onClick={(e) => e.stopPropagation()} className="text-kb-accent hover:underline">
            {r.title}
          </Link>
        </span>
      ))}
    </div>
  )
}

// waitsFor: a consolidation plan whose right-sizing plan still has steps to do.
function waitsFor(p: KobiPlanSummary, byId: Map<string, KobiPlanSummary>): boolean {
  if (p.kind !== 'consolidation' || !(p.effectiveStatus === 'draft' || p.effectiveStatus === 'exported')) return false
  return (p.dependsOn ?? []).some((d) => {
    const dep = byId.get(d.id)
    return !!dep && (dep.effectiveStatus === 'draft' || dep.effectiveStatus === 'exported') && (dep.appliedCount ?? 0) < dep.stepCount
  })
}

const shortDate = (iso: string) => new Date(iso).toLocaleDateString(undefined, { day: 'numeric', month: 'short' })

// A draft's expiry, in amber inside the last 48 hours (the headline's bar);
// a draft nobody exported, the day it expired. Any other plan does not
// expire: exported, or ended by a person (discarded).
function Expires({ plan }: { plan: KobiPlanSummary }) {
  if (plan.effectiveStatus === 'expired' && plan.effectiveExpiredReason === 'unexported') {
    return <span className="text-kb-text-tertiary">{shortDate(plan.expiresAt)}</span>
  }
  if (plan.effectiveStatus !== 'draft') return <span className="text-kb-text-tertiary">—</span>
  const soon = new Date(plan.expiresAt).getTime() - Date.now() < SOON_MS
  return <span className={soon ? 'text-status-warn' : 'text-kb-text-secondary'}>{shortDate(plan.expiresAt)}</span>
}

export function PlansListPage() {
  const [filter, setFilter] = useState<Filter>('live')
  const [cluster, setCluster] = useState('')
  const { openPanel } = useCopilot()
  const navigate = useNavigate()

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['kobi-plans', filter, cluster],
    queryFn: () => api.listPlans({ status: filter === 'live' ? LIVE : undefined, cluster: cluster || undefined, limit: 200 }),
  })
  // The live count for the headline, whatever the filter shows.
  const { data: live } = useQuery({
    queryKey: ['kobi-plans', 'live', ''],
    queryFn: () => api.listPlans({ status: LIVE, limit: 200 }),
  })
  const { data: clusters } = useQuery({ queryKey: ['clusters'], queryFn: api.listClusters })

  const clusterOptions = useMemo(() => {
    const seen = new Map<string, string>()
    for (const c of clusters ?? []) if (c.clusterId) seen.set(c.clusterId, c.displayName || c.name)
    for (const p of [...(data?.plans ?? []), ...(live?.plans ?? [])]) if (!seen.has(p.clusterId)) seen.set(p.clusterId, p.clusterName || p.clusterId)
    return [...seen.entries()].sort((a, b) => a[1].localeCompare(b[1]))
  }, [clusters, data, live])

  const livePlans = live?.plans ?? []
  const byId = new Map([...livePlans, ...(data?.plans ?? [])].map((p) => [p.id, p]))
  // The bill's path: the cluster picked, the only one with live plans, or,
  // with several, the fleet's.
  const liveClusters = [...new Set(livePlans.map((p) => p.clusterId))]
  const pathCluster = cluster || (liveClusters.length === 1 ? liveClusters[0] : '')
  const fleetPath = !pathCluster && liveClusters.length > 1
  const soon = livePlans.filter((p) => p.effectiveStatus === 'draft' && new Date(p.expiresAt).getTime() - Date.now() < SOON_MS).length
  const clustersWithPlans = new Set(livePlans.map((p) => p.clusterId)).size

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-2">
            <ResourceTypeIcon type="plans" />
            <h1 className="text-lg font-semibold text-kb-text-primary">Plans</h1>
            <BetaPill />
          </div>
          <p className="text-[13px] text-kb-text-secondary">
            Reviewable drafts of changes that Kobi builds from KubeBolt&apos;s engines. Ask Kobi for one in the chat; nothing is applied until you do.
          </p>
        </div>
        <PlansFeedbackLink />
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <KpiCard primary value={livePlans.length} unit={livePlans.length === 1 ? 'plan' : 'plans'} description="live" caption="drafts and exported" />
        <KpiCard value={clustersWithPlans} unit={clustersWithPlans === 1 ? 'cluster' : 'clusters'} description="with a live plan" caption="across the organization" />
        <KpiCard value={soon} unit={soon === 1 ? 'draft' : 'drafts'} description="expire within 48 hours" caption="unless exported" alert={soon > 0 ? 'warn' : undefined} />
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <div className="inline-flex rounded-lg border border-kb-border p-0.5" role="tablist" aria-label="Which plans">
          {(['live', 'all'] as Filter[]).map((f) => (
            <button
              key={f}
              type="button"
              role="tab"
              aria-selected={filter === f}
              onClick={() => setFilter(f)}
              className={`px-3 py-1 rounded-md text-[12px] transition-colors ${filter === f ? 'bg-kb-elevated text-kb-text-primary' : 'text-kb-text-tertiary hover:text-kb-text-primary'}`}
            >
              {f === 'live' ? 'Live' : 'All'}
            </button>
          ))}
        </div>
        <select
          aria-label="Cluster"
          value={cluster}
          onChange={(e) => setCluster(e.target.value)}
          className="h-8 px-2 rounded-lg border border-kb-border bg-kb-card text-[12px] text-kb-text-primary"
        >
          <option value="">All clusters</option>
          {clusterOptions.map(([id, name]) => (
            <option key={id} value={id}>
              {name}
            </option>
          ))}
        </select>
      </div>

      {/* The bill's path comes with the editions that have consolidation (P22). */}
      {eeConsolidationPlans && (pathCluster || fleetPath) && <BillPathPanel clusterId={pathCluster || undefined} onPickCluster={setCluster} />}

      {isLoading ? (
        <LoadingSpinner />
      ) : error ? (
        <ErrorState message="The plans could not be read" onRetry={() => refetch()} />
      ) : (data?.plans ?? []).length === 0 ? (
        <div className="kb-panel flex flex-col items-center justify-center text-center gap-3 py-16">
          <ClipboardList className="w-8 h-8 text-kb-text-tertiary" />
          <div className="text-[15px] font-semibold text-kb-text-primary">{filter === 'live' ? 'No live plans' : 'No plans yet'}</div>
          <p className="text-[13px] text-kb-text-secondary max-w-md">
            Ask Kobi for a right-sizing plan for a cluster or a namespace: it builds a draft you review here before anything changes.
          </p>
          <button
            type="button"
            onClick={openPanel}
            className="h-9 px-3.5 rounded-lg bg-kb-accent text-white text-[13px] font-semibold hover:opacity-90 transition-opacity"
          >
            Ask Kobi for a plan
          </button>
        </div>
      ) : (
        <div className="kb-panel overflow-hidden">
          <div className="overflow-x-auto">
            {/* Fleet's table grammar (shared/table): names in sans, every
                other column mono, numbers on the right, status as a filled
                chip, the whole row opens the plan. */}
            <table className="w-full text-left">
              <thead>
                <tr className="border-b border-kb-border">
                  <Th>Plan</Th>
                  <Th>Cluster</Th>
                  <Th>Status</Th>
                  <Th className="hidden md:table-cell">Max risk</Th>
                  <Th right>Steps</Th>
                  <Th right>Frees</Th>
                  <Th className="hidden lg:table-cell">Created</Th>
                  <Th>Expires</Th>
                </tr>
              </thead>
              <tbody>
                {data!.plans.map((p) => (
                  <tr key={p.id} onClick={() => navigate(planPath(p.id))} className={TR}>
                    <td className="px-3 py-2.5 min-w-[200px]">
                      <Link
                        to={planPath(p.id)}
                        onClick={(e) => e.stopPropagation()}
                        className="block text-xs leading-snug text-kb-text-primary hover:text-kb-accent"
                      >
                        {p.title}
                      </Link>
                      <div className="text-[10px] font-mono text-kb-text-tertiary">
                        {p.kind} · v{p.version}
                      </div>
                      <Related label="depends on" plans={p.dependsOn} />
                      <Related label="leads to" plans={p.leadsTo} />
                    </td>
                    <td className={`${TD} text-kb-text-secondary whitespace-nowrap`}>{p.clusterName || p.clusterId}</td>
                    <td className="px-3 py-2.5 whitespace-nowrap">
                      <span className={`px-2 py-0.5 rounded-full text-[10px] font-mono ${STATUS_BADGE_CLASS[p.effectiveStatus]}`}>
                        {statusWords(p.effectiveStatus, p.effectiveExpiredReason).toUpperCase()}
                      </span>
                    </td>
                    <td className={`${TD} text-kb-text-secondary whitespace-nowrap hidden md:table-cell`}>
                      {p.maxRisk ? (
                        <span className="inline-flex items-center gap-1.5">
                          <span className="w-1.5 h-1.5 rounded-full" style={{ background: RISK_COLOR[p.maxRisk] }} />
                          {p.maxRisk}
                        </span>
                      ) : (
                        <span className="text-kb-text-tertiary">—</span>
                      )}
                    </td>
                    <td className={`${TD_NUM} whitespace-nowrap`}>
                      <Steps plan={p} waits={waitsFor(p, byId)} />
                    </td>
                    <td className={TD_NUM}>
                      <Frees plan={p} />
                    </td>
                    <td className={`${TD} text-kb-text-secondary whitespace-nowrap hidden lg:table-cell`}>
                      {shortDate(p.createdAt)}
                      {p.createdBy && <div className="text-[10px] text-kb-text-tertiary">{p.createdBy}</div>}
                    </td>
                    <td className={`${TD} whitespace-nowrap`}>
                      <Expires plan={p} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}
