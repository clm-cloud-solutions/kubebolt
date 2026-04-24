import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Check, Loader2 } from 'lucide-react'
import { api, type AgentInstallConfig, type Integration } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { Modal } from '@/components/shared/Modal'

// AgentConfigureDialog edits an existing managed install in place.
// Field set mirrors AgentInstallWizard because the backend accepts
// the same shape (AgentInstallConfig) for both operations — the
// only differences here:
//   - Namespace is fixed (pinned to whatever the DS lives in; the
//     configure path never relocates the workload).
//   - Initial values come from the cluster via GET /config.
//   - The progress indicator waits for the DS rollout, not a
//     delete → detect transition.
interface Props {
  integration: Integration
  onClose: () => void
}

// Same helper shape AgentInstallWizard uses for ad-hoc KV pairs.
function KVRow({
  k, v, onChange, onRemove,
}: { k: string; v: string; onChange: (k: string, v: string) => void; onRemove: () => void }) {
  return (
    <div className="flex gap-2">
      <input
        type="text" placeholder="key" value={k}
        onChange={(e) => onChange(e.target.value, v)}
        className="flex-1 px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
      />
      <input
        type="text" placeholder="value" value={v}
        onChange={(e) => onChange(k, e.target.value)}
        className="flex-1 px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
      />
      <button
        type="button" onClick={onRemove}
        className="px-2 py-1 rounded bg-kb-card border border-kb-border text-xs text-kb-text-tertiary hover:text-status-error"
      >
        ×
      </button>
    </div>
  )
}

export function AgentConfigureDialog({ integration, onClose }: Props) {
  const qc = useQueryClient()

  // Load live config from the cluster. While this is pending the
  // form is hidden — editing stale values would defeat the purpose.
  const { data: initialConfig, isLoading, error: loadError } = useQuery({
    queryKey: ['integration-config', integration.id],
    queryFn: () => api.getIntegrationConfig<AgentInstallConfig>(integration.id),
    // Configure dialog is a one-shot — don't poll.
    refetchOnMount: 'always',
    staleTime: Infinity,
  })

  const [cfg, setCfg] = useState<AgentInstallConfig | null>(null)
  const [nodeSelector, setNodeSelector] = useState<Array<{ k: string; v: string }>>([])
  const [advancedOpen, setAdvancedOpen] = useState(true)
  const [phase, setPhase] = useState<'idle' | 'saving' | 'rolling' | 'done'>('idle')

  // Seed the form once the live config lands. Running this once is
  // enough — we never reset to server values after the user starts
  // editing, that'd clobber their work on a background refetch.
  useEffect(() => {
    if (initialConfig && !cfg) {
      setCfg(initialConfig)
      if (initialConfig.nodeSelector) {
        setNodeSelector(
          Object.entries(initialConfig.nodeSelector).map(([k, v]) => ({ k, v })),
        )
      }
    }
  }, [initialConfig, cfg])

  // Refetch the integration's live state so we can detect when the
  // rolling update has finished. Accelerated during the 'rolling'
  // phase; dormant otherwise.
  const { data: liveIntegration } = useQuery({
    queryKey: ['integration', integration.id],
    queryFn: () => api.getIntegration(integration.id),
    initialData: integration,
    refetchInterval: phase === 'rolling' ? 800 : 10_000,
  })

  const save = useMutation({
    mutationFn: (body: AgentInstallConfig) =>
      api.configureIntegration(integration.id, body),
    onMutate: () => setPhase('saving'),
    onSuccess: () => {
      setPhase('rolling')
      qc.invalidateQueries({ queryKey: ['integration', integration.id] })
    },
    onError: () => setPhase('idle'),
  })

  // Treat the rollout as complete when pods ready match desired.
  // Kubernetes drives the rolling restart — we just observe.
  useEffect(() => {
    if (phase !== 'rolling' || !liveIntegration) return
    const h = liveIntegration.health
    if (h && h.podsDesired > 0 && h.podsReady === h.podsDesired) {
      setPhase('done')
      const t = setTimeout(() => {
        qc.invalidateQueries({ queryKey: ['integrations'] })
        onClose()
      }, 1200)
      return () => clearTimeout(t)
    }
  }, [phase, liveIntegration, qc, onClose])

  function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!cfg) return

    const ns: Record<string, string> = {}
    for (const { k, v } of nodeSelector) {
      const key = k.trim()
      if (!key) continue
      ns[key] = v
    }

    const tls = cfg.hubbleRelayTls?.existingSecret?.trim()
      ? {
          existingSecret: cfg.hubbleRelayTls.existingSecret.trim(),
          serverName: cfg.hubbleRelayTls.serverName?.trim() || undefined,
        }
      : undefined
    const res = cfg.resources
    const hasAnyRes = !!(res?.cpuRequest || res?.cpuLimit || res?.memoryRequest || res?.memoryLimit)

    const body: AgentInstallConfig = {
      ...cfg,
      backendUrl: cfg.backendUrl.trim(),
      clusterName: cfg.clusterName?.trim() || undefined,
      imageTag: cfg.imageTag?.trim() || undefined,
      imageRepo: cfg.imageRepo?.trim() || undefined,
      hubbleRelayAddress: cfg.hubbleRelayAddress?.trim() || undefined,
      hubbleRelayTls: tls,
      priorityClassName: cfg.priorityClassName?.trim() || undefined,
      nodeSelector: Object.keys(ns).length > 0 ? ns : undefined,
      resources: hasAnyRes ? res : undefined,
    }
    if (!body.backendUrl) return
    save.mutate(body)
  }

  const inFlight = phase === 'saving' || phase === 'rolling'

  // Modal portals to document.body — that sidesteps any lingering
  // ambiguity from the parent detail panel's own DOM tree (React's
  // synthetic events + pointer-events cascades used to let clicks
  // on form inputs read as a parent-close before we portaled).
  return (
    <Modal badge="Configure" title="KubeBolt Agent" onClose={onClose} size="xl">
        {isLoading && (
          <div className="flex-1 flex items-center justify-center p-8">
            <LoadingSpinner />
          </div>
        )}
        {loadError && (
          <div className="flex-1 p-5">
            <div className="flex items-start gap-2 px-3 py-2.5 rounded-lg bg-status-error-dim border border-status-error/30">
              <AlertTriangle className="w-4 h-4 text-status-error shrink-0 mt-0.5" />
              <span className="text-[11px] text-status-error">
                Failed to load current config: {(loadError as Error).message}
              </span>
            </div>
          </div>
        )}

        {cfg && !loadError && (
          <form onSubmit={submit} className="flex-1 overflow-y-auto p-5 space-y-5">
            {/* While the rollout is happening we show a progress
                tracker in place of the form so the operator has
                clear, step-anchored feedback. */}
            {inFlight || phase === 'done' ? (
              <ConfigureProgress
                phase={phase}
                integration={liveIntegration ?? integration}
              />
            ) : (
              <>
                <div>
                  <label className="block text-[11px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1.5">
                    Backend URL <span className="text-status-error">*</span>
                  </label>
                  <input
                    type="text" required value={cfg.backendUrl}
                    onChange={(e) => setCfg({ ...cfg, backendUrl: e.target.value })}
                    className="w-full px-3 py-2 rounded-lg bg-kb-elevated border border-kb-border text-sm text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                  />
                </div>

                <div>
                  <label className="block text-[11px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1.5">
                    Cluster name <span className="text-kb-text-tertiary font-normal normal-case">(display label)</span>
                  </label>
                  <input
                    type="text" placeholder="e.g. kind-kubebolt-dev"
                    value={cfg.clusterName ?? ''}
                    onChange={(e) => setCfg({ ...cfg, clusterName: e.target.value })}
                    className="w-full px-3 py-2 rounded-lg bg-kb-elevated border border-kb-border text-sm text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                  />
                </div>

                <div className="flex items-start justify-between gap-3 p-3 rounded-lg bg-kb-elevated border border-kb-border">
                  <div>
                    <div className="text-sm text-kb-text-primary font-medium">Hubble flow collector</div>
                    <p className="text-[11px] text-kb-text-secondary mt-0.5">
                      Toggle the L4/L7/DNS flow stream. No-ops when Cilium isn't installed.
                    </p>
                  </div>
                  <button
                    type="button" role="switch" aria-checked={cfg.hubbleEnabled ?? true}
                    onClick={() => setCfg({ ...cfg, hubbleEnabled: !(cfg.hubbleEnabled ?? true) })}
                    className={`relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors ${(cfg.hubbleEnabled ?? true) ? 'bg-kb-accent' : 'bg-kb-border'}`}
                  >
                    <span className={`inline-block h-4 w-4 rounded-full bg-white transition-transform ${(cfg.hubbleEnabled ?? true) ? 'translate-x-[18px]' : 'translate-x-0.5'} mt-0.5`} />
                  </button>
                </div>

                <div>
                  <button
                    type="button"
                    onClick={() => setAdvancedOpen(!advancedOpen)}
                    className="text-[11px] font-mono text-kb-text-tertiary uppercase tracking-wider hover:text-kb-text-primary"
                  >
                    {advancedOpen ? '▾' : '▸'} Advanced
                  </button>
                  {advancedOpen && (
                    <div className="mt-3 space-y-4 p-3 rounded-lg bg-kb-elevated border border-kb-border">
                      <section className="space-y-2">
                        <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Image</div>
                        <div>
                          <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Namespace</label>
                          <input
                            type="text" value={cfg.namespace ?? ''} disabled
                            className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-tertiary font-mono opacity-60 cursor-not-allowed"
                          />
                          <p className="text-[10px] text-kb-text-tertiary mt-1">
                            Namespace is fixed. Move the agent by uninstall + reinstall.
                          </p>
                        </div>
                        <div className="grid grid-cols-2 gap-3">
                          <div>
                            <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Image repo</label>
                            <input
                              type="text" value={cfg.imageRepo ?? ''}
                              onChange={(e) => setCfg({ ...cfg, imageRepo: e.target.value })}
                              className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                            />
                          </div>
                          <div>
                            <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Image tag</label>
                            <input
                              type="text" value={cfg.imageTag ?? ''}
                              onChange={(e) => setCfg({ ...cfg, imageTag: e.target.value })}
                              className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                            />
                          </div>
                        </div>
                        <div>
                          <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Pull policy</label>
                          <select
                            value={cfg.imagePullPolicy ?? ''}
                            onChange={(e) => setCfg({ ...cfg, imagePullPolicy: (e.target.value || undefined) as AgentInstallConfig['imagePullPolicy'] })}
                            className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                          >
                            <option value="">auto</option>
                            <option value="Always">Always</option>
                            <option value="IfNotPresent">IfNotPresent</option>
                            <option value="Never">Never (local-only image)</option>
                          </select>
                        </div>
                      </section>

                      <section className="space-y-2 pt-3 border-t border-kb-border/60">
                        <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Hubble relay</div>
                        <div>
                          <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Relay address override</label>
                          <input
                            type="text" placeholder="hubble-relay.kube-system.svc.cluster.local:80"
                            value={cfg.hubbleRelayAddress ?? ''}
                            onChange={(e) => setCfg({ ...cfg, hubbleRelayAddress: e.target.value })}
                            className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                          />
                        </div>
                        <div>
                          <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">TLS Secret (existing)</label>
                          <input
                            type="text"
                            value={cfg.hubbleRelayTls?.existingSecret ?? ''}
                            onChange={(e) => setCfg({ ...cfg, hubbleRelayTls: { ...(cfg.hubbleRelayTls ?? { existingSecret: '' }), existingSecret: e.target.value } })}
                            className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                          />
                        </div>
                        <div>
                          <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Server name (SNI)</label>
                          <input
                            type="text"
                            value={cfg.hubbleRelayTls?.serverName ?? ''}
                            onChange={(e) => setCfg({ ...cfg, hubbleRelayTls: { ...(cfg.hubbleRelayTls ?? { existingSecret: '' }), serverName: e.target.value } })}
                            className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                          />
                        </div>
                      </section>

                      <section className="space-y-2 pt-3 border-t border-kb-border/60">
                        <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Scheduling</div>
                        <div>
                          <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Priority class name</label>
                          <input
                            type="text" value={cfg.priorityClassName ?? ''}
                            onChange={(e) => setCfg({ ...cfg, priorityClassName: e.target.value })}
                            className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                          />
                        </div>
                        <div>
                          <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Node selector</label>
                          <div className="space-y-1.5">
                            {nodeSelector.map((pair, i) => (
                              <KVRow
                                key={i} k={pair.k} v={pair.v}
                                onChange={(k, v) => {
                                  const next = [...nodeSelector]
                                  next[i] = { k, v }
                                  setNodeSelector(next)
                                }}
                                onRemove={() => setNodeSelector(nodeSelector.filter((_, j) => j !== i))}
                              />
                            ))}
                            <button
                              type="button"
                              onClick={() => setNodeSelector([...nodeSelector, { k: '', v: '' }])}
                              className="text-[10px] font-mono text-kb-accent hover:underline"
                            >
                              + Add selector
                            </button>
                          </div>
                        </div>
                      </section>

                      <section className="space-y-2 pt-3 border-t border-kb-border/60">
                        <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Resources</div>
                        <div className="grid grid-cols-2 gap-3">
                          <div>
                            <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">CPU request</label>
                            <input
                              type="text" value={cfg.resources?.cpuRequest ?? ''}
                              onChange={(e) => setCfg({ ...cfg, resources: { ...(cfg.resources ?? {}), cpuRequest: e.target.value } })}
                              className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                            />
                          </div>
                          <div>
                            <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">CPU limit</label>
                            <input
                              type="text" value={cfg.resources?.cpuLimit ?? ''}
                              onChange={(e) => setCfg({ ...cfg, resources: { ...(cfg.resources ?? {}), cpuLimit: e.target.value } })}
                              className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                            />
                          </div>
                          <div>
                            <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Memory request</label>
                            <input
                              type="text" value={cfg.resources?.memoryRequest ?? ''}
                              onChange={(e) => setCfg({ ...cfg, resources: { ...(cfg.resources ?? {}), memoryRequest: e.target.value } })}
                              className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                            />
                          </div>
                          <div>
                            <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Memory limit</label>
                            <input
                              type="text" value={cfg.resources?.memoryLimit ?? ''}
                              onChange={(e) => setCfg({ ...cfg, resources: { ...(cfg.resources ?? {}), memoryLimit: e.target.value } })}
                              className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                            />
                          </div>
                        </div>
                      </section>

                      <section className="space-y-2 pt-3 border-t border-kb-border/60">
                        <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Logging</div>
                        <div>
                          <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Log level</label>
                          <select
                            value={cfg.logLevel ?? 'info'}
                            onChange={(e) => setCfg({ ...cfg, logLevel: e.target.value })}
                            className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                          >
                            <option value="debug">debug</option>
                            <option value="info">info</option>
                            <option value="warn">warn</option>
                            <option value="error">error</option>
                          </select>
                        </div>
                      </section>
                    </div>
                  )}
                </div>

                {save.isError && (
                  <div className="flex items-start gap-2 px-3 py-2.5 rounded-lg bg-status-error-dim border border-status-error/30">
                    <AlertTriangle className="w-4 h-4 text-status-error shrink-0 mt-0.5" />
                    <div className="text-[11px] text-status-error">{(save.error as Error).message}</div>
                  </div>
                )}
              </>
            )}
          </form>
        )}

        <div className="flex items-center justify-end gap-2 px-5 py-3 border-t border-kb-border shrink-0">
          <button
            type="button"
            onClick={onClose}
            disabled={inFlight}
            className="px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors disabled:opacity-50"
          >
            {phase === 'done' ? 'Close' : 'Cancel'}
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={!cfg || !cfg.backendUrl?.trim() || inFlight || phase === 'done'}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-accent hover:bg-kb-accent-hover text-kb-on-accent text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {phase === 'saving' ? (
              <><Loader2 className="w-3.5 h-3.5 animate-spin" /> Applying…</>
            ) : phase === 'rolling' ? (
              <><Loader2 className="w-3.5 h-3.5 animate-spin" /> Rolling out…</>
            ) : phase === 'done' ? (
              <><Check className="w-3.5 h-3.5" /> Saved</>
            ) : (
              <>Save changes</>
            )}
          </button>
        </div>
    </Modal>
  )
}

function ConfigureProgress({
  phase,
  integration,
}: {
  phase: 'saving' | 'rolling' | 'done' | 'idle'
  integration: Integration
}) {
  const savingDone = phase !== 'saving'
  const rollingActive = phase === 'rolling'
  const rollingDone = phase === 'done'

  const rollingHint = (() => {
    if (!rollingActive) return null
    const h = integration.health
    if (!h) return 'Waiting for cluster state'
    return `${h.podsReady}/${h.podsDesired} pods on the new spec`
  })()

  return (
    <div className="space-y-3">
      <div className="text-sm font-semibold text-kb-text-primary">Applying configuration</div>
      <div className="space-y-1.5">
        <Step state={savingDone ? 'done' : 'active'} label="Updating DaemonSet spec" />
        <Step
          state={rollingDone ? 'done' : rollingActive ? 'active' : 'pending'}
          label="Rolling out to nodes"
          hint={rollingHint}
        />
      </div>
      {phase === 'done' && (
        <div className="pt-1 text-[11px] text-status-ok">✓ Configuration applied. Closing…</div>
      )}
    </div>
  )
}

function Step({ state, label, hint }: { state: 'pending' | 'active' | 'done'; label: string; hint?: string | null }) {
  return (
    <div className="flex items-start gap-2">
      <div className="w-4 h-4 flex items-center justify-center shrink-0 mt-0.5">
        {state === 'done' ? (
          <Check className="w-3.5 h-3.5 text-status-ok" />
        ) : state === 'active' ? (
          <Loader2 className="w-3.5 h-3.5 text-kb-text-primary animate-spin" />
        ) : (
          <div className="w-2.5 h-2.5 rounded-full border border-kb-text-tertiary" />
        )}
      </div>
      <div className="min-w-0 flex-1">
        <div className={`text-[11px] ${state === 'pending' ? 'text-kb-text-tertiary' : 'text-kb-text-primary'}`}>{label}</div>
        {hint && <div className="text-[10px] font-mono text-kb-text-tertiary mt-0.5">{hint}</div>}
      </div>
    </div>
  )
}
