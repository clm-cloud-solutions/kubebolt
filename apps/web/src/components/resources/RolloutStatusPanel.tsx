import { useEffect, useMemo, useState } from 'react'
import { CheckCircle2, Loader2, AlertCircle, AlertTriangle } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/services/api'
import type { ResourceItem } from '@/types/kubernetes'

// RolloutStatusPanel — reusable post-mutation progress view. Used by:
//   1. RollbackModal after the rollback POST returns 200
//   2. (Cut 6) SetImageModal after the set-image POST returns "patched"
//
// What it does:
//   - Polls the workload's resource-detail every 1.5s (faster than
//     the global refresh interval — operators are watching live)
//     plus the pod list, and renders a progress view tailored to
//     the workload kind.
//   - Detects convergence using the same conditions kubectl rollout
//     status keys off, so "Rollback complete" matches what the CLI
//     would say.
//   - Three layout variants:
//     * Deployment — Ready X/Y bar + pod-status tile grid
//     * StatefulSet — reverse-ordinal list (pod-N → pod-0) since
//       STS rolls in that order
//     * DaemonSet — per-node row showing each pod's status
//
// The polling stops automatically when the rollout converges (or
// errors), so the panel doesn't keep beating on the API forever.
//
// Convergence rules — sourced from kubectl rollout status:
//   Deployment:    observedGeneration >= generation
//                  AND updatedReplicas == replicas
//                  AND availableReplicas == replicas
//   StatefulSet:   observedGeneration >= generation
//                  AND updatedReplicas == replicas
//                  AND readyReplicas == replicas
//   DaemonSet:     observedGeneration >= generation
//                  AND updatedNumber == desired
//                  AND ready == desired

interface PodSummary {
  name: string
  status: string
  ready: boolean
  node?: string
  ordinal?: number
  // Unix-ms parse of pod.creationTimestamp. Used to scope the panel
  // to pods that belong to THIS rollout (created at or after the
  // submitted-at marker). Without this, old pods from a previous
  // failed rollback bleed into the new rollback's view and trigger
  // a false "Rollout failed" banner before the new pods even start.
  createdAtMs?: number
}

interface Props {
  type: 'deployments' | 'statefulsets' | 'daemonsets'
  namespace: string
  name: string
  // Title shown above the status while the rollout is in progress.
  // Caller passes "Rolling back…" or "Applying new image…" so the
  // same component serves both flows.
  title: string
  // Generation the panel should wait to observe before declaring
  // convergence. Captured at submit time as `resource.generation + 1`
  // — without this gate, the panel can see a transient "everything
  // ready" state from the OLD revision (informer cache lag between
  // the apiserver Update and the cache catching up) and prematurely
  // declare success. kubectl rollout status uses the same trick.
  expectedGeneration?: number
  // Unix-ms timestamp of when the operator submitted this rollout.
  // Pods created BEFORE this point (with a small grace window for
  // clock skew) belong to a previous rollout and are excluded from
  // failure detection AND the pod table. Caller passes Date.now()
  // at submit time. Without this, a lingering ErrImagePull pod from
  // a prior bad rollback is detected as a failure of the new (good)
  // rollback that was just submitted.
  submittedAtMs?: number
}

// Convergence + failure detection happens here so the render path
// stays linear. Three states the UI cares about:
//
//   * progress   — patch not observed yet OR pods still rolling
//   * converged  — controller observed and all replicas are
//                  fresh, ready, and updated. Match `kubectl
//                  rollout status`'s success condition.
//   * failed     — at least one pod is stuck in a known terminal
//                  failure (ImagePullBackOff etc.) OR the Deployment
//                  itself reports Progressing=False with reason
//                  ProgressDeadlineExceeded. Once we hit this we
//                  stop pretending and show the operator the
//                  pod-level reason.
type RolloutPhase = 'progress' | 'converged' | 'failed'

// Failure reasons we recognize from container.state.waiting.reason
// (the backend's podToMap already promotes these into the pod's
// top-level `status` field). Anything else falls through to the
// generic "still rolling" view; we don't want to misclassify
// PodInitializing, ContainerCreating, or other transient states.
const TERMINAL_POD_FAILURE_REASONS = new Set([
  'ImagePullBackOff',
  'ErrImagePull',
  'InvalidImageName',
  'CrashLoopBackOff',
  'CreateContainerConfigError',
  'CreateContainerError',
  'OOMKilled',
])

const STUCK_AFTER_MS = 5 * 60_000 // 5 minutes

export function RolloutStatusPanel({
  type,
  namespace,
  name,
  title,
  expectedGeneration,
  submittedAtMs,
}: Props) {
  const queryClient = useQueryClient()

  // Fast-poll the resource detail so the convergence check refreshes
  // every 1.5s. We ignore the global refresh setting here — operators
  // are watching this view, not glancing at it.
  const { data: detail, error } = useQuery({
    queryKey: ['rollout-detail', type, namespace, name],
    queryFn: () => api.getResourceDetail(type, namespace, name),
    refetchInterval: 1500,
    enabled: !!type && !!namespace && !!name,
  })

  // Pods drive the per-kind visualization. Same fast cadence.
  const { data: podList } = useQuery({
    queryKey: ['rollout-pods', type, namespace, name],
    queryFn: () => fetchPods(type, namespace, name),
    refetchInterval: 1500,
    enabled: !!type && !!namespace && !!name,
  })

  const allPods = useMemo(() => normalizePods(podList?.items ?? []), [podList])
  // Filter pods to ones created at-or-after the submit timestamp,
  // minus 3s for clock skew between the browser and the apiserver.
  // Pods created before that window are leftovers from a previous
  // rollout and shouldn't count toward THIS rollout's success or
  // failure assessment. If the caller didn't pass submittedAtMs
  // (panel reused outside a rollback flow), no filtering applies.
  const pods = useMemo(() => {
    if (submittedAtMs === undefined) return allPods
    const cutoff = submittedAtMs - 3000
    return allPods.filter((p) => (p.createdAtMs ?? 0) >= cutoff)
  }, [allPods, submittedAtMs])
  const failingPods = useMemo(() => pods.filter((p) => TERMINAL_POD_FAILURE_REASONS.has(p.status)), [pods])
  const deploymentProgressFailed = useMemo(() => detectProgressDeadlineExceeded(detail), [detail])
  const status = useMemo(
    () => detectStatus(type, detail, expectedGeneration),
    [type, detail, expectedGeneration],
  )

  // Track elapsed seconds since mount. Stop the timer on convergence
  // OR failure — both are terminal states; we don't want a "5m
  // elapsed" header sitting under a red error message.
  const [elapsedMs, setElapsedMs] = useState(0)

  const phase: RolloutPhase = useMemo(() => {
    if (failingPods.length > 0 || deploymentProgressFailed) return 'failed'
    if (status.converged) return 'converged'
    return 'progress'
  }, [failingPods.length, deploymentProgressFailed, status.converged])

  const stuck = phase === 'progress' && elapsedMs > STUCK_AFTER_MS

  useEffect(() => {
    if (phase !== 'progress') {
      // Invalidate the workload's main query so when the operator
      // closes the modal the detail page reflects the new state.
      queryClient.invalidateQueries({ queryKey: ['resource-detail', type, namespace, name] })
      queryClient.invalidateQueries({ queryKey: ['resources'] })
      return
    }
    const start = Date.now()
    const id = setInterval(() => setElapsedMs(Date.now() - start), 250)
    return () => clearInterval(id)
  }, [phase, queryClient, type, namespace, name])

  if (error) {
    return (
      <div className="flex items-start gap-2 text-xs text-status-error border border-status-error/30 bg-status-error-dim rounded p-3">
        <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
        <div>
          <div className="font-semibold">Couldn't poll workload status</div>
          <div className="text-kb-text-secondary">{(error as Error).message}</div>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          {phase === 'converged' && <CheckCircle2 className="w-4 h-4 text-status-ok" />}
          {phase === 'failed' && <AlertCircle className="w-4 h-4 text-status-error" />}
          {phase === 'progress' && (
            <Loader2 className={`w-4 h-4 ${stuck ? 'text-status-warn' : 'text-status-info'} animate-spin`} />
          )}
          <span className="text-sm text-kb-text-primary">
            {phase === 'converged'
              ? 'Rollout complete'
              : phase === 'failed'
              ? 'Rollout failed'
              : stuck
              ? 'Taking longer than expected'
              : title}
          </span>
        </div>
        <div className="text-[11px] font-mono text-kb-text-tertiary">
          {phase === 'progress' ? `${formatElapsed(elapsedMs)} elapsed` : formatElapsed(elapsedMs)}
        </div>
      </div>

      {phase === 'failed' && (
        <FailureBanner failingPods={failingPods} deploymentProgressFailed={deploymentProgressFailed} />
      )}

      {stuck && phase === 'progress' && (
        <div className="flex items-start gap-2 text-[11px] text-status-warn border border-status-warn/30 bg-status-warn-dim rounded p-2.5">
          <AlertTriangle className="w-3.5 h-3.5 mt-0.5 shrink-0" />
          <div>
            The rollout has been in progress for more than 5 minutes. The controller is still working — you can leave this open or close and check back later. Common causes: slow image pulls, readiness probes ramping up, large replica counts.
          </div>
        </div>
      )}

      {/* Top-line progress: ready X/Y + updated X/Y */}
      <div className="grid grid-cols-2 gap-2">
        <ProgressTile
          label={status.readyLabel}
          current={status.readyCount}
          target={status.targetCount}
          accent={phase === 'converged' ? 'ok' : phase === 'failed' ? 'error' : 'info'}
        />
        <ProgressTile
          label={status.updatedLabel}
          current={status.updatedCount}
          target={status.targetCount}
          accent={phase === 'converged' ? 'ok' : phase === 'failed' ? 'error' : 'info'}
        />
      </div>

      {/* Per-kind body */}
      {type === 'deployments' && <DeploymentPods pods={pods} />}
      {type === 'statefulsets' && <StatefulSetPods pods={pods} />}
      {type === 'daemonsets' && <DaemonSetPods pods={pods} />}
    </div>
  )
}

function FailureBanner({
  failingPods,
  deploymentProgressFailed,
}: {
  failingPods: PodSummary[]
  deploymentProgressFailed: string | null
}) {
  // Aggregate the unique reasons (ImagePullBackOff / CrashLoopBackOff
  // / etc.) so the operator sees the cause without scrolling through
  // every pod row. Pod names are still in the table below.
  const reasons = new Set<string>()
  failingPods.forEach((p) => reasons.add(p.status))
  const summary = deploymentProgressFailed
    ? deploymentProgressFailed
    : reasons.size > 0
    ? `${failingPods.length} of N pod${failingPods.length === 1 ? '' : 's'} failing: ${[...reasons].join(', ')}`
    : 'The rollout did not converge.'
  return (
    <div className="flex items-start gap-2 text-xs text-status-error border border-status-error/40 bg-status-error-dim rounded p-3">
      <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
      <div className="space-y-1">
        <div className="font-semibold">Rollout failed</div>
        <div className="text-kb-text-secondary">
          {summary}
        </div>
        <div className="text-[11px] text-kb-text-tertiary">
          Pods will keep retrying. Close this modal and inspect pod logs / events, or trigger another rollback to a known-healthy revision.
        </div>
      </div>
    </div>
  )
}

function ProgressTile({
  label,
  current,
  target,
  accent,
}: {
  label: string
  current: number
  target: number
  accent: 'info' | 'ok' | 'error'
}) {
  const pct = target > 0 ? Math.min(100, Math.round((current / target) * 100)) : 0
  const barColor =
    accent === 'ok' ? 'bg-status-ok' : accent === 'error' ? 'bg-status-error' : 'bg-status-info'
  return (
    <div className="border border-kb-border rounded-lg p-3 bg-kb-card">
      <div className="text-[10px] uppercase tracking-wider text-kb-text-tertiary mb-1">{label}</div>
      <div className="flex items-baseline gap-2">
        <span className="text-base font-mono text-kb-text-primary">
          {current}
          <span className="text-kb-text-tertiary">/{target}</span>
        </span>
      </div>
      <div className="mt-2 h-1.5 bg-kb-elevated rounded overflow-hidden">
        <div className={`h-full ${barColor} transition-all duration-500`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

function DeploymentPods({ pods }: { pods: PodSummary[] }) {
  if (pods.length === 0) {
    return (
      <div className="border border-kb-border rounded-lg p-4 text-[11px] text-kb-text-tertiary text-center">
        No pods running yet…
      </div>
    )
  }
  // Two-mode layout. Up to 8 pods → readable list with names + status
  // chips so the operator sees exactly which pod is which (the most
  // common case: small deployments where you want individual context).
  // More than 8 → flex grid of compact tiles, one square per pod with
  // hover-tooltip showing the name and status — visual density wins
  // when you can't read 50 names anyway.
  if (pods.length <= 8) {
    return (
      <div className="border border-kb-border rounded-lg overflow-hidden">
        <div className="px-3 py-2 bg-kb-elevated/50 text-[10px] uppercase tracking-wider text-kb-text-tertiary">
          Pods ({pods.length})
        </div>
        <table className="w-full text-[11px]">
          <tbody>
            {pods.map((p) => (
              <tr key={p.name} className="border-t border-kb-border">
                <td className="px-3 py-2 font-mono text-kb-text-secondary break-all">{p.name}</td>
                <td className="px-3 py-2 w-32">
                  <PodStatusChip status={p.status} ready={p.ready} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    )
  }
  return (
    <div className="border border-kb-border rounded-lg overflow-hidden">
      <div className="px-3 py-2 bg-kb-elevated/50 text-[10px] uppercase tracking-wider text-kb-text-tertiary">
        Pods ({pods.length})
      </div>
      <div className="p-3 flex flex-wrap gap-1.5">
        {pods.map((p) => (
          <PodTile key={p.name} pod={p} />
        ))}
      </div>
    </div>
  )
}

function StatefulSetPods({ pods }: { pods: PodSummary[] }) {
  // STS rolls in REVERSE ordinal order (highest ordinal first), so
  // sort that way so the top of the list is the one currently
  // updating. Pods without an ordinal sort to the bottom.
  const sorted = [...pods].sort((a, b) => {
    if (a.ordinal === undefined && b.ordinal === undefined) return 0
    if (a.ordinal === undefined) return 1
    if (b.ordinal === undefined) return -1
    return b.ordinal - a.ordinal
  })
  if (sorted.length === 0) {
    return (
      <div className="border border-kb-border rounded-lg p-4 text-[11px] text-kb-text-tertiary text-center">
        No pods running yet…
      </div>
    )
  }
  return (
    <div className="border border-kb-border rounded-lg overflow-hidden">
      <div className="px-3 py-2 bg-kb-elevated/50 text-[10px] uppercase tracking-wider text-kb-text-tertiary">
        Pods ({sorted.length}) · rolling in reverse-ordinal order
      </div>
      <table className="w-full text-[11px]">
        <thead className="bg-kb-elevated/30">
          <tr className="text-left text-kb-text-tertiary uppercase tracking-wider text-[10px]">
            <th className="px-3 py-2 font-medium w-16">Ord</th>
            <th className="px-3 py-2 font-medium">Pod</th>
            <th className="px-3 py-2 font-medium w-32">Status</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((p) => (
            <tr key={p.name} className="border-t border-kb-border">
              <td className="px-3 py-2 font-mono text-kb-text-primary">
                {p.ordinal ?? '—'}
              </td>
              <td className="px-3 py-2 font-mono text-kb-text-secondary break-all">{p.name}</td>
              <td className="px-3 py-2">
                <PodStatusChip status={p.status} ready={p.ready} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function DaemonSetPods({ pods }: { pods: PodSummary[] }) {
  if (pods.length === 0) {
    return (
      <div className="border border-kb-border rounded-lg p-4 text-[11px] text-kb-text-tertiary text-center">
        No pods running yet…
      </div>
    )
  }
  return (
    <div className="border border-kb-border rounded-lg overflow-hidden">
      <div className="px-3 py-2 bg-kb-elevated/50 text-[10px] uppercase tracking-wider text-kb-text-tertiary">
        Pods ({pods.length}) · one per node
      </div>
      <table className="w-full text-[11px]">
        <thead className="bg-kb-elevated/30">
          <tr className="text-left text-kb-text-tertiary uppercase tracking-wider text-[10px]">
            <th className="px-3 py-2 font-medium">Node</th>
            <th className="px-3 py-2 font-medium">Pod</th>
            <th className="px-3 py-2 font-medium w-32">Status</th>
          </tr>
        </thead>
        <tbody>
          {pods.map((p) => (
            <tr key={p.name} className="border-t border-kb-border">
              <td className="px-3 py-2 font-mono text-kb-text-secondary">{p.node ?? '—'}</td>
              <td className="px-3 py-2 font-mono text-kb-text-secondary break-all">{p.name}</td>
              <td className="px-3 py-2">
                <PodStatusChip status={p.status} ready={p.ready} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function PodTile({ pod }: { pod: PodSummary }) {
  const tone = podTone(pod)
  return (
    <div
      className={`w-5 h-5 rounded-sm ${tone.bg} ${tone.border} border`}
      title={`${pod.name}\n${pod.status}${pod.ready ? ' (ready)' : ''}`}
    />
  )
}

function PodStatusChip({ status, ready }: { status: string; ready: boolean }) {
  const tone = podTone({ status, ready })
  return (
    <span className={`px-1.5 py-0.5 rounded text-[10px] font-mono ${tone.text} ${tone.bg}`}>
      {status}
    </span>
  )
}

function podTone(p: { status: string; ready: boolean }): { bg: string; border: string; text: string } {
  if (p.ready) {
    return { bg: 'bg-status-ok-dim', border: 'border-status-ok/40', text: 'text-status-ok' }
  }
  const s = p.status.toLowerCase()
  if (s.includes('pending') || s.includes('containercreating') || s.includes('podinitializing')) {
    return { bg: 'bg-kb-elevated', border: 'border-kb-border', text: 'text-kb-text-secondary' }
  }
  if (s.includes('crashloopbackoff') || s.includes('error') || s.includes('failed') || s.includes('imagepull')) {
    return { bg: 'bg-status-error-dim', border: 'border-status-error/40', text: 'text-status-error' }
  }
  if (s.includes('terminating')) {
    return { bg: 'bg-status-warn-dim', border: 'border-status-warn/40', text: 'text-status-warn' }
  }
  return { bg: 'bg-status-warn-dim', border: 'border-status-warn/40', text: 'text-status-warn' }
}

// detectStatus computes the convergence flag + progress numbers
// from the resource-detail payload. Each kind has its own progress
// fields; we surface them under a unified shape so the panel doesn't
// branch on every render.
//
// `expectedGeneration` is the parent-supplied gate: convergence is
// only allowed once we've seen the generation field reach this
// value, which proves the apiserver has accepted the patch and the
// informer cache has refreshed. Without this gate, the panel can see
// a fully-converged OLD revision in the brief window between the
// rollback's Update() returning and the cache catching up — and
// fire a false-positive "Done".
function detectStatus(
  type: 'deployments' | 'statefulsets' | 'daemonsets',
  detail: ResourceItem | undefined,
  expectedGeneration: number | undefined,
): {
  converged: boolean
  readyLabel: string
  updatedLabel: string
  readyCount: number
  updatedCount: number
  targetCount: number
} {
  const empty = {
    converged: false,
    readyLabel: 'Ready',
    updatedLabel: 'Updated',
    readyCount: 0,
    updatedCount: 0,
    targetCount: 0,
  }
  if (!detail) return empty
  const d = detail as unknown as {
    generation?: number
    observedGeneration?: number
    replicas?: number
    readyReplicas?: number
    availableReplicas?: number
    updatedReplicas?: number
    desired?: number
    ready?: number
    numberAvailable?: number
    updatedNumber?: number
  }
  const genObserved = (d.observedGeneration ?? 0) >= (d.generation ?? 0)
  // expectedGeneration gate: the patch must have been observed at
  // least once. If the parent didn't supply expectedGeneration (e.g.
  // the panel is reused outside a rollback flow), fall back to the
  // generation==observedGeneration check alone.
  const patchAccepted =
    expectedGeneration === undefined || (d.generation ?? 0) >= expectedGeneration
  const genConverged = genObserved && patchAccepted
  switch (type) {
    case 'deployments': {
      const target = d.replicas ?? 0
      const ready = d.availableReplicas ?? 0
      const updated = d.updatedReplicas ?? 0
      return {
        converged: genConverged && target > 0 && ready === target && updated === target,
        readyLabel: 'Ready',
        updatedLabel: 'Updated',
        readyCount: ready,
        updatedCount: updated,
        targetCount: target,
      }
    }
    case 'statefulsets': {
      const target = d.replicas ?? 0
      const ready = d.readyReplicas ?? 0
      const updated = d.updatedReplicas ?? 0
      return {
        converged: genConverged && target > 0 && ready === target && updated === target,
        readyLabel: 'Ready',
        updatedLabel: 'Updated',
        readyCount: ready,
        updatedCount: updated,
        targetCount: target,
      }
    }
    case 'daemonsets': {
      const target = d.desired ?? 0
      const ready = d.ready ?? 0
      const updated = d.updatedNumber ?? 0
      return {
        converged: genConverged && target > 0 && ready === target && updated === target,
        readyLabel: 'Ready',
        updatedLabel: 'Updated',
        readyCount: ready,
        updatedCount: updated,
        targetCount: target,
      }
    }
  }
}

// detectProgressDeadlineExceeded — the Deployment controller flips
// the Progressing condition to status=False with reason
// =ProgressDeadlineExceeded when a rollout has been stuck longer
// than `spec.progressDeadlineSeconds` (default 600s). When that
// happens, no further automatic progress is going to be made and
// the operator must intervene. STS/DS don't expose this signal —
// for those kinds we lean on per-pod failure detection.
function detectProgressDeadlineExceeded(detail: ResourceItem | undefined): string | null {
  if (!detail) return null
  const conds = (detail as unknown as { conditions?: Array<{ type?: string; status?: string; reason?: string; message?: string }> }).conditions
  if (!Array.isArray(conds)) return null
  for (const c of conds) {
    if (c.type === 'Progressing' && c.status === 'False' && c.reason === 'ProgressDeadlineExceeded') {
      return c.message || 'Deployment rollout exceeded its progress deadline'
    }
  }
  return null
}

async function fetchPods(type: string, namespace: string, name: string) {
  switch (type) {
    case 'deployments':
      return api.getDeploymentPods(namespace, name)
    case 'statefulsets':
      return api.getStatefulSetPods(namespace, name)
    case 'daemonsets':
      return api.getDaemonSetPods(namespace, name)
    default:
      return { items: [], kind: 'pods', total: 0 }
  }
}

function normalizePods(items: ResourceItem[]): PodSummary[] {
  return items.map((p) => {
    const r = p as unknown as {
      name: string
      status?: string
      readyReplicas?: number
      ready?: number | string
      nodeName?: string
      podIP?: string
      createdAt?: string
    }
    // The pod listing payload exposes "ready" as a "X/Y" string for
    // multi-container pods; treat any X==Y as ready. Single-string
    // statuses like "Running" alone don't always imply readiness, so
    // we lean on the ready field.
    const readyStr = typeof r.ready === 'string' ? r.ready : ''
    const isReady = (() => {
      if (readyStr) {
        const [a, b] = readyStr.split('/').map((n) => Number(n.trim()))
        if (Number.isFinite(a) && Number.isFinite(b) && b > 0) return a === b
      }
      return r.status === 'Running'
    })()
    const ordinal = parseOrdinal(r.name)
    const createdAtMs = r.createdAt ? Date.parse(r.createdAt) : undefined
    return {
      name: r.name,
      status: r.status ?? 'Unknown',
      ready: isReady,
      node: r.nodeName,
      ordinal,
      createdAtMs: Number.isFinite(createdAtMs) ? (createdAtMs as number) : undefined,
    }
  })
}

// StatefulSet pods are named `<sts>-<ordinal>`. Extract the trailing
// integer; return undefined for everything else (Deployment/DaemonSet
// pods don't have meaningful ordinals).
function parseOrdinal(name: string): number | undefined {
  const m = name.match(/-(\d+)$/)
  if (!m) return undefined
  const n = Number(m[1])
  return Number.isFinite(n) ? n : undefined
}

function formatElapsed(ms: number): string {
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m ${s % 60}s`
}
