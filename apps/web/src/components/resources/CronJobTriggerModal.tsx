import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Play, AlertTriangle, AlertCircle, CheckCircle2, ExternalLink } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { Modal } from '@/components/shared/Modal'
import { api, ApiError } from '@/services/api'
import type { ResourceItem } from '@/types/kubernetes'

// CronJobTriggerModal — small confirmation step for the manual
// `kubectl create job --from=cronjob/X` action. Lets the operator
// override the default auto-generated Job name and optionally
// suspend the schedule after the manual run starts (useful when
// debugging — "fire one now, then make sure 3 a.m. doesn't fire
// while I'm investigating").
//
// On success we navigate to the new Job's detail page so the
// operator can watch its pods come up. The CronJob's child-jobs
// view picks up the manual run automatically because we set the
// OwnerReference on the backend (see actions_cronjob.go).

interface Props {
  cronJob: ResourceItem
  onClose: () => void
}

export function CronJobTriggerModal({ cronJob, onClose }: Props) {
  const queryClient = useQueryClient()
  const namespace = cronJob.namespace
  const name = cronJob.name

  // Pre-fill with the same auto-name format the backend would
  // generate, so the operator sees the actual final name and can
  // edit it if they want a more descriptive label.
  const defaultJobName = useMemo(() => `${name}-manual-${Math.floor(Date.now() / 1000)}`, [name])
  const [jobName, setJobName] = useState(defaultJobName)
  const [suspendAfter, setSuspendAfter] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // After a successful trigger we hold the job summary so the modal
  // can render a "Job created — view it →" success state. Auto-
  // navigating immediately would race the apiserver / informer
  // propagation and the destination page would briefly 404 before
  // refetching. Letting the operator click the link guarantees the
  // resource is visible by the time they hit it.
  const [createdJob, setCreatedJob] = useState<{ name: string; namespace: string } | null>(null)

  // concurrencyPolicy=Forbid means the cron controller refuses to
  // start a scheduled run while a previous one is in flight. Manual
  // triggers via Job creation BYPASS that policy — they're separate
  // Jobs, not "scheduled runs". Surface the policy so the operator
  // knows they're sidestepping the constraint they wrote.
  const concurrencyPolicy = String(
    (cronJob as unknown as { concurrencyPolicy?: string }).concurrencyPolicy ?? '',
  )
  const concurrencyForbid = concurrencyPolicy === 'Forbid'

  const isAlreadySuspended =
    (cronJob as unknown as { suspend?: boolean }).suspend === true

  async function submit() {
    setBusy(true)
    setError(null)
    try {
      const res = await api.triggerCronJob(
        namespace,
        name,
        {
          jobName: jobName.trim() || undefined,
          suspendAfterTrigger: suspendAfter,
        },
        'ui',
      )
      // The new Job appears as a child of this CronJob — invalidate
      // the children query so the CronJob detail page's Jobs tab
      // refreshes when the operator returns. Also bump the cronjob
      // detail (suspend may have flipped if suspendAfter=true).
      queryClient.invalidateQueries({ queryKey: ['cronjob-jobs', namespace, name] })
      queryClient.invalidateQueries({ queryKey: ['resource-detail', 'cronjobs', namespace, name] })
      queryClient.invalidateQueries({ queryKey: ['resources', 'jobs'] })
      // Pre-populate the destination page's detail cache. The
      // backend gives us the canonical Job map (built from the
      // freshly-created object, not the informer cache). With this
      // cached entry + the global staleTime: 10_000, navigating to
      // /jobs/<ns>/<name> renders from cache for ~10s without a
      // refetch — long enough for the informer to catch up before
      // the next refetch interval fires. Without this, "Open job"
      // hits a 404 because the lister hasn't seen the watch event
      // yet.
      //
      // Defensive shape check: only cache if the response carries
      // the full Job map (status field is required by the detail
      // page's StatusBadge). An older backend version returned a
      // minimal {name, namespace, createdAt} which would crash
      // the destination page with "Cannot read properties of
      // undefined (reading 'toLowerCase')". When the response is
      // partial we skip the cache and let useResourceDetail fetch
      // normally — TanStack's default retry handles the brief
      // informer lag.
      const hasFullShape = res.job && typeof (res.job as { status?: unknown }).status === 'string'
      if (hasFullShape) {
        queryClient.setQueryData(
          ['resource-detail', 'jobs', res.job.namespace, res.job.name],
          res.job,
        )
      }
      setCreatedJob({ name: res.job.name, namespace: res.job.namespace })
      setBusy(false)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message)
      setBusy(false)
    }
  }

  return (
    <Modal
      badge={
        <span className="flex items-center gap-1 px-1 -mx-1 rounded bg-status-info text-kb-bg font-semibold">
          <Play className="w-3 h-3" /> trigger
        </span>
      }
      title={`Trigger CronJob · ${name}`}
      onClose={onClose}
      size="md"
    >
      <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
        {createdJob && (
          <div className="flex items-start gap-2 text-xs text-status-ok border border-status-ok/30 bg-status-ok-dim rounded p-3">
            <CheckCircle2 className="w-4 h-4 mt-0.5 shrink-0" />
            <div className="flex-1 min-w-0">
              <div className="font-semibold">Job created</div>
              <div className="text-kb-text-secondary leading-relaxed mt-0.5">
                <span className="font-mono">{createdJob.name}</span> is starting in namespace{' '}
                <span className="font-mono">{createdJob.namespace}</span>. Open it to watch pods come up.
              </div>
              <Link
                to={`/jobs/${createdJob.namespace}/${createdJob.name}`}
                onClick={onClose}
                className="inline-flex items-center gap-1.5 mt-2 px-2.5 py-1 rounded bg-status-ok text-kb-bg text-[11px] font-medium hover:opacity-90"
              >
                <ExternalLink className="w-3 h-3" />
                Open job
              </Link>
            </div>
          </div>
        )}

        <p className="text-xs text-kb-text-tertiary">
          Equivalent to{' '}
          <code className="font-mono px-1 py-px rounded bg-kb-elevated text-kb-text-primary text-[11px]">
            kubectl create job {jobName || `<name>`} --from=cronjob/{name}
          </code>
          . Creates a one-off Job from this CronJob's template; the schedule itself isn't affected.
        </p>

        {concurrencyForbid && (
          <div className="flex items-start gap-2 text-[11px] text-status-warn border border-status-warn/30 bg-status-warn-dim rounded p-3">
            <AlertTriangle className="w-3.5 h-3.5 mt-0.5 shrink-0" />
            <div className="text-kb-text-secondary leading-relaxed">
              <span className="font-semibold text-status-warn">concurrencyPolicy: Forbid is set.</span>{' '}
              The CronJob controller would refuse to start a SCHEDULED run while another is in flight, but manual triggers via Job creation aren't subject to that policy — your run will start regardless of any in-progress execution.
            </div>
          </div>
        )}

        {isAlreadySuspended && (
          <div className="flex items-start gap-2 text-[11px] text-status-info border border-status-info/30 bg-status-info-dim rounded p-3">
            <AlertCircle className="w-3.5 h-3.5 mt-0.5 shrink-0" />
            <div className="text-kb-text-secondary leading-relaxed">
              The CronJob is currently <span className="font-semibold">suspended</span> — the schedule won't fire on its own. Manual triggers still work; this is the right way to run a one-off while keeping the schedule paused.
            </div>
          </div>
        )}

        <div>
          <label className="text-[11px] text-kb-text-secondary block mb-1.5">
            Job name
          </label>
          <input
            type="text"
            value={jobName}
            onChange={(e) => setJobName(e.target.value)}
            placeholder={defaultJobName}
            className="w-full px-2 py-1.5 text-[11px] font-mono bg-kb-bg border border-kb-border rounded text-kb-text-primary focus:outline-none focus:border-kb-border-active"
          />
          <p className="text-[10px] text-kb-text-secondary mt-1">
            Auto-generated <span className="font-mono">&lt;cronjob&gt;-manual-&lt;unix&gt;</span> by default. Edit if you want a more descriptive name.
          </p>
        </div>

        {!isAlreadySuspended && (
          <label className="flex items-center gap-2 cursor-pointer p-3 border border-kb-border rounded-lg hover:bg-kb-elevated transition-colors">
            <input
              type="checkbox"
              checked={suspendAfter}
              onChange={(e) => setSuspendAfter(e.target.checked)}
              className="accent-status-warn"
            />
            <div>
              <div className="text-xs text-kb-text-primary">Suspend the cron schedule after this run</div>
              <div className="text-[10px] text-kb-text-secondary mt-0.5">
                Useful when triggering ad-hoc to debug — keeps further scheduled runs from firing until you resume manually.
              </div>
            </div>
          </label>
        )}

        {error && (
          <div className="flex items-start gap-2 text-xs text-status-error border border-status-error/30 bg-status-error-dim rounded p-3">
            <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
            <span className="text-kb-text-secondary leading-relaxed">{error}</span>
          </div>
        )}
      </div>

      <div className="px-5 py-3 border-t border-kb-border flex justify-end gap-2 shrink-0">
        <button
          onClick={onClose}
          disabled={busy}
          className="px-3 py-1.5 text-xs rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated disabled:opacity-50"
        >
          {createdJob ? 'Close' : 'Cancel'}
        </button>
        {!createdJob && (
          <button
            onClick={submit}
            disabled={busy}
            className="px-3 py-1.5 text-xs rounded bg-status-info text-kb-bg border border-status-info font-medium hover:opacity-90 disabled:opacity-40 disabled:cursor-not-allowed inline-flex items-center gap-1.5"
          >
            <Play className="w-3 h-3" />
            {busy ? 'Triggering…' : 'Trigger now'}
          </button>
        )}
      </div>
    </Modal>
  )
}
