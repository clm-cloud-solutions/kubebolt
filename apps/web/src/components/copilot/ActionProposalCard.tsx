import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  Play,
  X as XIcon,
  Loader2,
  CheckCircle2,
  AlertTriangle,
  Wrench,
  ShieldAlert,
  ExternalLink,
  Trash2,
} from 'lucide-react'
import { api, ApiError } from '@/services/api'
import { useCopilot } from '@/contexts/CopilotContext'
import type { ActionProposal } from '@/services/copilot/types'

type Status = 'pending' | 'executing' | 'success' | 'error' | 'dismissed'

interface Props {
  proposal: ActionProposal
  /** ID of the tool result that carried this proposal. Used to record the
   * user's outcome (Execute/Dismiss) back into that message so the LLM
   * sees it on the next turn and doesn't re-propose the same action. */
  toolCallId: string
}

/**
 * ActionProposalCard renders an LLM-emitted mutation proposal as an
 * interactive confirmation card. The LLM never executes — the user must
 * click Execute, which calls the existing mutation endpoint under their
 * RBAC role. The X-KubeBolt-Action-Source header tags the resulting
 * audit log entry so we can distinguish UI clicks from approved Copilot
 * proposals.
 *
 * On success, the user is navigated to the resource detail page on the
 * Pods tab (for workload actions) so they can watch the action take
 * effect, while the card stays in chat showing live rollout/scale
 * progress until completion or timeout.
 */
export function ActionProposalCard({ proposal, toolCallId }: Props) {
  const navigate = useNavigate()
  const { recordProposalOutcome } = useCopilot()
  // Seed the local status from any persisted execution metadata. If the
  // chat re-renders this card after the user already acted (or after a
  // session compaction), we honor that and never re-offer Execute on a
  // resolved proposal.
  const initialStatus: Status =
    proposal.executionStatus === 'executed'
      ? 'success'
      : proposal.executionStatus === 'failed'
        ? 'error'
        : proposal.executionStatus === 'dismissed'
          ? 'dismissed'
          : 'pending'
  const [status, setStatus] = useState<Status>(initialStatus)
  const [error, setError] = useState<string | null>(
    proposal.executionStatus === 'failed' ? proposal.executionResult ?? 'previous attempt failed' : null,
  )
  const [resultMsg, setResultMsg] = useState<string | null>(
    proposal.executionStatus === 'executed' ? proposal.executionResult ?? 'Done' : null,
  )

  const { action, target, params, summary, rationale, risk, reversible } = proposal

  async function execute() {
    setStatus('executing')
    setError(null)
    try {
      const result = await runProposal(proposal)
      setResultMsg(result)
      setStatus('success')
      recordProposalOutcome(toolCallId, 'executed', result)
      // Take the user to the place where they can watch the action land.
      // Workload actions go to the "Pods" tab so they see pods cycling;
      // rollbacks go there too (the new=old RS spawns fresh pods); deletes
      // go to the resource-type LIST (the detail page is gone now).
      if (
        action === 'restart_workload' ||
        action === 'scale_workload' ||
        action === 'rollback_deployment'
      ) {
        const ns = target.namespace || '_'
        navigate(`/${target.type}/${ns}/${target.name}?tab=${podsTabId(target.type)}`)
      } else if (action === 'delete_resource') {
        navigate(`/${target.type}`)
      }
    } catch (e) {
      const msg =
        e instanceof ApiError
          ? e.status === 403
            ? `Forbidden — your role does not allow this action. Ask an Editor or Admin to approve.`
            : e.status === 404
              ? `Target ${target.namespace}/${target.name} no longer exists. The cluster may have changed since this was proposed.`
              : e.status === 503
                ? `Cluster is unreachable. Try again once the connection is restored.`
                : e.message
          : e instanceof Error
            ? e.message
            : 'Unknown error'
      setError(msg)
      setStatus('error')
      recordProposalOutcome(toolCallId, 'failed', msg)
    }
  }

  function dismiss() {
    setStatus('dismissed')
    recordProposalOutcome(toolCallId, 'dismissed')
  }

  // High-risk proposals (delete) require the user to type the exact
  // namespace/name before Execute is enabled. Same pattern as the native
  // Delete modal in the Resource Detail Page — familiar muscle memory.
  const requireTyping = risk === 'high'
  const confirmExpected = `${target.namespace || '_'}/${target.name}`
  const [confirmText, setConfirmText] = useState('')
  const confirmMatched = !requireTyping || confirmText.trim() === confirmExpected

  // Pull blast radius (if present) out of params so we can render it as
  // its own visual section and keep the params dump clean.
  const blastRadiusRaw = params.blastRadius
  const otherParams = Object.fromEntries(
    Object.entries(params).filter(([k]) => k !== 'blastRadius'),
  )

  if (status === 'dismissed') {
    return (
      <div className="text-[10px] font-mono text-kb-text-tertiary italic px-2 py-1">
        Proposal dismissed: {summary}
      </div>
    )
  }

  // Risk-driven accent. Low = accent green, medium = warn amber, high = error red.
  const accentClasses =
    risk === 'high'
      ? 'border-status-error/40 bg-status-error-dim/30'
      : risk === 'medium'
        ? 'border-status-warn/40 bg-status-warn-dim/30'
        : 'border-kb-accent/40 bg-kb-accent-light/30'

  // The card always sits on a tinted background (green / amber / red, all
  // at low alpha). The default tertiary token (#555770 in dark) loses
  // contrast on every one of them — first observed on high-risk red, but
  // it's just as bad on medium-risk amber. Use secondary (#8b8d9a)
  // uniformly for all muted text inside the card; it's still clearly a
  // "less important" gray but readable on every tint and in both themes.
  const headerMutedClass = 'text-kb-text-secondary'

  const detailPath = buildDetailPath(target.type, target.namespace, target.name)
  const detailPathPods = detailPath ? `${detailPath}?tab=${podsTabId(target.type)}` : null

  return (
    <div
      className={`max-w-[95%] rounded-xl border ${accentClasses} px-3 py-2.5 my-1 flex flex-col gap-2`}
    >
      {/* Header */}
      <div className="flex items-start gap-2">
        <div className="w-6 h-6 rounded-lg bg-kb-bg flex items-center justify-center shrink-0 mt-0.5">
          <Wrench className="w-3.5 h-3.5 text-kb-accent" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-1.5 flex-wrap">
            <span className={`text-[9px] font-mono ${headerMutedClass} uppercase tracking-wider`}>
              Kobi proposes
            </span>
            <RiskBadge risk={risk} />
            {!reversible && (
              <span className="text-[9px] font-mono text-status-warn flex items-center gap-1">
                <ShieldAlert className="w-3 h-3" />
                irreversible
              </span>
            )}
          </div>
          <div className="text-xs font-semibold text-kb-text-primary mt-0.5 break-words">
            {summary}
          </div>
          <div className={`text-[10px] font-mono ${headerMutedClass} mt-0.5`}>
            {target.type} ·{' '}
            {detailPath ? (
              <Link
                to={detailPath}
                className="hover:text-kb-accent inline-flex items-center gap-0.5"
              >
                {target.namespace}/{target.name}
                <ExternalLink className="w-2.5 h-2.5" />
              </Link>
            ) : (
              <span>
                {target.namespace}/{target.name}
              </span>
            )}
          </div>
        </div>
      </div>

      {/* Rationale */}
      {rationale && (
        <div className="text-[11px] text-kb-text-secondary leading-snug border-l-2 border-kb-border pl-2 ml-1">
          {rationale}
        </div>
      )}

      {/* Params (e.g., replicas) — blastRadius is rendered separately below */}
      {Object.keys(otherParams).length > 0 && (
        <div className="flex flex-wrap gap-1.5 ml-1">
          {Object.entries(otherParams).map(([k, v]) => (
            <span
              key={k}
              className="text-[10px] font-mono text-kb-text-secondary bg-kb-bg/60 px-1.5 py-0.5 rounded border border-kb-border"
            >
              {k}: <span className="text-kb-text-primary">{String(v)}</span>
            </span>
          ))}
        </div>
      )}

      {/* Blast radius — only on delete proposals (or any proposal that ships
          one). Concrete consequences are far more useful than a generic
          "irreversible" badge: shows pod counts, affected services, HPAs,
          orphaned PVCs, etc. */}
      {!!blastRadiusRaw && typeof blastRadiusRaw === 'object' && (
        <BlastRadiusPreview blast={blastRadiusRaw as Record<string, unknown>} />
      )}

      {/* Typing-to-confirm — only for risk=high (delete and similarly
          dangerous future actions). Mirrors the native Delete modal pattern. */}
      {status === 'pending' && requireTyping && (
        <div className="flex flex-col gap-1.5 mt-1 ml-1">
          <label className="text-[10px] font-mono text-kb-text-secondary">
            Type{' '}
            <span className="font-bold text-kb-text-primary bg-kb-bg/60 px-1 rounded border border-kb-border">
              {confirmExpected}
            </span>{' '}
            to enable Execute:
          </label>
          <input
            type="text"
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            placeholder={confirmExpected}
            className="px-2 py-1 rounded border border-kb-border bg-kb-bg text-[11px] font-mono text-kb-text-primary placeholder:text-kb-text-tertiary focus:outline-none focus:border-status-error/60"
            autoComplete="off"
            spellCheck={false}
          />
        </div>
      )}

      {/* Action footer */}
      {status === 'pending' && (
        <div className="flex items-center gap-2 mt-1">
          <button
            onClick={execute}
            disabled={!confirmMatched}
            className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-white text-[11px] font-medium transition-colors disabled:opacity-40 disabled:cursor-not-allowed ${
              risk === 'high'
                ? 'bg-status-error hover:bg-status-error/90'
                : 'bg-kb-accent hover:bg-kb-accent/90'
            }`}
          >
            {risk === 'high' ? <Trash2 className="w-3 h-3" /> : <Play className="w-3 h-3" />}
            Execute · {action.replace(/_/g, ' ')}
          </button>
          <button
            onClick={dismiss}
            className={`flex items-center gap-1 px-2.5 py-1.5 rounded-lg ${headerMutedClass} hover:text-kb-text-primary hover:bg-kb-elevated text-[11px] transition-colors`}
          >
            <XIcon className="w-3 h-3" />
            Dismiss
          </button>
        </div>
      )}

      {status === 'executing' && (
        <div className="flex items-center gap-2 text-[11px] text-kb-text-secondary mt-1">
          <Loader2 className="w-3.5 h-3.5 animate-spin text-kb-accent" />
          Executing...
        </div>
      )}

      {status === 'success' && (
        <div className="flex flex-col gap-1.5 mt-1">
          <div className="flex items-center gap-2 text-[11px] text-status-ok">
            <CheckCircle2 className="w-3.5 h-3.5" />
            {resultMsg ?? 'Done'}
          </div>
          {/* Live progress only for actions that have an observable rollout
              AND only while the polling hasn't already settled. Delete
              leaves nothing to poll. progressSettled is set the first time
              the poller reaches its terminal state and persists into the
              proposal — protects against a re-mount restarting polling
              against a cluster that has since moved on (e.g. another scale
              was issued in a later turn). */}
          {action !== 'delete_resource' && !proposal.progressSettled && (
            <WorkloadProgress proposal={proposal} toolCallId={toolCallId} />
          )}
          {action === 'delete_resource' ? (
            <Link
              to={`/${target.type}`}
              className="self-start text-[10px] font-mono text-kb-accent hover:underline inline-flex items-center gap-1"
            >
              <ExternalLink className="w-2.5 h-2.5" />
              View {target.type}
            </Link>
          ) : (
            detailPathPods && (
              <Link
                to={detailPathPods}
                className="self-start text-[10px] font-mono text-kb-accent hover:underline inline-flex items-center gap-1"
              >
                <ExternalLink className="w-2.5 h-2.5" />
                View pods
              </Link>
            )
          )}
        </div>
      )}

      {status === 'error' && (
        <div className="flex flex-col gap-1.5 mt-1">
          <div className="flex items-start gap-2 text-[11px] text-status-error">
            <AlertTriangle className="w-3.5 h-3.5 shrink-0 mt-0.5" />
            <span className="break-words">{error}</span>
          </div>
          <button
            onClick={execute}
            className={`self-start text-[10px] font-mono ${headerMutedClass} hover:text-kb-accent underline`}
          >
            Retry
          </button>
        </div>
      )}
    </div>
  )
}

function RiskBadge({ risk }: { risk: ActionProposal['risk'] }) {
  const cls =
    risk === 'high'
      ? 'text-status-error bg-status-error-dim'
      : risk === 'medium'
        ? 'text-status-warn bg-status-warn-dim'
        : 'text-kb-accent bg-kb-accent-light'
  return (
    <span className={`text-[9px] font-mono uppercase tracking-wider px-1.5 py-0.5 rounded ${cls}`}>
      {risk} risk
    </span>
  )
}

// runProposal dispatches the proposal to the matching mutation endpoint.
// New action types added to the backend whitelist must be added here too —
// keeping this switch exhaustive is what enforces the frontend whitelist.
async function runProposal(p: ActionProposal): Promise<string> {
  const SOURCE = 'copilot_proposal'
  switch (p.action) {
    case 'restart_workload': {
      const r = await api.restartResource(p.target.type, p.target.namespace, p.target.name, SOURCE)
      return `Restart triggered (${r.status})`
    }
    case 'scale_workload': {
      const replicas = Number(p.params.replicas)
      if (!Number.isFinite(replicas) || replicas < 0) {
        throw new Error('invalid replicas in proposal')
      }
      const r = await api.scaleResource(
        p.target.type,
        p.target.namespace,
        p.target.name,
        replicas,
        SOURCE,
      )
      return `Scaled ${r.fromReplicas} → ${r.toReplicas}`
    }
    case 'rollback_deployment': {
      const toRevision = Number(p.params.toRevision)
      const r = await api.rollbackResource(
        p.target.type,
        p.target.namespace,
        p.target.name,
        Number.isFinite(toRevision) && toRevision > 0 ? toRevision : undefined,
        SOURCE,
      )
      return `Rolled back: revision ${r.fromRevision} → ${r.toRevision}`
    }
    case 'delete_resource': {
      const force = Boolean(p.params.force)
      const orphan = Boolean(p.params.orphan)
      const r = await api.deleteResource(p.target.type, p.target.namespace, p.target.name, {
        force,
        orphan,
        source: SOURCE,
      })
      return `Deleted ${p.target.type}/${p.target.namespace}/${p.target.name} (${r.status})`
    }
    default:
      throw new Error(`unsupported action: ${p.action}`)
  }
}

// ─── Blast radius preview ──────────────────────────────────────────
//
// Renders the consequences computed by the backend (cluster/blast_radius.go)
// as a readable list of "what will happen" bullets. Only renders sections
// that have data — keeps the card compact for resources with narrow blast
// radius (e.g. an orphan ConfigMap nobody mounts).

interface BlastRadiusData {
  ownedPods?: number
  ownedPodNames?: string[]
  affectedServices?: string[]
  affectedHPAs?: string[]
  orphanedPVCs?: string[]
  usingPods?: string[]
  affectedIngresses?: string[]
  notes?: string[]
}

function BlastRadiusPreview({ blast }: { blast: Record<string, unknown> }) {
  const b = blast as BlastRadiusData
  const items: { icon: 'warn' | 'info'; text: string; detail?: string[] }[] = []

  if (b.ownedPods && b.ownedPods > 0) {
    items.push({
      icon: 'warn',
      text: `${b.ownedPods} pod${b.ownedPods === 1 ? '' : 's'} will be terminated`,
      detail: b.ownedPodNames,
    })
  }
  if (b.affectedServices && b.affectedServices.length > 0) {
    items.push({
      icon: 'warn',
      text: `${b.affectedServices.length} Service${b.affectedServices.length === 1 ? '' : 's'} will be left without endpoints`,
      detail: b.affectedServices,
    })
  }
  if (b.affectedHPAs && b.affectedHPAs.length > 0) {
    items.push({
      icon: 'warn',
      text: `${b.affectedHPAs.length} HPA${b.affectedHPAs.length === 1 ? '' : 's'} will be orphaned`,
      detail: b.affectedHPAs,
    })
  }
  if (b.orphanedPVCs && b.orphanedPVCs.length > 0) {
    items.push({
      icon: 'info',
      text: `${b.orphanedPVCs.length} PVC${b.orphanedPVCs.length === 1 ? '' : 's'} will be retained (data preserved)`,
      detail: b.orphanedPVCs,
    })
  }
  if (b.usingPods && b.usingPods.length > 0) {
    items.push({
      icon: 'warn',
      text: `${b.usingPods.length} pod${b.usingPods.length === 1 ? '' : 's'} reference this resource`,
      detail: b.usingPods,
    })
  }
  if (b.affectedIngresses && b.affectedIngresses.length > 0) {
    items.push({
      icon: 'warn',
      text: `${b.affectedIngresses.length} Ingress${b.affectedIngresses.length === 1 ? '' : 'es'} will lose this backend/cert`,
      detail: b.affectedIngresses,
    })
  }

  if (items.length === 0 && (!b.notes || b.notes.length === 0)) {
    return null
  }

  // Note on text colors: the panel sits on a tinted red background
  // (bg-status-error-dim/20). Default tertiary/secondary tokens lose
  // contrast there in dark mode (#555770 over translucent red ≈
  // illegible — see issue noted during PoC validation). We pick colors
  // that work on both light and dark by using the status-error palette
  // with reduced opacity for muted text and a high-contrast primary for
  // body copy, instead of the generic kb-text-* tokens.
  return (
    <div className="rounded-md border border-status-error/30 bg-status-error-dim/20 px-2.5 py-2 ml-1 flex flex-col gap-1.5">
      <div className="flex items-center gap-1.5 text-[10px] font-mono text-status-error uppercase tracking-wider">
        <AlertTriangle className="w-3 h-3" />
        What will happen
      </div>
      <ul className="flex flex-col gap-1 text-[11px] text-kb-text-primary">
        {items.map((it, i) => (
          <li key={i} className="flex flex-col gap-0.5">
            <span className="flex items-start gap-1.5">
              <span className={it.icon === 'warn' ? 'text-status-error' : 'text-status-error/60'}>
                •
              </span>
              <span>{it.text}</span>
            </span>
            {it.detail && it.detail.length > 0 && (
              <span className="text-[10px] font-mono text-status-error/80 ml-3 break-words">
                {it.detail.slice(0, 5).join(', ')}
                {it.detail.length > 5 ? `, ... +${it.detail.length - 5} more` : ''}
              </span>
            )}
          </li>
        ))}
      </ul>
      {b.notes && b.notes.length > 0 && (
        <ul className="flex flex-col gap-1 mt-1 pt-1.5 border-t border-status-error/20 text-[10px] text-kb-text-primary/80 leading-snug">
          {b.notes.map((note, i) => (
            <li key={i} className="flex items-start gap-1.5">
              <span className="text-status-error/70">ℹ</span>
              <span>{note}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

// ─── Live progress ──────────────────────────────────────────────────
//
// After Execute succeeds, WorkloadProgress polls the workload's detail
// every few seconds and renders a one-line status: "Rollout: 2/3 pods
// updated · 2/3 ready" or "Scale: 2/3 ready". Polling stops when the
// action is complete or after PROGRESS_TIMEOUT_MS, whichever comes first,
// so we don't burn requests forever on a stuck rollout.

const PROGRESS_TIMEOUT_MS = 90_000
const PROGRESS_POLL_MS = 2_500

interface WorkloadCounts {
  desired: number // spec.replicas — what we want
  ready: number // status.readyReplicas — pods passing readiness probe
  updated: number // status.updatedReplicas — pods matching latest template
  // ACTUAL pod count from the API server (any phase, including Terminating).
  // The connector sets this via len(GetXPods()) which uses the pod lister.
  // Crucially DIFFERENT from status.replicas: K8s computes status.replicas
  // excluding pods with DeletionTimestamp, so it drops to 0 the instant a
  // delete is issued — long before pods are actually gone. For convergence
  // signals on scale-to-0 / delete, only this field is honest.
  // null when the backend doesn't expose `livePodCount` (older builds);
  // the convergence check falls back to ready-only in that case.
  livePods: number | null
  generation: number
  observedGeneration: number
}

function readWorkloadCounts(type: string, item: Record<string, unknown> | undefined): WorkloadCounts {
  const empty: WorkloadCounts = {
    desired: 0,
    ready: 0,
    updated: 0,
    livePods: null,
    generation: 0,
    observedGeneration: 0,
  }
  if (!item) return empty
  const num = (v: unknown) => Number(v ?? 0)
  // Distinguish "field absent" (older backend without livePodCount) from
  // "field present and = 0" (new backend, no pods exist). Reading via `in`
  // operator avoids the trap where num(undefined) silently becomes 0 and
  // makes scale-to-0 look instantly complete.
  const livePods =
    'livePodCount' in item ? Number(item.livePodCount ?? 0) : null
  const gens = {
    generation: num(item.generation),
    observedGeneration: num(item.observedGeneration),
  }
  switch (type) {
    case 'deployments':
      return {
        ...gens,
        desired: num(item.specReplicas ?? item.replicas),
        ready: num(item.readyReplicas),
        updated: num(item.updatedReplicas),
        livePods,
      }
    case 'statefulsets':
      return {
        ...gens,
        desired: num(item.specReplicas ?? item.replicas),
        ready: num(item.readyReplicas),
        updated: num(item.updatedReplicas ?? item.currentReplicas),
        livePods,
      }
    case 'daemonsets':
      return {
        ...gens,
        desired: num(item.desired),
        ready: num(item.ready),
        updated: num(item.updatedNumber ?? item.ready),
        livePods,
      }
    default:
      return empty
  }
}

function WorkloadProgress({
  proposal,
  toolCallId,
}: {
  proposal: ActionProposal
  toolCallId: string
}) {
  const { recordProposalProgressSettled } = useCopilot()
  const startedAt = useRef(Date.now())
  const [timedOut, setTimedOut] = useState(false)
  const [done, setDone] = useState(false)

  useEffect(() => {
    const id = window.setInterval(() => {
      if (Date.now() - startedAt.current > PROGRESS_TIMEOUT_MS) {
        setTimedOut(true)
      }
    }, 5_000)
    return () => window.clearInterval(id)
  }, [])

  const { data } = useQuery<Record<string, unknown>>({
    queryKey: [
      'proposal-progress',
      proposal.target.type,
      proposal.target.namespace,
      proposal.target.name,
    ],
    queryFn: () =>
      api.getResourceDetail(proposal.target.type, proposal.target.namespace, proposal.target.name),
    refetchInterval: done || timedOut ? false : PROGRESS_POLL_MS,
    enabled: !done && !timedOut,
    // Don't retry hard if the resource was deleted underneath us.
    retry: 1,
  })

  if (proposal.action !== 'restart_workload' && proposal.action !== 'scale_workload') {
    return null
  }

  const counts = readWorkloadCounts(proposal.target.type, data)

  // Generation convergence is the same signal `kubectl rollout status` uses:
  // the controller has observed and acted on the latest spec when
  // observedGeneration >= generation. This is robust to two edge cases:
  //   - a real rollout in progress (observedGen catches up at the end)
  //   - a no-op patch (no spec change → observedGen >= generation immediately)
  // We only trust it once we actually have data with a non-zero generation
  // (avoids a false "complete" before the first poll lands).
  const generationConverged =
    counts.generation > 0 && counts.observedGeneration >= counts.generation

  let line: string
  let isComplete: boolean

  if (proposal.action === 'restart_workload') {
    // During a real rollout, `updated` climbs from 0 to desired — show it.
    // For no-op restarts (or when updated lags), it stays at 0 even though
    // the workload is stable; in that case we suppress the misleading
    // "0/N updated" suffix and just show readiness.
    const showUpdated = counts.updated > 0 && counts.updated !== counts.ready
    line = showUpdated
      ? `Pods: ${counts.ready}/${counts.desired} ready · ${counts.updated}/${counts.desired} updated`
      : `Pods: ${counts.ready}/${counts.desired} ready`
    isComplete =
      generationConverged && counts.desired > 0 && counts.ready === counts.desired
  } else {
    // scale_workload — honest convergence is the conjunction of:
    //   livePods === target  → no extra pods still draining (the only
    //     field that doesn't drop to target the instant K8s issues the
    //     delete; counts pods by their actual presence in the API)
    //   ready    === target  → the pods that should be there serve traffic
    //
    // If `livePods` is null (older backend without the field), we cannot
    // detect drain completion — fall back to ready-only. The display
    // makes the degraded mode visible so we don't silently lie about a
    // "complete" state we can't verify.
    const target = Number(proposal.params.replicas)
    const haveLivePods = counts.livePods !== null
    const draining = haveLivePods ? Math.max(0, (counts.livePods as number) - target) : 0
    if (target === 0) {
      if (!haveLivePods) {
        line = `Scale to 0: waiting for pods to drain (refresh to see progress)`
      } else if (counts.livePods === 0) {
        line = `Scaled to 0`
      } else {
        line = `Scale to 0: ${counts.livePods} pod(s) draining`
      }
    } else {
      const base = `Scale to ${target}: ${counts.ready}/${target} ready`
      line = draining > 0 ? `${base} · ${draining} pod(s) still draining` : base
    }
    isComplete =
      generationConverged &&
      counts.ready === target &&
      (haveLivePods ? counts.livePods === target : true)
  }

  // Latch `done` once we reach the goal state so polling stops, AND
  // persist that into the proposal so a future re-mount of this card
  // (which would otherwise restart polling against a now-stale target)
  // skips the poller and shows only the resolved state.
  useEffect(() => {
    if (isComplete && !done) {
      setDone(true)
      recordProposalProgressSettled(toolCallId)
    }
  }, [isComplete, done, recordProposalProgressSettled, toolCallId])

  const Icon = isComplete ? CheckCircle2 : Loader2
  const colorCls = isComplete
    ? 'text-status-ok'
    : timedOut
      ? 'text-status-warn'
      : 'text-kb-text-secondary'
  const iconAnim = isComplete ? '' : 'animate-spin'

  return (
    <div className={`flex items-center gap-2 text-[11px] ${colorCls}`}>
      <Icon className={`w-3.5 h-3.5 ${iconAnim}`} />
      <span className="font-mono">{line}</span>
      {isComplete && <span className="text-[10px]">· complete</span>}
      {timedOut && !isComplete && (
        <span className="text-[10px]">· still in progress, check pods view</span>
      )}
    </div>
  )
}

// podsTabId maps a workload type to its tab id in ResourceDetailPage. Each
// workload kind has its own prefixed id (deploy-pods, sts-pods, ds-pods,
// job-pods) because the underlying tab components query different listers.
// Falls back to "overview" for non-workload targets so navigation still
// lands on a valid tab instead of "Coming Soon".
function podsTabId(type: string): string {
  switch (type) {
    case 'deployments':
      return 'deploy-pods'
    case 'statefulsets':
      return 'sts-pods'
    case 'daemonsets':
      return 'ds-pods'
    case 'jobs':
      return 'job-pods'
    default:
      return 'overview'
  }
}

// buildDetailPath maps a proposal target to the resource detail route.
// Returns null for resource types we don't have a detail page for (none
// of the PoC actions hit this case, but it keeps us robust as we add more).
function buildDetailPath(type: string, namespace: string, name: string): string | null {
  const ns = namespace || '_'
  const known = new Set([
    'deployments',
    'statefulsets',
    'daemonsets',
    'pods',
    'jobs',
    'cronjobs',
    'services',
    'ingresses',
    'configmaps',
    'secrets',
    'pvcs',
    'pvs',
    'hpas',
    'nodes',
    'namespaces',
  ])
  if (!known.has(type)) return null
  return `/${type}/${ns}/${name}`
}
