import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { AlertTriangle, Bug, ChevronDown, ChevronRight, Loader2, X } from 'lucide-react'
import { api, ApiError } from '@/services/api'
import type { ResourceItem } from '@/types/kubernetes'

// DebugPodModal spawns an ephemeral container inside a running pod via
// the apiserver's `ephemeralcontainers` subresource. Spec #09 V2 /
// Item 4 / C1 audit decision — the only post-1.11 deferred action
// from the pod-actions audit.
//
// Why this exists vs the Terminal tab's exec path: distroless / scratch /
// read-only-fs containers don't have a shell binary on disk, so
// `kubectl exec` returns "exec: \"sh\": executable file not found in
// $PATH". An ephemeral container runs alongside the target with shared
// pid+net namespaces, bringing its own busybox / netshoot / etc. tools.
//
// UX shape:
//   - Image picker: 4 presets (busybox / netshoot / alpine / ubuntu) +
//     custom-text fallback. busybox is the default — smallest, gets the
//     shell into a distroless namespace fastest.
//   - Target container: required dropdown of the pod's containers
//     (regular + init). Without targeting, the debug container only
//     shares the pod's network namespace, defeating the main distroless
//     use case.
//   - Advanced (collapsible): command override + shareProcessNamespace.
//     Defaults work for 99% of cases; keeping advanced off-screen avoids
//     scaring operators with knobs they don't need.
//
// On success: caller is expected to navigate to the Terminal tab with
// `?tab=terminal&container=<ephemeralName>` so the terminal opens
// pre-selected on the new container (no manual dropdown step).

const PRESET_IMAGES: Array<{ value: string; label: string; helper: string }> = [
  {
    value: 'busybox:latest',
    label: 'busybox',
    helper: 'Tiny (~1 MB). Shell + standard core-utils. Default — gets into a distroless namespace fast.',
  },
  {
    value: 'nicolaka/netshoot:latest',
    label: 'netshoot',
    helper: 'Networking toolkit (curl, dig, tcpdump, mtr, iperf, etc.). Best for connectivity debugging.',
  },
  {
    value: 'alpine:latest',
    label: 'alpine',
    helper: 'Package manager (apk) for installing extras inside the debug session.',
  },
  {
    value: 'ubuntu:latest',
    label: 'ubuntu',
    helper: 'Full Debian-family toolkit. Heavier (~70 MB) but the most familiar shell environment.',
  },
]

const CUSTOM_VALUE = '__custom__'

interface DebugPodModalProps {
  namespace: string
  name: string
  item: ResourceItem
  onClose: () => void
  // Called with the auto-generated ephemeral container name once the
  // apiserver accepts the spawn. Caller is responsible for routing to
  // the Terminal tab with the new container pre-selected.
  onSpawned: (ephemeralContainerName: string) => void
}

export function DebugPodModal({ namespace, name, item, onClose, onSpawned }: DebugPodModalProps) {
  // Build the target-container list from the pod's regular + init
  // containers. Ephemeral containers themselves are EXCLUDED — debug-
  // a-debug rarely makes sense and the dropdown stays cleaner.
  const targetCandidates: Array<{ name: string; kind: 'container' | 'init' }> = (() => {
    const out: Array<{ name: string; kind: 'container' | 'init' }> = []
    const containers = Array.isArray(item.containers) ? (item.containers as Array<Record<string, unknown>>) : []
    for (const c of containers) {
      if (c.ephemeral) continue
      out.push({ name: String(c.name ?? ''), kind: 'container' })
    }
    // initContainers don't ship in `item.containers` today; if/when the
    // backend surfaces them the loop above already excludes ephemerals,
    // so adding init here would be a separate enhancement.
    return out
  })()

  const [imageChoice, setImageChoice] = useState<string>(PRESET_IMAGES[0].value)
  const [customImage, setCustomImage] = useState<string>('')
  const [targetContainer, setTargetContainer] = useState<string>(targetCandidates[0]?.name ?? '')
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [command, setCommand] = useState<string>('') // space-separated; empty = backend default ("sh")
  const [shareProcessNamespace, setShareProcessNamespace] = useState(false)

  const [spawning, setSpawning] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    function handleEsc(e: KeyboardEvent) {
      if (e.key === 'Escape' && !spawning) onClose()
    }
    document.addEventListener('keydown', handleEsc)
    return () => document.removeEventListener('keydown', handleEsc)
  }, [onClose, spawning])

  const effectiveImage = imageChoice === CUSTOM_VALUE ? customImage.trim() : imageChoice
  const canSubmit = !!effectiveImage && !!targetContainer && !spawning

  async function handleSpawn() {
    if (!canSubmit) return
    setSpawning(true)
    setError(null)
    try {
      // Parse the command override: simple whitespace split. Operators
      // who need quoted args (rare for debug shells) can drop into the
      // shell post-spawn and run the real command interactively.
      const parsedCommand = command.trim() ? command.trim().split(/\s+/) : undefined
      const resp = await api.debugPod(
        namespace,
        name,
        {
          image: effectiveImage,
          targetContainer,
          command: parsedCommand,
          shareProcessNamespace,
        },
      )
      onSpawned(resp.ephemeralContainerName)
    } catch (err) {
      if (err instanceof ApiError) {
        // The backend returns 400 with the message body for validation
        // (bad target container, missing image); 503 for cluster
        // disconnect; 5xx with apiserver errors otherwise.
        setError(err.message || `HTTP ${err.status}`)
      } else {
        setError(err instanceof Error ? err.message : 'Failed to spawn debug container')
      }
      setSpawning(false)
    }
  }

  const presetHelper = imageChoice !== CUSTOM_VALUE
    ? PRESET_IMAGES.find((p) => p.value === imageChoice)?.helper
    : null

  return createPortal(
    <div className="fixed inset-0 z-[99999] flex items-center justify-center" onClick={spawning ? undefined : onClose}>
      <div className="absolute inset-0 bg-black/70 backdrop-blur-sm" />
      <div
        className="relative w-[90vw] max-w-lg bg-kb-card border border-kb-border rounded-xl shadow-2xl flex flex-col overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="px-5 py-4 flex items-start justify-between">
          <div className="flex items-start gap-3">
            <div className="w-8 h-8 rounded-lg bg-status-info-dim flex items-center justify-center shrink-0 mt-0.5">
              <Bug className="w-4 h-4 text-status-info" />
            </div>
            <div>
              <h4 className="text-sm font-semibold text-kb-text-primary">Debug pod</h4>
              <p className="text-[11px] text-kb-text-tertiary">
                Spawn an ephemeral container with a shell. Useful for distroless / scratch images.
              </p>
            </div>
          </div>
          <button
            onClick={onClose}
            disabled={spawning}
            className="p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors disabled:opacity-40"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Target pod info */}
        <div className="mx-5 px-3 py-2.5 rounded-lg bg-kb-bg border border-kb-border">
          <div className="text-[11px] font-mono text-kb-text-secondary space-y-0.5">
            <div>Pod: <span className="text-kb-text-primary">{namespace}/{name}</span></div>
          </div>
        </div>

        <div className="px-5 py-4 space-y-4 max-h-[60vh] overflow-y-auto">
          {/* Image picker */}
          <div className="space-y-1.5">
            <label className="block text-[11px] font-semibold text-kb-text-primary uppercase tracking-wider">
              Debug image
            </label>
            <select
              value={imageChoice}
              onChange={(e) => setImageChoice(e.target.value)}
              disabled={spawning}
              className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent disabled:opacity-40"
            >
              {PRESET_IMAGES.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label} ({p.value})
                </option>
              ))}
              <option value={CUSTOM_VALUE}>Custom image…</option>
            </select>
            {presetHelper && (
              <p className="text-[11px] text-kb-text-tertiary leading-relaxed">{presetHelper}</p>
            )}
            {imageChoice === CUSTOM_VALUE && (
              <input
                type="text"
                placeholder="registry.example.com/team/debugger:1.0"
                value={customImage}
                onChange={(e) => setCustomImage(e.target.value)}
                disabled={spawning}
                className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent disabled:opacity-40"
              />
            )}
          </div>

          {/* Target container */}
          <div className="space-y-1.5">
            <label className="block text-[11px] font-semibold text-kb-text-primary uppercase tracking-wider">
              Target container
            </label>
            {targetCandidates.length === 0 ? (
              <div className="px-2 py-1.5 rounded-md bg-status-warn-dim/40 border border-status-warn-dim text-[11px] text-status-warn">
                Pod has no containers — cannot spawn a debug session.
              </div>
            ) : (
              <select
                value={targetContainer}
                onChange={(e) => setTargetContainer(e.target.value)}
                disabled={spawning}
                className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent disabled:opacity-40"
              >
                {targetCandidates.map((c) => (
                  <option key={c.name} value={c.name}>
                    {c.name}
                    {c.kind === 'init' ? ' (init)' : ''}
                  </option>
                ))}
              </select>
            )}
            <p className="text-[11px] text-kb-text-tertiary leading-relaxed">
              Debug container shares the target's pid + network namespaces. Required for{' '}
              <code className="font-mono">ps</code> / <code className="font-mono">/proc</code>{' '}
              access against the target's processes.
            </p>
          </div>

          {/* Advanced (collapsible) */}
          <div className="border border-kb-border rounded-md">
            <button
              type="button"
              onClick={() => setAdvancedOpen((v) => !v)}
              className="w-full px-3 py-2 flex items-center gap-1.5 text-[11px] font-semibold text-kb-text-secondary hover:bg-kb-card-hover transition-colors"
            >
              {advancedOpen ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
              Advanced
            </button>
            {advancedOpen && (
              <div className="px-3 pb-3 pt-1 space-y-3">
                <div className="space-y-1.5">
                  <label className="block text-[10px] font-semibold text-kb-text-primary uppercase tracking-wider">
                    Command (space-separated)
                  </label>
                  <input
                    type="text"
                    placeholder="sh  (default)"
                    value={command}
                    onChange={(e) => setCommand(e.target.value)}
                    disabled={spawning}
                    className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent disabled:opacity-40"
                  />
                  <p className="text-[10px] text-kb-text-tertiary leading-relaxed">
                    Default <code className="font-mono">sh</code>. Override to e.g.{' '}
                    <code className="font-mono">/bin/bash</code> when the image ships bash.
                  </p>
                </div>
                <div className="flex items-start gap-2">
                  <input
                    type="checkbox"
                    id="shareProcNs"
                    checked={shareProcessNamespace}
                    onChange={(e) => setShareProcessNamespace(e.target.checked)}
                    disabled={spawning}
                    className="mt-0.5"
                  />
                  <label htmlFor="shareProcNs" className="text-[11px] text-kb-text-secondary leading-relaxed cursor-pointer">
                    <span className="font-semibold">Share process namespace</span> —{' '}
                    apiserver-level flag. Rarely needed when targetContainer is set
                    (which already shares pid ns); enable only for niche cases like
                    multi-container debugging in older clusters.
                  </label>
                </div>
              </div>
            )}
          </div>

          {/* Error display */}
          {error && (
            <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
              <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
              <div className="break-words">{error}</div>
            </div>
          )}

          {/* Lifecycle note. Two K8s API constraints to surface upfront:
              (1) ephemerals are append-only — can't be removed via
              PATCH/DELETE; (2) our Terminal tab uses exec (not attach),
              so the shell session and the ephemeral's PID 1 are
              separate process trees. Closing the terminal exits the
              EXEC process, not PID 1. The ephemeral keeps running until
              the pod restarts. Important to be explicit because the
              expectation from `kubectl debug` (which uses attach) is
              the opposite — operators coming from that background
              expect exit-to-terminate. */}
          <div className="text-[10px] text-kb-text-tertiary leading-relaxed border-t border-kb-border pt-3">
            <strong className="text-kb-text-secondary">Lifecycle:</strong> ephemeral containers
            stay <code className="font-mono">Running</code> in the pod spec until the pod is
            restarted — Kubernetes doesn't expose a "remove ephemeral" API. Closing your terminal
            session ends YOUR interaction (KubeBolt's terminal uses exec, not attach), but the
            container's PID 1 keeps running. To clear it, use the toolbar's{' '}
            <strong className="text-kb-text-secondary">Restart</strong> action which recreates
            the pod from scratch.
          </div>
        </div>

        {/* Actions */}
        <div className="px-5 py-4 flex gap-2 justify-end border-t border-kb-border">
          <button
            onClick={onClose}
            disabled={spawning}
            className="px-4 py-2 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-secondary hover:bg-kb-card-hover transition-colors disabled:opacity-40"
          >
            Cancel
          </button>
          <button
            onClick={handleSpawn}
            disabled={!canSubmit}
            className="px-4 py-2 text-xs font-medium bg-kb-accent text-white rounded-lg hover:bg-kb-accent/90 transition-colors disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-1.5"
          >
            {spawning && <Loader2 className="w-3.5 h-3.5 animate-spin" />}
            {spawning ? 'Spawning…' : 'Spawn & open terminal'}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  )
}
