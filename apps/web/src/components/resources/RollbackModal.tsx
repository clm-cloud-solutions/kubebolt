import { useMemo, useState } from 'react'
import { RotateCcw, AlertTriangle, ArrowRight } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import { api, ApiError } from '@/services/api'
import { useRolloutHistory } from '@/hooks/useResources'
import { useQueryClient } from '@tanstack/react-query'
import type { ResourceItem } from '@/types/kubernetes'
import type { RevisionImage } from '@/services/api'
import { RolloutStatusPanel } from './RolloutStatusPanel'

// RollbackModal — confirmation surface for `kubectl rollout undo
// --to-revision=N`. Shipped scope:
//
//   - Before/after multi-container image diff. Pulled from the new
//     ?detailed=true history endpoint so the diff matches what the
//     pod template will actually become (vs the legacy first-image-
//     only rendering).
//   - In-progress rollout warning. Reads deployment.status.conditions
//     for type=Progressing with reason!=NewReplicaSetAvailable, which
//     is the same signal `kubectl rollout status` waits on. Stacking
//     rollbacks on top of an in-flight rollout works but the operator
//     should know they're doing it.
//   - Typed-confirmation gate when the current revision is healthy
//     (skip when it's failing — speed matters more than friction in
//     a 3 a.m. recovery scenario).
//   - HPA footnote on Deployments — the rollback only changes the
//     pod template; replica count stays under the HPA's control.
//
// Deferred to Cut 5:
//   - Live progress panel after submit (rolling-out spinner with
//     pod-Ready transitions). Until then, the modal closes on success
//     and the user falls back to the standard detail-page refresh.

interface Props {
  type: 'deployments' | 'statefulsets' | 'daemonsets'
  namespace: string
  name: string
  // The target revision the operator picked from the timeline.
  targetRevision: number
  // The current resource detail — used to detect in-progress
  // rollouts (Deployment.status.conditions[Progressing]) and to
  // show the workload's HPA footnote when applicable.
  resource: ResourceItem | undefined
  onClose: () => void
}

export function RollbackModal({
  type,
  namespace,
  name,
  targetRevision,
  resource,
  onClose,
}: Props) {
  const queryClient = useQueryClient()
  const { data: history, isLoading: loadingHistory } = useRolloutHistory(type, namespace, name)

  const currentRev = history?.currentRevision ?? 0
  const target = history?.revisions.find((r) => r.revision === targetRevision)
  const current = history?.revisions.find((r) => r.revision === currentRev)
  const targetImages = target?.images ?? []
  const currentImages = current?.images ?? []
  const diff = useMemo(() => buildDiff(currentImages, targetImages), [currentImages, targetImages])

  const inProgressWarning = useMemo(() => detectInProgressRollout(resource), [resource])
  const currentIsHealthy = useMemo(() => isCurrentHealthy(resource), [resource])
  const requireTyped = currentIsHealthy

  const [typedName, setTypedName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Once the rollback POST succeeds we switch the modal body to the
  // live RolloutStatusPanel — modal stays open while pods churn so
  // the operator can watch progress without flipping back to the
  // detail page. They can still close mid-rollout (pods keep
  // rolling) or wait for convergence / failure.
  // expectedGeneration is the workload's pre-rollback generation+1,
  // captured at submit time. The panel uses it to gate convergence
  // and avoid false "Done" flickers from the informer cache lag.
  // submittedAtMs scopes failure detection to pods of THIS rollout
  // — pods left over from a previous failed rollback would
  // otherwise trigger a misleading "Rollout failed" the moment the
  // new modal opens.
  const [rollingOut, setRollingOut] = useState<{
    expectedGeneration: number
    submittedAtMs: number
  } | null>(null)

  // Build a human-readable reason for why submit is disabled, so the
  // tooltip on the button tells the operator exactly what to do
  // instead of just showing a silent blocked cursor. Order matters:
  // we surface the most actionable blocker first.
  const typedNameMatches = typedName.trim() === name
  let disabledReason: string | null = null
  if (busy) disabledReason = 'Submitting…'
  else if (loadingHistory) disabledReason = 'Loading revision details…'
  else if (!target) disabledReason = `Target revision ${targetRevision} not found in history`
  else if (targetRevision === currentRev) disabledReason = 'Target revision is already the current one'
  else if (requireTyped && !typedNameMatches) disabledReason = `Type "${name}" in the input above to confirm`
  const canSubmit = disabledReason === null

  async function submit() {
    setBusy(true)
    setError(null)
    // Capture the pre-submit generation so the panel can wait for
    // (current+1) before declaring success. Reading `resource` is
    // safe here — at this point in the modal lifecycle, the parent
    // has already loaded the workload's detail.
    const preGen = ((resource as unknown as { generation?: number })?.generation ?? 0) + 1
    // Snapshot of the submit moment for the pod-creation cutoff.
    // Captured pre-await so even a slow API call doesn't shift the
    // window forward and accidentally exclude the first new pod.
    const submittedAtMs = Date.now()
    try {
      const res = await api.rollbackResource(type, namespace, name, targetRevision, 'ui')
      if (res.resource) {
        queryClient.setQueryData(['resource-detail', type, namespace, name], res.resource)
      }
      queryClient.invalidateQueries({ queryKey: ['resources'] })
      queryClient.invalidateQueries({ queryKey: ['rollout-history', type, namespace, name] })
      queryClient.invalidateQueries({ queryKey: ['deployment-history', namespace, name] })
      queryClient.invalidateQueries({ queryKey: ['workload-history', type, namespace, name] })
      // Don't close the modal — switch to the live progress panel so
      // the operator watches pod transitions without leaving the
      // dialog. Operator closes via the modal's Close button (works
      // for both progress, converged, and failed states).
      setRollingOut({ expectedGeneration: preGen, submittedAtMs })
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  if (rollingOut) {
    return (
      <Modal
        badge={
          <span className="flex items-center gap-1 px-1 -mx-1 rounded bg-status-info text-kb-bg font-semibold">
            <RotateCcw className="w-3 h-3" /> rolling back
          </span>
        }
        title={`${name} → revision ${targetRevision}`}
        onClose={onClose}
        size="lg"
      >
        <div className="flex-1 overflow-y-auto px-5 py-4">
          <RolloutStatusPanel
            type={type}
            namespace={namespace}
            name={name}
            title="Rolling back to previous pod template…"
            expectedGeneration={rollingOut.expectedGeneration}
            submittedAtMs={rollingOut.submittedAtMs}
          />
        </div>
        <div className="px-5 py-3 border-t border-kb-border flex justify-end shrink-0">
          <button
            onClick={onClose}
            className="px-3 py-1.5 text-xs rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated"
          >
            Close
          </button>
        </div>
      </Modal>
    )
  }

  return (
    <Modal
      badge={
        <span className="flex items-center gap-1 px-1 -mx-1 rounded bg-status-warn text-kb-bg font-semibold">
          <RotateCcw className="w-3 h-3" /> rollback
        </span>
      }
      title={`Rollback ${name} to revision ${targetRevision}`}
      onClose={onClose}
      size="lg"
    >
      <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
        <div className="text-xs text-kb-text-secondary">
          Equivalent to{' '}
          <code className="font-mono px-1 py-px rounded bg-kb-elevated text-kb-text-primary text-[11px]">
            kubectl rollout undo {kubectlAlias(type)}/{name} --to-revision={targetRevision}
          </code>
          . Replicas, strategy, and other top-level fields are preserved — only the pod template reverts.
        </div>

        {/* Current → target summary */}
        <div className="border border-kb-border rounded-lg overflow-hidden">
          <div className="grid grid-cols-[1fr,auto,1fr] gap-0">
            <div className="p-3 bg-kb-elevated">
              <div className="text-[10px] uppercase tracking-wider text-kb-text-tertiary mb-1">From (current)</div>
              <div className="text-sm font-mono text-kb-text-primary">revision {currentRev || '?'}</div>
            </div>
            <div className="flex items-center justify-center px-2 bg-kb-elevated text-kb-text-tertiary">
              <ArrowRight className="w-4 h-4" />
            </div>
            <div className="p-3 bg-status-info-dim/30">
              <div className="text-[10px] uppercase tracking-wider text-status-info mb-1">To (target)</div>
              <div className="text-sm font-mono text-kb-text-primary">revision {targetRevision}</div>
            </div>
          </div>

          {/* Per-container image diff */}
          <div className="border-t border-kb-border">
            {loadingHistory ? (
              <div className="p-4 text-xs text-kb-text-tertiary text-center">Loading revision details…</div>
            ) : diff.length === 0 ? (
              <div className="p-4 text-xs text-kb-text-tertiary text-center">
                No container image data for one or both revisions — the rollback will still apply, but the diff can't be shown.
              </div>
            ) : (
              <table className="w-full text-[11px]">
                <thead className="bg-kb-elevated/50">
                  <tr className="text-left text-kb-text-tertiary uppercase tracking-wider text-[10px]">
                    <th className="px-3 py-2 font-medium w-32">Container</th>
                    <th className="px-3 py-2 font-medium">Current</th>
                    <th className="px-3 py-2 font-medium">After rollback</th>
                  </tr>
                </thead>
                <tbody>
                  {diff.map((row) => (
                    <tr key={row.container} className={`border-t border-kb-border ${row.changed ? 'bg-status-info-dim/20' : ''}`}>
                      <td className="px-3 py-2 font-mono text-kb-text-primary">{row.container}</td>
                      <td className="px-3 py-2 font-mono text-kb-text-secondary break-all">{row.current || <em className="text-kb-text-tertiary">absent</em>}</td>
                      <td className="px-3 py-2 font-mono break-all">
                        <span className={row.changed ? 'text-status-info' : 'text-kb-text-secondary'}>
                          {row.target || <em className="text-kb-text-tertiary">absent</em>}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>

        {inProgressWarning && (
          <div className="flex items-start gap-2 text-[11px] text-status-warn border border-status-warn/30 bg-status-warn-dim rounded p-2.5">
            <AlertTriangle className="w-3.5 h-3.5 mt-0.5 shrink-0" />
            <div>
              <div className="font-semibold mb-1">A rollout is currently in progress</div>
              <div className="text-kb-text-secondary">
                {inProgressWarning}
                <br />
                Stacking a rollback on top of an in-flight rollout works, but pod transitions may interleave. Consider waiting unless you're rolling back BECAUSE the in-flight rollout is bad.
              </div>
            </div>
          </div>
        )}

        {type === 'deployments' && hasHPA(resource) && (
          <div className="text-[11px] text-kb-text-tertiary border-l-2 border-kb-border pl-3">
            This Deployment is managed by an HPA. Rollback only changes the pod template — replica count stays under the HPA's control.
          </div>
        )}

        {requireTyped && (
          <div className="space-y-1">
            <label className="text-[11px] text-kb-text-tertiary">
              The current revision is healthy. To proceed, type{' '}
              <code className="font-mono px-1 py-px rounded bg-kb-elevated text-kb-text-primary">{name}</code>{' '}
              to confirm:
            </label>
            <input
              type="text"
              value={typedName}
              onChange={(e) => setTypedName(e.target.value)}
              placeholder={name}
              autoComplete="off"
              autoFocus
              className={`w-full px-2 py-1.5 text-[11px] font-mono bg-kb-bg border rounded text-kb-text-primary focus:outline-none focus:border-kb-border-active ${
                typedName === '' || typedNameMatches
                  ? 'border-kb-border'
                  : 'border-status-error'
              }`}
            />
          </div>
        )}
        {!requireTyped && (
          <div className="text-[11px] text-kb-text-tertiary">
            The current revision is unhealthy — confirmation gate skipped to keep recovery fast.
          </div>
        )}

        {error && (
          <div className="flex items-center gap-2 text-xs text-status-error">
            <AlertTriangle className="w-3 h-3 shrink-0" />
            <span className="break-words">{error}</span>
          </div>
        )}
      </div>

      <div className="px-5 py-3 border-t border-kb-border flex justify-end gap-2 shrink-0">
        <button
          onClick={onClose}
          disabled={busy}
          className="px-3 py-1.5 text-xs rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated disabled:opacity-50"
        >
          Cancel
        </button>
        <button
          onClick={submit}
          disabled={!canSubmit}
          title={disabledReason ?? `Rollback to revision ${targetRevision}`}
          className="px-3 py-1.5 text-xs rounded bg-status-warn text-kb-bg border border-status-warn hover:opacity-90 disabled:opacity-40 disabled:cursor-not-allowed inline-flex items-center gap-1 font-medium"
        >
          <RotateCcw className={`w-3 h-3 ${busy ? 'animate-spin' : ''}`} />
          {busy ? 'Rolling back…' : `Rollback to revision ${targetRevision}`}
        </button>
      </div>
    </Modal>
  )
}

// buildDiff produces a unioned per-container view: every container
// that exists in either current or target is a row. Changed flag is
// true when both sides exist but the image string differs.
function buildDiff(
  currentImages: RevisionImage[],
  targetImages: RevisionImage[],
): { container: string; current: string; target: string; changed: boolean }[] {
  const byName = new Map<string, { current: string; target: string }>()
  for (const c of currentImages) {
    byName.set(c.container, { current: c.image, target: '' })
  }
  for (const t of targetImages) {
    const existing = byName.get(t.container)
    if (existing) existing.target = t.image
    else byName.set(t.container, { current: '', target: t.image })
  }
  const out: { container: string; current: string; target: string; changed: boolean }[] = []
  byName.forEach((v, k) => {
    out.push({ container: k, current: v.current, target: v.target, changed: v.current !== v.target })
  })
  // Stable sort by container name so re-renders don't reorder rows.
  out.sort((a, b) => a.container.localeCompare(b.container))
  return out
}

// detectInProgressRollout returns a one-line description when the
// Deployment has an active Progressing condition, or null when it
// doesn't. The signal we look at is the standard
// type=Progressing/status=True/reason=ReplicaSetUpdated chain that
// `kubectl rollout status` keys off; reason=NewReplicaSetAvailable
// means the rollout COMPLETED successfully and is no longer in
// progress, so we suppress the warning in that case.
function detectInProgressRollout(resource: ResourceItem | undefined): string | null {
  if (!resource) return null
  const conds = (resource as unknown as { conditions?: Array<{ type?: string; status?: string; reason?: string; message?: string }> }).conditions
  if (!Array.isArray(conds)) return null
  for (const c of conds) {
    if (c.type === 'Progressing' && c.status === 'True') {
      if (c.reason === 'NewReplicaSetAvailable') return null
      return c.message || c.reason || 'Rollout in progress'
    }
  }
  return null
}

// isCurrentHealthy tells us whether to require typed confirmation.
// "Healthy" = the deployment Available condition is True; STS/DS
// don't expose conditions in the same way, so we fall back to the
// readyReplicas == replicas heuristic.
function isCurrentHealthy(resource: ResourceItem | undefined): boolean {
  if (!resource) return false
  const r = resource as unknown as {
    conditions?: Array<{ type?: string; status?: string }>
    readyReplicas?: number
    replicas?: number
  }
  if (Array.isArray(r.conditions)) {
    const avail = r.conditions.find((c) => c.type === 'Available')
    if (avail) return avail.status === 'True'
  }
  if (typeof r.readyReplicas === 'number' && typeof r.replicas === 'number' && r.replicas > 0) {
    return r.readyReplicas === r.replicas
  }
  return false
}

// hasHPA detects whether a Deployment is currently autoscaled. The
// resource detail doesn't carry HPA refs directly — HPAs reference
// the deployment, not the other way around — so this is a best-
// effort check on the spec.replicas being ABSENT (HPA-managed
// deployments often omit replicas from spec). It's correct often
// enough to be useful; a false negative just hides the footnote.
function hasHPA(resource: ResourceItem | undefined): boolean {
  if (!resource) return false
  // Heuristic: K8s HPA controller typically clears spec.replicas on
  // managed deployments and writes the desired count to status. The
  // resource detail's `specReplicas` field captures spec value.
  const r = resource as unknown as { specReplicas?: number }
  return r.specReplicas === 0 || r.specReplicas === undefined
}

function kubectlAlias(type: 'deployments' | 'statefulsets' | 'daemonsets'): string {
  switch (type) {
    case 'deployments':
      return 'deploy'
    case 'statefulsets':
      return 'sts'
    case 'daemonsets':
      return 'ds'
  }
}
