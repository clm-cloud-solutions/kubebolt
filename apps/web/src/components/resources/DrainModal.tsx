import { useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  Loader2,
  X as XIcon,
  Info,
} from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { Modal } from '@/components/shared/Modal'
import { api, ApiError } from '@/services/api'
import { useResources } from '@/hooks/useResources'
import type { ResourceItem } from '@/types/kubernetes'

// DrainModal — `kubectl drain` made ergonomic and observable. Two
// phases:
//
//   1. configure — operator picks grace period, timeout, and the
//      four flags. Pre-flight summary + guardrails make sure they
//      know what they're committing to before the apiserver sees
//      any patches.
//   2. streaming — POST returns an SSE stream that emits one
//      `pod-evicted` event per pod terminated and a final
//      `drain-complete` carrying status + duration + error if any.
//      We render that stream live: progress bar, scrollable list
//      of evicted pods, terminal state at the bottom.
//
// Drain survives modal close: the goroutine on the backend keeps
// running on its own context (see actions_drain.go). On reopen we
// GET the same URL to re-attach to the in-flight session, replay
// the buffered events, and continue from where we left off.

interface Props {
  node: ResourceItem
  onClose: () => void
}

interface DrainConfig {
  gracePeriodSeconds: number
  timeoutSeconds: number
  deleteEmptyDirData: boolean
  ignoreDaemonsets: boolean
  force: boolean
  disableEviction: boolean
}

const DEFAULT_CONFIG: DrainConfig = {
  gracePeriodSeconds: 60,
  timeoutSeconds: 300,
  deleteEmptyDirData: true,
  ignoreDaemonsets: true,
  force: false,
  disableEviction: false,
}

interface DrainPodEvent {
  pod: string
  namespace: string
  status: 'evicted' | 'deleted' | 'error'
  error?: string
}

interface DrainCompletePayload {
  status: 'drained' | 'drain-failed' | 'drain-partial' | 'cancelled'
  evicted: number
  durationMs: number
  error?: string
}

type Phase = 'configure' | 'streaming' | 'completed'

export function DrainModal({ node, onClose }: Props) {
  const queryClient = useQueryClient()
  const [config, setConfig] = useState<DrainConfig>(DEFAULT_CONFIG)
  const [phase, setPhase] = useState<Phase>('configure')
  const [evicted, setEvicted] = useState<DrainPodEvent[]>([])
  const [completion, setCompletion] = useState<DrainCompletePayload | null>(null)
  const [streamError, setStreamError] = useState<string | null>(null)
  const [submitError, setSubmitError] = useState<string | null>(null)
  // Snapshot of the evictable count at submit time — used as the
  // denominator of the progress bar. Computing it from the live
  // pod list during streaming would be misleading because pods
  // start disappearing as they're evicted.
  const [expectedTotal, setExpectedTotal] = useState(0)

  // Pull the full pod list from cache (already live-refetching for
  // the Pods page) and the nodes list (for the 1-node guardrail).
  // This avoids a new endpoint just for the pre-flight; the data we
  // need is already loaded.
  const { data: podData } = useResources('pods')
  const { data: nodesData } = useResources('nodes')

  const podsOnNode = useMemo(() => {
    const items = podData?.items ?? []
    return items.filter((p) => (p as unknown as { nodeName?: string }).nodeName === node.name)
  }, [podData, node.name])

  const summary = useMemo(() => computeDrainSummary(podsOnNode, config), [podsOnNode, config])
  const guardrail = useMemo(() => computeGuardrails(node, nodesData?.items ?? []), [node, nodesData])

  // Surface the node's current cordoned state and the
  // "nothing-to-evict" condition. Both inform the operator before
  // they click — a click on a 0-evictable drain is a no-op
  // server-side (drain.Helper completes in ~100ms with 0 evicted)
  // but UX-wise reads as a wasted action. Disabling + tooltip
  // makes the state visible without removing the affordance
  // entirely (so the operator can still toggle Ignore DaemonSets
  // off if they really want to evict daemonset pods too).
  const isCordoned = (node as unknown as { unschedulable?: boolean }).unschedulable === true
  const nothingToEvict = summary.evictable === 0 && summary.total > 0
  const drainDisabledReason: string | null = guardrail.block
    ? guardrail.blockReason!
    : nothingToEvict
    ? isCordoned
      ? 'Node is already drained — no evictable pods left. Toggle "Ignore DaemonSets" off if you intend to evict daemonset pods too.'
      : 'No evictable pods on this node. Toggle "Ignore DaemonSets" off if you intend to evict daemonset pods too.'
    : null

  // AbortController is the leash on the in-flight POST/GET fetch.
  // We abort it when the modal closes so we don't keep an orphan
  // reader running. NOTE: aborting the fetch does NOT cancel the
  // server-side drain — that's what handleDrainCancel + the DELETE
  // endpoint are for. Closing the modal mid-drain just stops THIS
  // browser's stream consumption; the drain keeps going.
  const abortRef = useRef<AbortController | null>(null)
  useEffect(() => {
    return () => {
      abortRef.current?.abort()
    }
  }, [])

  // On open, attempt to re-attach to an in-flight session if one
  // exists for this node. 404 means none; we go straight to
  // configure mode. Any other status with a real stream means a
  // drain is already in progress — we hop directly to streaming
  // and replay the buffered events.
  useEffect(() => {
    let cancelled = false
    const ctrl = new AbortController()
    ;(async () => {
      try {
        const res = await api.attachDrainSession(node.name, ctrl.signal)
        if (cancelled) return
        if (res.status === 404) return
        if (!res.ok) return
        // Active session found — start consuming.
        abortRef.current = ctrl
        setPhase('streaming')
        await consumeDrainStream(res, {
          onPod: (p) => setEvicted((prev) => [...prev, p]),
          onComplete: (c) => {
            setCompletion(c)
            setPhase('completed')
            invalidateNode(queryClient, node.name)
          },
          onStreamError: (e) => setStreamError(e),
        })
      } catch {
        // ignore — initial probe; user will manually start a drain
      }
    })()
    return () => {
      cancelled = true
      ctrl.abort()
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [node.name])

  async function startDrain() {
    if (guardrail.block) return
    setSubmitError(null)
    setEvicted([])
    setCompletion(null)
    setStreamError(null)
    setExpectedTotal(summary.evictable)

    const ctrl = new AbortController()
    abortRef.current = ctrl

    try {
      const res = await api.drainNode(
        node.name,
        {
          gracePeriodSeconds: config.gracePeriodSeconds,
          timeoutSeconds: config.timeoutSeconds,
          deleteEmptyDirData: config.deleteEmptyDirData,
          ignoreDaemonsets: config.ignoreDaemonsets,
          force: config.force,
          disableEviction: config.disableEviction,
        },
        'ui',
        ctrl.signal,
      )
      if (!res.ok) {
        // The handler returns SSE on success and JSON on early
        // error. A non-OK status means the request never started
        // streaming — read the body as text and surface it.
        const msg = await res.text().catch(() => '')
        throw new ApiError(res.status, msg || `drain failed (HTTP ${res.status})`)
      }
      setPhase('streaming')
      await consumeDrainStream(res, {
        onPod: (p) => setEvicted((prev) => [...prev, p]),
        onComplete: (c) => {
          setCompletion(c)
          setPhase('completed')
          invalidateNode(queryClient, node.name)
        },
        onStreamError: (e) => setStreamError(e),
      })
    } catch (e) {
      if ((e as Error).name === 'AbortError') return
      setSubmitError(e instanceof ApiError ? e.message : (e as Error).message)
      setPhase('configure')
    }
  }

  async function cancelDrain() {
    try {
      await api.cancelDrain(node.name)
      // Server emits drain-complete with status=cancelled; our
      // stream consumer picks it up and transitions to 'completed'.
      // Nothing to do client-side beyond the request.
    } catch (e) {
      setStreamError(`Cancel failed: ${(e as Error).message}`)
    }
  }

  return (
    <Modal
      badge={
        <span className="flex items-center gap-1 px-1 -mx-1 rounded bg-status-warn text-kb-bg font-semibold">
          <AlertTriangle className="w-3 h-3" /> drain
        </span>
      }
      title={`Drain node · ${node.name}`}
      onClose={onClose}
      size="xl"
    >
      {/* Body shape switches by phase. Configure scrolls the whole
          form (long if the cluster has a lot of pods to summarize).
          Streaming/completed pin the banner + progress bar at the
          top and let only the pod-outcomes table scroll — operators
          watching a 100-pod drain shouldn't lose the progress
          indicator off the top of the modal. */}
      {phase === 'configure' ? (
        <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
            <p className="text-xs text-kb-text-tertiary">
              Equivalent to{' '}
              <code className="font-mono px-1 py-px rounded bg-kb-elevated text-kb-text-primary text-[11px]">
                kubectl drain {node.name}
              </code>
              . Pods are evicted respecting PodDisruptionBudgets; the node is
              automatically cordoned first.
            </p>

            {/* Cordoned-state banner — informational, comes first
                so the operator immediately sees the node is already
                in a drained-ish state. Suppressed when a hard
                guardrail block is present (block message takes
                precedence). */}
            {isCordoned && !guardrail.block && (
              <CordonedBanner alreadyDrained={nothingToEvict} />
            )}
            {guardrail.block && (
              <GuardrailBanner kind="block" message={guardrail.blockReason!} />
            )}
            {guardrail.warn && (
              <GuardrailBanner kind="warn" message={guardrail.warnReason!} />
            )}

            <PreflightTable summary={summary} />

            <div className="border border-kb-border rounded-lg p-4 space-y-4">
              <SliderRow
                label="Grace period"
                hint="Seconds the kubelet waits for pods to terminate before SIGKILL"
                value={config.gracePeriodSeconds}
                min={0}
                max={600}
                step={10}
                unit="s"
                onChange={(v) => setConfig({ ...config, gracePeriodSeconds: v })}
              />
              <SliderRow
                label="Timeout"
                hint="Maximum total drain time before aborting"
                value={config.timeoutSeconds}
                min={30}
                max={1800}
                step={30}
                unit="s"
                onChange={(v) => setConfig({ ...config, timeoutSeconds: v })}
              />
              <div className="grid grid-cols-2 gap-2">
                <Toggle
                  label="Delete emptyDir data"
                  hint="Required to evict pods using emptyDir volumes"
                  value={config.deleteEmptyDirData}
                  onChange={(v) => setConfig({ ...config, deleteEmptyDirData: v })}
                />
                <Toggle
                  label="Ignore DaemonSets"
                  hint="DaemonSet pods aren't evicted (kubelet replaces them)"
                  value={config.ignoreDaemonsets}
                  onChange={(v) => setConfig({ ...config, ignoreDaemonsets: v })}
                />
                <Toggle
                  label="Force"
                  hint="Evict pods that aren't managed by a controller (DANGEROUS)"
                  value={config.force}
                  onChange={(v) => setConfig({ ...config, force: v })}
                />
                <Toggle
                  label="Disable eviction"
                  hint="Use direct delete instead of eviction (skips PDBs)"
                  value={config.disableEviction}
                  onChange={(v) => setConfig({ ...config, disableEviction: v })}
                />
              </div>
            </div>

            {submitError && (
              <div className="flex items-start gap-2 text-xs text-status-error border border-status-error/30 bg-status-error-dim rounded p-3">
                <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                <span className="text-kb-text-secondary leading-relaxed">{submitError}</span>
              </div>
            )}
        </div>
      ) : (
        <div className="flex-1 flex flex-col overflow-hidden">
          <DrainProgressView
            evicted={evicted}
            expectedTotal={expectedTotal || evicted.length}
            completion={completion}
            streamError={streamError}
          />
        </div>
      )}

      <div className="px-5 py-3 border-t border-kb-border flex justify-end gap-2 shrink-0">
        {phase === 'configure' && (
          <>
            <button
              onClick={onClose}
              className="px-3 py-1.5 text-xs rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated"
            >
              Cancel
            </button>
            <button
              onClick={startDrain}
              disabled={drainDisabledReason !== null}
              title={drainDisabledReason ?? undefined}
              className="px-3 py-1.5 text-xs rounded bg-status-warn text-kb-bg border border-status-warn font-medium hover:opacity-90 disabled:opacity-40 disabled:cursor-not-allowed inline-flex items-center gap-1.5"
            >
              <AlertTriangle className="w-3 h-3" />
              Drain node
            </button>
          </>
        )}
        {phase === 'streaming' && (
          <>
            <button
              onClick={onClose}
              className="px-3 py-1.5 text-xs rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated"
              title="Close the modal — the drain continues on the server"
            >
              Close
            </button>
            <button
              onClick={cancelDrain}
              className="px-3 py-1.5 text-xs rounded bg-status-error-dim text-status-error border border-status-error font-medium hover:bg-status-error hover:text-kb-bg inline-flex items-center gap-1.5"
            >
              <XIcon className="w-3 h-3" />
              Cancel drain
            </button>
          </>
        )}
        {phase === 'completed' && (
          <button
            onClick={onClose}
            className="px-3 py-1.5 text-xs rounded bg-status-info-dim text-status-info border border-status-info font-medium hover:bg-status-info hover:text-kb-bg inline-flex items-center gap-1.5"
          >
            <CheckCircle2 className="w-3 h-3" />
            Done
          </button>
        )}
      </div>
    </Modal>
  )
}

// ───────────────────────── Live progress view ─────────────────────────

function DrainProgressView({
  evicted,
  expectedTotal,
  completion,
  streamError,
}: {
  evicted: DrainPodEvent[]
  expectedTotal: number
  completion: DrainCompletePayload | null
  streamError: string | null
}) {
  // Until the drain finishes, the denominator is `expectedTotal`
  // (the snapshot from submit time). After completion the
  // canonical count is in `completion.evicted` — drain.Helper
  // sometimes evicts a pod we didn't pre-count (a daemonset pod
  // when ignoreDaemonsets=false, for instance). Use whichever is
  // larger to keep the progress bar from going backwards.
  const denominator = Math.max(expectedTotal, evicted.length, completion?.evicted ?? 0)
  const percent = denominator > 0 ? Math.min(100, Math.round((evicted.length / denominator) * 100)) : 0

  const isFinished = completion !== null

  // Status banner: green on full success, amber on cancel /
  // partial, red on outright failure or stream error. The
  // matching colors come from the same status-* tokens used
  // throughout the app.
  const banner = (() => {
    if (streamError) {
      return {
        accent: 'error',
        title: 'Stream interrupted',
        message: streamError,
        icon: <AlertCircle className="w-4 h-4" />,
      }
    }
    if (!isFinished) {
      return {
        accent: 'info',
        title: 'Draining…',
        message: `${evicted.length} of ~${denominator} pods evicted`,
        icon: <Loader2 className="w-4 h-4 animate-spin" />,
      }
    }
    if (completion!.status === 'drained') {
      return {
        accent: 'ok',
        title: 'Drain complete',
        message: `${completion!.evicted} pods evicted in ${formatDuration(completion!.durationMs)}`,
        icon: <CheckCircle2 className="w-4 h-4" />,
      }
    }
    if (completion!.status === 'cancelled') {
      return {
        accent: 'warn',
        title: 'Drain cancelled',
        message: `${completion!.evicted} pods evicted before cancel`,
        icon: <XIcon className="w-4 h-4" />,
      }
    }
    if (completion!.status === 'drain-partial') {
      return {
        accent: 'warn',
        title: 'Drain partial',
        message: completion!.error ?? `${completion!.evicted} pods evicted; the rest failed`,
        icon: <AlertTriangle className="w-4 h-4" />,
      }
    }
    return {
      accent: 'error',
      title: 'Drain failed',
      message: completion!.error ?? 'unknown error',
      icon: <AlertCircle className="w-4 h-4" />,
    }
  })()

  // Layout: banner + progress = shrink-0 (always visible at the
  // top), table = flex-1 + min-h-0 + overflow-y-auto (scrolls
  // internally instead of pushing progress off the modal). The
  // thead is sticky so column labels stay visible while the
  // operator scrolls a 100-pod drain.
  return (
    <div className="flex-1 flex flex-col gap-3 px-5 py-4 min-h-0 overflow-hidden">
      {/* Header banner with current state */}
      <div
        className={`shrink-0 flex items-start gap-2.5 text-xs border rounded p-3 ${
          banner.accent === 'ok'
            ? 'text-status-ok border-status-ok/30 bg-status-ok-dim'
            : banner.accent === 'warn'
            ? 'text-status-warn border-status-warn/30 bg-status-warn-dim'
            : banner.accent === 'error'
            ? 'text-status-error border-status-error/30 bg-status-error-dim'
            : 'text-status-info border-status-info/30 bg-status-info-dim'
        }`}
      >
        <div className="mt-0.5 shrink-0">{banner.icon}</div>
        <div className="flex-1 min-w-0">
          <div className="font-semibold">{banner.title}</div>
          <div className="text-kb-text-secondary leading-relaxed mt-0.5">{banner.message}</div>
        </div>
      </div>

      {/* Progress bar */}
      <div className="shrink-0 border border-kb-border rounded-lg p-3 space-y-2">
        <div className="flex items-center justify-between">
          <span className="text-[10px] uppercase tracking-wider text-kb-text-tertiary">Progress</span>
          <span className="text-xs font-mono text-kb-text-primary">
            {evicted.length} / {denominator}
          </span>
        </div>
        <div className="h-1.5 bg-kb-elevated rounded-full overflow-hidden">
          <div
            className={`h-full transition-all duration-500 ${
              isFinished
                ? completion!.status === 'drained'
                  ? 'bg-status-ok'
                  : completion!.status === 'cancelled'
                  ? 'bg-status-warn'
                  : 'bg-status-error'
                : 'bg-status-info'
            }`}
            style={{ width: `${percent}%` }}
          />
        </div>
      </div>

      {/* Per-pod outcomes — flex-1 + min-h-0 forces this region to
          take the remaining space and scroll internally. Sticky
          thead keeps the column headers anchored as rows scroll.
          Same table chrome SetImageModal uses. */}
      {evicted.length > 0 && (
        <div className="flex-1 min-h-0 border border-kb-border rounded-lg overflow-y-auto">
          <table className="w-full text-xs">
            <thead className="sticky top-0 bg-kb-elevated border-b border-kb-border z-10">
              <tr className="text-left text-kb-text-tertiary uppercase tracking-wider text-[10px]">
                <th className="px-3 py-2 font-medium">Pod</th>
                <th className="px-3 py-2 font-medium w-32">Namespace</th>
                <th className="px-3 py-2 font-medium w-24">Status</th>
              </tr>
            </thead>
            <tbody>
              {evicted.map((p, i) => (
                <tr key={`${p.namespace}/${p.pod}/${i}`} className="border-b border-kb-border last:border-b-0">
                  <td className="px-3 py-2 font-mono text-kb-text-primary text-[11px] break-all">{p.pod}</td>
                  <td className="px-3 py-2 font-mono text-kb-text-secondary text-[11px]">{p.namespace}</td>
                  <td className="px-3 py-2">
                    <span
                      className={`text-[10px] uppercase tracking-wider font-medium ${
                        p.status === 'error' ? 'text-status-error' : 'text-status-ok'
                      }`}
                    >
                      {p.status}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {evicted.length === 0 && !isFinished && (
        <div className="shrink-0 text-xs text-kb-text-secondary border border-kb-border bg-kb-elevated rounded p-3 text-center">
          Waiting for first eviction…
        </div>
      )}
    </div>
  )
}

// ──────────────────────────── SSE consumer ────────────────────────────

interface DrainStreamCallbacks {
  onPod: (p: DrainPodEvent) => void
  onComplete: (c: DrainCompletePayload) => void
  onStreamError: (msg: string) => void
}

// consumeDrainStream parses the SSE response body line-by-line.
// We can't use EventSource because it only does GET; the POST
// route returns SSE so we hand-roll the parser. Heartbeats are
// SSE comments (`: heartbeat\n\n`) and get dropped silently.
async function consumeDrainStream(res: Response, cb: DrainStreamCallbacks): Promise<void> {
  if (!res.body) {
    cb.onStreamError('response has no body')
    return
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buf += decoder.decode(value, { stream: true })
      // SSE events are separated by a blank line (`\n\n`). Pull
      // every complete event out of the buffer; leave the trailing
      // partial event for the next iteration.
      let sep = buf.indexOf('\n\n')
      while (sep !== -1) {
        const block = buf.slice(0, sep)
        buf = buf.slice(sep + 2)
        sep = buf.indexOf('\n\n')
        const ev = parseSSEBlock(block)
        if (!ev) continue
        if (ev.name === 'pod-evicted') {
          cb.onPod(ev.data as DrainPodEvent)
        } else if (ev.name === 'drain-complete') {
          cb.onComplete(ev.data as DrainCompletePayload)
        } else if (ev.name === 'drain-error') {
          // Pre-stream errors that didn't make it to a regular
          // drain-complete. Surface as a streamError; the session
          // closes immediately after on the backend.
          const data = ev.data as { error?: string }
          cb.onStreamError(data?.error ?? 'unknown drain error')
        }
      }
    }
  } catch (e) {
    if ((e as Error).name === 'AbortError') return
    cb.onStreamError((e as Error).message)
  }
}

function parseSSEBlock(block: string): { name: string; data: unknown } | null {
  let name = ''
  let dataStr = ''
  for (const line of block.split('\n')) {
    if (line.startsWith(':')) continue // comment / heartbeat
    if (line.startsWith('event:')) name = line.slice(6).trim()
    else if (line.startsWith('data:')) dataStr += line.slice(5).trim()
  }
  if (!name || !dataStr) return null
  try {
    return { name, data: JSON.parse(dataStr) }
  } catch {
    return null
  }
}

// ────────────────────────────── helpers ──────────────────────────────

function formatDuration(ms: number): string {
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m ${s % 60}s`
}

function invalidateNode(queryClient: ReturnType<typeof useQueryClient>, name: string) {
  // After a drain finishes, the node's pod count + cordon flag have
  // changed. Refetch the resources lists + the node detail so the
  // UI reflects the new state when the user closes the modal.
  queryClient.invalidateQueries({ queryKey: ['resources', 'nodes'] })
  queryClient.invalidateQueries({ queryKey: ['resources', 'pods'] })
  queryClient.invalidateQueries({ queryKey: ['resource-detail', 'nodes', '_', name] })
}

// ───────────────────────────── Pre-flight ─────────────────────────────

interface DrainSummary {
  total: number
  daemonset: number
  mirror: number
  evictable: number
  withEmptyDir: number
}

function computeDrainSummary(pods: ResourceItem[], cfg: DrainConfig): DrainSummary {
  let daemonset = 0
  let mirror = 0
  let withEmptyDir = 0
  for (const p of pods) {
    const refs = (p as unknown as { ownerReferences?: Array<{ kind?: string }> }).ownerReferences ?? []
    if (refs.some((r) => r.kind === 'DaemonSet')) daemonset++
    const annotations = (p as unknown as { annotations?: Record<string, string> }).annotations ?? {}
    if (annotations['kubernetes.io/config.mirror']) mirror++
    const volumes = (p as unknown as { volumes?: Array<{ emptyDir?: unknown }> }).volumes ?? []
    if (volumes.some((v) => v.emptyDir != null)) withEmptyDir++
  }
  // Evictable = pods that drain.RunNodeDrain will actually try to
  // remove. Daemonset pods are skipped if ignoreDaemonsets is true
  // (the default). Mirror pods are never evicted regardless.
  const evictable = pods.length - mirror - (cfg.ignoreDaemonsets ? daemonset : 0)
  return {
    total: pods.length,
    daemonset,
    mirror,
    evictable,
    withEmptyDir,
  }
}

// PreflightTable mirrors SetImageModal's table chrome: thead in
// kb-elevated with tertiary uppercase labels, tbody rows on the
// card body. Keeps the visual language consistent with the other
// action modals in the app instead of inventing per-modal stat
// cards.
function PreflightTable({ summary }: { summary: DrainSummary }) {
  return (
    <div className="border border-kb-border rounded-lg overflow-hidden">
      <table className="w-full text-xs">
        <thead className="bg-kb-elevated border-b border-kb-border">
          <tr className="text-left text-kb-text-tertiary uppercase tracking-wider text-[10px]">
            <th className="px-3 py-2 font-medium">Pods on node</th>
            <th className="px-3 py-2 font-medium">Will evict</th>
            <th className="px-3 py-2 font-medium">DaemonSet (skip)</th>
            <th className="px-3 py-2 font-medium">Mirror (skip)</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td className="px-3 py-2.5 font-mono text-base text-kb-text-primary">
              {summary.total}
            </td>
            <td className="px-3 py-2.5 font-mono text-base text-status-warn font-semibold">
              {summary.evictable}
            </td>
            <td className="px-3 py-2.5 font-mono text-base text-kb-text-secondary">
              {summary.daemonset}
            </td>
            <td className="px-3 py-2.5 font-mono text-base text-kb-text-secondary">
              {summary.mirror}
            </td>
          </tr>
        </tbody>
      </table>
      {summary.withEmptyDir > 0 && (
        <div className="border-t border-kb-border px-3 py-2.5 flex items-start gap-2 text-[11px] text-status-warn bg-status-warn-dim">
          <Info className="w-3.5 h-3.5 mt-0.5 shrink-0" />
          <span className="text-kb-text-secondary">
            <span className="font-mono text-status-warn">{summary.withEmptyDir}</span>{' '}
            pod{summary.withEmptyDir === 1 ? '' : 's'} use emptyDir volumes — the drain will fail unless{' '}
            <span className="font-mono text-kb-text-primary">deleteEmptyDirData</span> is enabled.
          </span>
        </div>
      )}
      <div className="border-t border-kb-border px-3 py-2 text-[10px] text-kb-text-secondary">
        PDB-blocked detection is best-effort and arrives in a later cut; the actual drain respects PDBs server-side regardless.
      </div>
    </div>
  )
}

// ───────────────────────────── Guardrails ─────────────────────────────

interface Guardrails {
  block: boolean
  blockReason?: string
  warn: boolean
  warnReason?: string
}

function computeGuardrails(node: ResourceItem, allNodes: ResourceItem[]): Guardrails {
  // Block: only one schedulable node in the cluster. Draining it
  // would leave 0 nodes available, the cluster effectively offline
  // until we uncordon. This catches the common "I have a single
  // kind cluster" footgun.
  const schedulable = allNodes.filter(
    (n) => (n as unknown as { unschedulable?: boolean }).unschedulable !== true,
  )
  if (allNodes.length <= 1) {
    return {
      block: true,
      blockReason: 'This is the only node in the cluster — draining it leaves nothing schedulable.',
      warn: false,
    }
  }
  if (
    schedulable.length === 1 &&
    schedulable[0].name === node.name
  ) {
    return {
      block: true,
      blockReason: 'This is the only schedulable node — draining it leaves the cluster with 0 schedulable nodes. Uncordon another node first.',
      warn: false,
    }
  }

  // Warn: control-plane node. HA clusters legit drain CPs for
  // upgrades, but in single-CP clusters this is a foot-gun.
  const labels = (node as unknown as { labels?: Record<string, string> }).labels ?? {}
  const isControlPlane =
    labels['node-role.kubernetes.io/control-plane'] !== undefined ||
    labels['node-role.kubernetes.io/master'] !== undefined
  if (isControlPlane) {
    const cpCount = allNodes.filter((n) => {
      const l = (n as unknown as { labels?: Record<string, string> }).labels ?? {}
      return (
        l['node-role.kubernetes.io/control-plane'] !== undefined ||
        l['node-role.kubernetes.io/master'] !== undefined
      )
    }).length
    if (cpCount === 1) {
      return {
        block: true,
        blockReason:
          'This is the only control-plane node — draining it would take the apiserver offline. Drain a worker node instead, or add another control-plane node first.',
        warn: false,
      }
    }
    return {
      block: false,
      warn: true,
      warnReason:
        `Control-plane node (${cpCount} total). Make sure your other CP nodes are healthy before proceeding — the apiserver loses one of ${cpCount} replicas during the drain.`,
    }
  }

  return { block: false, warn: false }
}

// CordonedBanner — informational banner shown when the node is
// already cordoned. Distinguishes "still has evictable pods" from
// "nothing left to evict" because the operator's intent differs
// in each case. Lives separately from GuardrailBanner because the
// state isn't blocking and the visual treatment is calmer (info
// blue rather than warn amber).
function CordonedBanner({ alreadyDrained }: { alreadyDrained: boolean }) {
  return (
    <div className="flex items-start gap-2 text-xs border border-status-info/30 bg-status-info-dim text-status-info rounded p-3">
      <Info className="w-4 h-4 mt-0.5 shrink-0" />
      <div className="text-kb-text-secondary leading-relaxed">
        {alreadyDrained ? (
          <>
            <span className="font-semibold text-status-info">Node already drained.</span>
            {' '}It's currently cordoned and only DaemonSet / Mirror pods remain. Drain has nothing left to evict unless you change the flags below.
          </>
        ) : (
          <>
            <span className="font-semibold text-status-info">Node is already cordoned.</span>
            {' '}Re-running drain will only attempt to evict the pods that returned or weren't evicted last time. No-op if nothing is left.
          </>
        )}
      </div>
    </div>
  )
}

function GuardrailBanner({
  kind,
  message,
}: {
  kind: 'block' | 'warn'
  message: string
}) {
  const isBlock = kind === 'block'
  const colorClass = isBlock
    ? 'text-status-error border-status-error/30 bg-status-error-dim'
    : 'text-status-warn border-status-warn/30 bg-status-warn-dim'
  return (
    <div className={`flex items-start gap-2 text-xs border rounded p-3 ${colorClass}`}>
      {isBlock ? (
        <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
      ) : (
        <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
      )}
      <div className="text-kb-text-secondary leading-relaxed">{message}</div>
    </div>
  )
}

// ───────────────────────────── Form atoms ─────────────────────────────

function SliderRow({
  label,
  hint,
  value,
  min,
  max,
  step,
  unit,
  onChange,
}: {
  label: string
  hint: string
  value: number
  min: number
  max: number
  step: number
  unit: string
  onChange: (v: number) => void
}) {
  // Compute the filled-portion percentage for the gradient track.
  // We render the track as a linear-gradient: status-info (the
  // brand accent) up to the thumb, then var(--kb-elevated) (theme-
  // aware track color) for the empty portion. This is the standard
  // two-tone slider visual users expect from a volume / range
  // control. accent-color handles the thumb itself for both
  // Chromium and Firefox.
  const percent = max === min ? 0 : ((value - min) / (max - min)) * 100
  const trackStyle = {
    background: `linear-gradient(to right,
      #4c9aff 0%,
      #4c9aff ${percent}%,
      var(--kb-elevated) ${percent}%,
      var(--kb-elevated) 100%)`,
  } as React.CSSProperties
  return (
    <div>
      <div className="flex items-center justify-between mb-1.5">
        <div>
          <span className="text-xs text-kb-text-primary">{label}</span>
          <span className="text-[10px] text-kb-text-secondary ml-2">{hint}</span>
        </div>
        <span className="font-mono text-xs text-kb-text-primary">
          {value}
          {unit}
        </span>
      </div>
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        style={trackStyle}
        className="w-full h-1.5 rounded-full appearance-none cursor-pointer accent-status-info"
      />
    </div>
  )
}

function Toggle({
  label,
  hint,
  value,
  onChange,
}: {
  label: string
  hint: string
  value: boolean
  onChange: (v: boolean) => void
}) {
  // Match SetImageModal's "changed row" highlight — same
  // bg-status-info-dim/30 + border-status-info treatment for the
  // active state, plain border for inactive. The off-state needs
  // a visible track too: bg-kb-elevated reads as "neutral, off"
  // against both kb-card light and dark backgrounds, where
  // bg-kb-border was nearly invisible in dark mode.
  return (
    <button
      type="button"
      onClick={() => onChange(!value)}
      className={`text-left p-3 border rounded-lg transition-colors ${
        value
          ? 'border-status-info bg-status-info-dim/30'
          : 'border-kb-border hover:bg-kb-elevated'
      }`}
    >
      <div className="flex items-center justify-between mb-1">
        <span className="text-xs text-kb-text-primary">{label}</span>
        <span
          className={`w-7 h-4 rounded-full relative transition-colors ${
            value ? 'bg-status-info' : 'bg-kb-elevated border border-kb-border'
          }`}
        >
          <span
            className={`absolute top-0.5 w-3 h-3 rounded-full bg-white shadow transition-transform ${
              value ? 'translate-x-3.5' : 'translate-x-0.5'
            }`}
          />
        </span>
      </div>
      <div className="text-[10px] text-kb-text-secondary leading-relaxed">{hint}</div>
    </button>
  )
}

