import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { X, Trash2, AlertTriangle, Loader2, ExternalLink, Check, Minus, Info, CircleDot, Settings } from 'lucide-react'
import { KubeBoltLogo } from '@/components/shared/KubeBoltLogo'
import { api, ApiError, type Integration } from '@/services/api'
import { StatusBadge } from '@/pages/admin/IntegrationsPage'
import { AgentConfigureDialog } from '@/components/admin/AgentConfigureDialog'

interface Props {
  integration: Integration
  isAdmin: boolean
  onClose: () => void
}

// Phase state for the uninstall progress indicator. Visible steps
// give the operator proof that something is happening — a single
// spinner that eventually disappears feels broken when the whole
// operation takes a few seconds (pod termination is the slow part).
//
//   idle       — no uninstall in progress; confirm dialog visible
//   deleting   — DELETE request in flight
//   verifying  — request returned OK; we're polling Detect until
//                the cluster reports NotInstalled (pods terminate,
//                apiserver reconciles)
//   done       — detected NotInstalled; show a brief ✓ then reset
type UninstallPhase = 'idle' | 'deleting' | 'verifying' | 'done'

export function IntegrationDetailPanel({ integration: initial, isAdmin, onClose }: Props) {
  const qc = useQueryClient()
  const [phase, setPhase] = useState<UninstallPhase>('idle')

  // Refetch the single integration so we see the latest health /
  // pod counts without waiting for the 15 s list poll. During the
  // verifying phase we accelerate the poll so the user sees the
  // state transition within ~1 s of it happening on the cluster.
  const { data } = useQuery({
    queryKey: ['integration', initial.id],
    queryFn: () => api.getIntegration(initial.id),
    initialData: initial,
    refetchInterval: phase === 'verifying' ? 800 : 10_000,
  })
  const integration = data ?? initial

  const [confirming, setConfirming] = useState(false)
  const [configuring, setConfiguring] = useState(false)
  // Typed confirmation for the force path. The user has to type
  // "uninstall" (case-insensitive) to unlock the delete button —
  // kills the "one-click catastrophe" failure mode for resources we
  // didn't install ourselves.
  const [forceConfirmText, setForceConfirmText] = useState('')
  // selfTargeted is set when the backend refuses an uninstall with
  // 409 because the agent being removed is the one backing the
  // active cluster session — uninstalling would sever the only path
  // to that cluster. We surface this as a separate warning state
  // (above the standard force-confirm) so the operator sees the
  // session impact spelled out, not just a generic "force" prompt.
  const [selfTargeted, setSelfTargeted] = useState<{
    proxyClusterId?: string
    activeContext?: string
    hint?: string
  } | null>(null)

  const uninstall = useMutation({
    mutationFn: (opts: { force?: boolean }) => api.uninstallIntegration(integration.id, opts),
    onMutate: () => setPhase('deleting'),
    onSuccess: () => {
      // API returned — resources are marked for deletion, but pods
      // are still terminating and Detect may still report Installed
      // for a beat. Move to verifying and let the poll drive the
      // final transition.
      setPhase('verifying')
      qc.invalidateQueries({ queryKey: ['integration', integration.id] })
    },
    onError: (err) => {
      // 409 + selfTargetedProxy is the self-DoS guard from the
      // backend: keep the confirm dialog open and switch it into
      // self-targeted mode so the operator sees the explicit
      // "you'll cut your own session" warning before deciding
      // whether to force the operation.
      if (
        err instanceof ApiError &&
        err.status === 409 &&
        err.payload?.selfTargetedProxy === true
      ) {
        setSelfTargeted({
          proxyClusterId: typeof err.payload.proxyClusterId === 'string' ? err.payload.proxyClusterId : undefined,
          activeContext: typeof err.payload.activeContext === 'string' ? err.payload.activeContext : undefined,
          hint: typeof err.payload.hint === 'string' ? err.payload.hint : undefined,
        })
        setForceConfirmText('')
      }
      // Stay on the confirm dialog so the user sees the error and
      // can retry or cancel.
      setPhase('idle')
    },
  })

  // Drive the 'verifying' → 'done' transition off the real detect
  // state. When the cluster reports NotInstalled, flash a success
  // tick and reset the dialog back to its initial view.
  useEffect(() => {
    if (phase !== 'verifying') return
    if (integration.status === 'not_installed') {
      setPhase('done')
      const t = setTimeout(() => {
        qc.invalidateQueries({ queryKey: ['integrations'] })
        setPhase('idle')
        setConfirming(false)
        setForceConfirmText('')
        setSelfTargeted(null)
      }, 1200)
      return () => clearTimeout(t)
    }
  }, [phase, integration.status, integration.id, qc])

  const isInstalled = integration.status === 'installed' || integration.status === 'degraded'

  // Position below the Topbar (h-[52px], z-[400]) so the cluster
  // switcher + search remain accessible while the panel is open.
  // Panel and backdrop both start at top-[52px] for the same reason.
  return (
    <div className="fixed left-0 right-0 bottom-0 top-[52px] z-[150] flex items-stretch justify-end pointer-events-none">
      <div
        className="absolute inset-0 bg-black/30 backdrop-blur-sm pointer-events-auto"
        onClick={onClose}
      />
      <div className="relative w-full max-w-md bg-kb-card border-l border-kb-border flex flex-col pointer-events-auto">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-kb-border shrink-0">
          <div className="min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <h2 className="text-base font-semibold text-kb-text-primary truncate">{integration.name}</h2>
              <StatusBadge status={integration.status} />
            </div>
            <p className="text-[10px] font-mono text-kb-text-tertiary mt-0.5">{integration.id}</p>
          </div>
          <button
            onClick={onClose}
            className="p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors shrink-0"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="flex-1 overflow-y-auto p-5 space-y-5">
          {/* Description */}
          <p className="text-xs text-kb-text-secondary leading-relaxed">{integration.description}</p>

          {/* Facts grid */}
          <div className="grid grid-cols-2 gap-x-4 gap-y-3">
            {integration.version && (
              <Fact label="Version" value={integration.version} mono />
            )}
            {integration.namespace && (
              <Fact label="Namespace" value={integration.namespace} mono />
            )}
            {integration.health && (
              <>
                <Fact label="Pods ready" value={`${integration.health.podsReady}/${integration.health.podsDesired}`} mono />
                {integration.health.message && (
                  <div className="col-span-2">
                    <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1">Note</div>
                    <div className="text-[11px] text-kb-text-secondary">{integration.health.message}</div>
                  </div>
                )}
              </>
            )}
          </div>

          {/* Capabilities */}
          {integration.capabilities && integration.capabilities.length > 0 && (
            <div>
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-2">Capabilities</div>
              <div className="flex flex-wrap gap-1.5">
                {integration.capabilities.map((c) => (
                  <span
                    key={c}
                    className="px-2 py-0.5 rounded-full bg-kb-elevated text-[10px] font-mono text-kb-text-secondary border border-kb-border"
                  >
                    {c}
                  </span>
                ))}
              </div>
            </div>
          )}

          {/* Features */}
          {integration.features && integration.features.length > 0 && (
            <div>
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-2">Features</div>
              <div className="space-y-2">
                {integration.features.map((f) => (
                  <div
                    key={f.key}
                    className="flex items-start justify-between gap-3 p-3 rounded-lg bg-kb-elevated border border-kb-border"
                  >
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="text-sm text-kb-text-primary font-medium">{f.label}</span>
                        {f.value ? (
                          // Multi-state flag (e.g. permission tier).
                          // Render the value as a pill so the operator
                          // sees "Cluster-wide read" instead of just "On".
                          <span
                            className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-mono font-semibold ${
                              f.enabled
                                ? 'bg-status-ok-dim text-status-ok border border-status-ok/30'
                                : 'bg-kb-card text-kb-text-tertiary border border-kb-border'
                            }`}
                          >
                            {f.value}
                          </span>
                        ) : (
                          <span
                            className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded-full text-[9px] font-mono font-semibold uppercase tracking-wider ${
                              f.enabled ? 'bg-status-ok-dim text-status-ok' : 'bg-kb-card text-kb-text-tertiary'
                            }`}
                          >
                            {f.enabled ? <Check className="w-2.5 h-2.5" /> : <Minus className="w-2.5 h-2.5" />}
                            {f.enabled ? 'On' : 'Off'}
                          </span>
                        )}
                      </div>
                      {f.description && (
                        <p className="text-[11px] text-kb-text-secondary mt-1 leading-relaxed">{f.description}</p>
                      )}
                      {f.requires && f.requires.length > 0 && (
                        <div className="mt-1 text-[10px] text-kb-text-tertiary font-mono">
                          requires: {f.requires.join(', ')}
                        </div>
                      )}
                    </div>
                  </div>
                ))}
              </div>
              <p className="text-[10px] text-kb-text-tertiary italic mt-2">
                Toggling features from the UI lands in the next iteration. For now, edit the DaemonSet env or run <code>helm upgrade</code>.
              </p>
            </div>
          )}

          {/* Docs */}
          {integration.docsUrl && (
            <a
              href={integration.docsUrl}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1.5 text-xs text-kb-accent hover:underline"
            >
              <ExternalLink className="w-3.5 h-3.5" />
              Documentation
            </a>
          )}

          {/* Configure — only for managed installs. Externally
              managed installs keep their config in the source tool
              (Helm values, raw manifest) so editing from here would
              fight with the operator's source of truth. */}
          {isInstalled && integration.managed && isAdmin && (
            <div className="pt-4 border-t border-kb-border">
              <button
                onClick={() => setConfiguring(true)}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors"
              >
                <Settings className="w-3.5 h-3.5" />
                Configure
              </button>
              <p className="text-[10px] text-kb-text-tertiary mt-1.5">
                Edit backend URL, cluster name, Hubble toggle, image, scheduling, and resources. A rolling restart applies the changes.
              </p>
            </div>
          )}

          {/* Uninstall — two paths, same button label, distinct
              confirmation flow. Managed installs get a simple
              confirm. Externally-managed installs (Helm, kubectl)
              get an expanded warning + typed confirmation so the
              operator opts into the force path consciously. */}
          {isInstalled && isAdmin && (
            <div className="pt-4 border-t border-kb-border">
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-2">Danger zone</div>

              {/* Info banner when not managed by KubeBolt. Stays
                  visible in both states (confirming or not) so the
                  operator keeps seeing the context while deciding. */}
              {!integration.managed && (
                <div className="flex items-start gap-2 px-3 py-2.5 rounded-lg bg-status-info-dim border border-status-info/30 mb-3">
                  <Info className="w-4 h-4 text-status-info shrink-0 mt-0.5" />
                  <div className="text-[11px] text-kb-text-primary">
                    <div className="font-semibold mb-0.5">Not installed by KubeBolt</div>
                    <div className="text-kb-text-secondary">
                      This agent was deployed with helm or raw kubectl. The cleanest removal is with the same tool:
                    </div>
                    <ul className="mt-1.5 ml-4 list-disc text-kb-text-secondary space-y-0.5">
                      <li><code className="font-mono">helm uninstall kubebolt-agent -n {integration.namespace}</code></li>
                      <li><code className="font-mono">kubectl delete -f &lt;your manifest&gt;</code></li>
                    </ul>
                    <div className="mt-2 text-kb-text-secondary">
                      You can also force-uninstall from here. KubeBolt will delete the DaemonSet, RBAC, and ServiceAccount by name.
                      {' '}Helm release metadata will remain; run <code className="font-mono">helm uninstall</code> afterwards if you want it cleaned up too.
                    </div>
                  </div>
                </div>
              )}

              {confirming ? (
                <div className="p-3 rounded-lg bg-status-error-dim border border-status-error/30 space-y-3">
                  {/* Progress steps — only shown once the delete kicks
                      off. Until then the confirm form is visible so
                      the user sees both the warning and the input. */}
                  {phase !== 'idle' ? (
                    <UninstallProgress
                      phase={phase}
                      integration={integration}
                    />
                  ) : (
                    <>
                      {selfTargeted && (
                        <div className="flex items-start gap-2 px-3 py-2.5 rounded-lg bg-status-warning/10 border border-status-warning/40">
                          <KubeBoltLogo className="w-4 h-4 text-status-warning shrink-0 mt-0.5" />
                          <div className="text-[11px] text-kb-text-primary space-y-1">
                            <div className="font-semibold">This agent backs your active session</div>
                            <div className="text-kb-text-secondary">
                              {selfTargeted.activeContext && (
                                <>The active cluster <code className="font-mono">{selfTargeted.activeContext}</code> is reached through this agent's proxy. </>
                              )}
                              Uninstalling will make the cluster unreachable from KubeBolt — no auto-recovery.
                            </div>
                            {selfTargeted.hint && (
                              <div className="text-kb-text-secondary italic">{selfTargeted.hint}</div>
                            )}
                          </div>
                        </div>
                      )}

                      <div className="flex items-start gap-2">
                        <AlertTriangle className="w-4 h-4 text-status-error shrink-0 mt-0.5" />
                        <div className="text-[11px] text-kb-text-primary">
                          {integration.managed ? (
                            <>
                              This removes the DaemonSet and RBAC KubeBolt installed in <code className="font-mono">{integration.namespace}</code>.
                              The namespace itself is preserved.
                            </>
                          ) : (
                            <>
                              <span className="font-semibold">Force uninstall.</span>{' '}
                              Deletes the DaemonSet, RBAC, and ServiceAccount named <code className="font-mono">kubebolt-agent</code> in <code className="font-mono">{integration.namespace}</code> by name, regardless of which tool installed them.
                            </>
                          )}
                        </div>
                      </div>

                      {/* Typed confirmation — required for the force
                          path AND for the self-targeted-proxy path,
                          since both have catastrophic blast radius
                          (data loss for force; session loss for
                          self-targeted). */}
                      {(!integration.managed || selfTargeted) && (
                        <div>
                          <label className="block text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1">
                            Type <span className="text-status-error">uninstall</span> to confirm
                          </label>
                          <input
                            type="text"
                            value={forceConfirmText}
                            onChange={(e) => setForceConfirmText(e.target.value)}
                            className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-status-error"
                            autoFocus
                          />
                        </div>
                      )}

                      {uninstall.isError && !selfTargeted && (
                        <div className="text-[11px] text-status-error">{(uninstall.error as Error).message}</div>
                      )}
                    </>
                  )}

                  <div className="flex items-center justify-end gap-2">
                    <button
                      onClick={() => { setConfirming(false); setForceConfirmText(''); setSelfTargeted(null) }}
                      disabled={phase !== 'idle'}
                      className="px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      Cancel
                    </button>
                    <button
                      onClick={() => uninstall.mutate({ force: !integration.managed || !!selfTargeted })}
                      disabled={
                        phase !== 'idle' ||
                        ((!integration.managed || !!selfTargeted) && forceConfirmText.trim().toLowerCase() !== 'uninstall')
                      }
                      className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-status-error hover:bg-status-error/90 text-white text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      {phase === 'deleting' || phase === 'verifying' ? (
                        <><Loader2 className="w-3.5 h-3.5 animate-spin" /> In progress…</>
                      ) : phase === 'done' ? (
                        <><Check className="w-3.5 h-3.5" /> Done</>
                      ) : selfTargeted ? (
                        <><Trash2 className="w-3.5 h-3.5" /> Sever session and uninstall</>
                      ) : !integration.managed ? (
                        <><Trash2 className="w-3.5 h-3.5" /> Force uninstall</>
                      ) : (
                        <><Trash2 className="w-3.5 h-3.5" /> Uninstall</>
                      )}
                    </button>
                  </div>
                </div>
              ) : (
                <button
                  onClick={() => setConfirming(true)}
                  className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-status-error-dim hover:bg-status-error-dim/80 text-status-error text-xs border border-status-error/30 transition-colors"
                >
                  <Trash2 className="w-3.5 h-3.5" />
                  {integration.managed ? 'Uninstall' : 'Force uninstall'}
                </button>
              )}
            </div>
          )}
        </div>
      </div>

      {configuring && integration.id === 'agent' && (
        <AgentConfigureDialog
          integration={integration}
          onClose={() => setConfiguring(false)}
        />
      )}
    </div>
  )
}

function Fact({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1">{label}</div>
      <div className={`text-xs text-kb-text-primary ${mono ? 'font-mono' : ''}`}>{value}</div>
    </div>
  )
}

// UninstallProgress renders the step list visible while the
// uninstall is in flight. Each step has three states:
//   pending (empty circle) — haven't started
//   active  (spinner)       — running now
//   done    (check)         — completed
// Steps advance based on real phase transitions plus, for
// verification, the live pod count from Detect. No timers are used
// to fake progress — what the user sees reflects cluster state.
function UninstallProgress({
  phase,
  integration,
}: {
  phase: Exclude<UninstallPhase, 'idle'>
  integration: Integration
}) {
  const deletingDone = phase !== 'deleting'
  const verifyingActive = phase === 'verifying'
  const verifyingDone = phase === 'done'

  // While verifying, surface the live pod-terminating count so the
  // user sees cluster state catching up rather than a static spinner.
  const terminatingHint = (() => {
    if (!verifyingActive) return null
    if (integration.status === 'not_installed') return 'Resources gone'
    if (integration.health) {
      return `${integration.health.podsReady}/${integration.health.podsDesired} pods still running`
    }
    return 'Waiting for cluster state'
  })()

  return (
    <div className="space-y-2">
      <div className="text-[11px] font-semibold text-kb-text-primary">
        Uninstalling kubebolt-agent
      </div>
      <div className="space-y-1.5">
        <Step
          state={deletingDone ? 'done' : 'active'}
          label="Deleting workloads, RBAC, and ServiceAccount"
        />
        <Step
          state={verifyingDone ? 'done' : verifyingActive ? 'active' : 'pending'}
          label="Verifying removal"
          hint={terminatingHint}
        />
      </div>
      {phase === 'done' && (
        <div className="pt-1 text-[11px] text-status-ok">
          ✓ Uninstall complete. Closing…
        </div>
      )}
    </div>
  )
}

function Step({
  state,
  label,
  hint,
}: {
  state: 'pending' | 'active' | 'done'
  label: string
  hint?: string | null
}) {
  return (
    <div className="flex items-start gap-2">
      <div className="w-4 h-4 flex items-center justify-center shrink-0 mt-0.5">
        {state === 'done' ? (
          <Check className="w-3.5 h-3.5 text-status-ok" />
        ) : state === 'active' ? (
          <Loader2 className="w-3.5 h-3.5 text-kb-text-primary animate-spin" />
        ) : (
          <CircleDot className="w-3 h-3 text-kb-text-tertiary" />
        )}
      </div>
      <div className="min-w-0 flex-1">
        <div className={`text-[11px] ${state === 'pending' ? 'text-kb-text-tertiary' : 'text-kb-text-primary'}`}>
          {label}
        </div>
        {hint && (
          <div className="text-[10px] font-mono text-kb-text-tertiary mt-0.5">{hint}</div>
        )}
      </div>
    </div>
  )
}
