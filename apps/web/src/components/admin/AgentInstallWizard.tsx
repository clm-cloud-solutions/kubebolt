import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { X, AlertTriangle, Check, Loader2 } from 'lucide-react'
import { api, type AgentInstallConfig, type Integration } from '@/services/api'

// The agent ships samples to the KubeBolt backend over gRPC :9090.
// These are the shapes that typically work — the user picks the one
// matching their deployment topology. Free text is always allowed
// for custom cases.
const backendPresets = [
  { label: 'In-cluster backend (Helm release "kubebolt" in namespace "kubebolt")', value: 'kubebolt.kubebolt.svc.cluster.local:9090' },
  { label: 'Backend on host (Docker Desktop)', value: 'host.docker.internal:9090' },
]

interface Props {
  integration: Integration
  onClose: () => void
}

// One-row helper for the node selector editor. Keeps the wizard
// body readable.
function KVRow({
  k, v, onChange, onRemove,
}: { k: string; v: string; onChange: (k: string, v: string) => void; onRemove: () => void }) {
  return (
    <div className="flex gap-2">
      <input
        type="text"
        placeholder="key"
        value={k}
        onChange={(e) => onChange(e.target.value, v)}
        className="flex-1 px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
      />
      <input
        type="text"
        placeholder="value"
        value={v}
        onChange={(e) => onChange(k, e.target.value)}
        className="flex-1 px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
      />
      <button
        type="button"
        onClick={onRemove}
        className="px-2 py-1 rounded bg-kb-card border border-kb-border text-xs text-kb-text-tertiary hover:text-status-error"
      >
        ×
      </button>
    </div>
  )
}

export function AgentInstallWizard({ integration: _integration, onClose }: Props) {
  const qc = useQueryClient()
  const [cfg, setCfg] = useState<AgentInstallConfig>({
    backendUrl: '',
    clusterName: '',
    hubbleEnabled: true,
  })
  // NodeSelector as ordered pairs so users can add empty rows and
  // fill them in; converted to a map on submit.
  const [nodeSelector, setNodeSelector] = useState<Array<{ k: string; v: string }>>([])
  const [advancedOpen, setAdvancedOpen] = useState(false)

  // Server-side conflicts come back as HTTP 409 with the conflicting
  // resource broken out. We render a tailored hint for that case.
  const [conflict, setConflict] = useState<{
    kind: string
    namespace?: string
    name: string
    reason: string
  } | null>(null)

  const mut = useMutation({
    mutationFn: (body: AgentInstallConfig) => api.installIntegration('agent', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['integrations'] })
      onClose()
    },
    onError: async (err: Error) => {
      // ApiError from fetchJSON carries the response body as .payload;
      // the 409 shape is { error, conflict: { Kind, Namespace, Name, Reason } }.
      // We degrade gracefully when the shape doesn't match.
      const payload = (err as unknown as { payload?: { conflict?: { Kind: string; Namespace: string; Name: string; Reason: string } } }).payload
      if (payload?.conflict) {
        setConflict({
          kind: payload.conflict.Kind,
          namespace: payload.conflict.Namespace,
          name: payload.conflict.Name,
          reason: payload.conflict.Reason,
        })
      }
    },
  })

  function submit(e: React.FormEvent) {
    e.preventDefault()
    setConflict(null)

    // Collapse the KV rows into a map, skipping blank keys so an
    // empty row doesn't become {"": ""} on the backend.
    const ns: Record<string, string> = {}
    for (const { k, v } of nodeSelector) {
      const key = k.trim()
      if (!key) continue
      ns[key] = v
    }

    // Drop empty sub-objects so Hubble TLS / resources are absent
    // rather than an empty shell the backend would have to special-case.
    const tls = cfg.hubbleRelayTls?.existingSecret?.trim()
      ? { existingSecret: cfg.hubbleRelayTls.existingSecret.trim(), serverName: cfg.hubbleRelayTls.serverName?.trim() || undefined }
      : undefined

    const res = cfg.resources
    const hasAnyRes = !!(res?.cpuRequest || res?.cpuLimit || res?.memoryRequest || res?.memoryLimit)

    const trimmed: AgentInstallConfig = {
      ...cfg,
      backendUrl: cfg.backendUrl.trim(),
      clusterName: cfg.clusterName?.trim() || undefined,
      imageTag: cfg.imageTag?.trim() || undefined,
      imageRepo: cfg.imageRepo?.trim() || undefined,
      namespace: cfg.namespace?.trim() || undefined,
      hubbleRelayAddress: cfg.hubbleRelayAddress?.trim() || undefined,
      hubbleRelayTls: tls,
      priorityClassName: cfg.priorityClassName?.trim() || undefined,
      nodeSelector: Object.keys(ns).length > 0 ? ns : undefined,
      resources: hasAnyRes ? res : undefined,
    }
    if (!trimmed.backendUrl) return
    mut.mutate(trimmed)
  }

  return (
    <div className="fixed inset-0 z-[250] flex items-center justify-center bg-black/50 backdrop-blur-sm p-4">
      <div className="bg-kb-card border border-kb-border rounded-xl w-full max-w-xl max-h-[90vh] overflow-hidden flex flex-col">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-kb-border shrink-0">
          <div>
            <h2 className="text-base font-semibold text-kb-text-primary">Install KubeBolt Agent</h2>
            <p className="text-[11px] text-kb-text-tertiary mt-0.5">
              Ships node-level metrics and Hubble flows to the KubeBolt backend.
            </p>
          </div>
          <button
            onClick={onClose}
            className="p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        <form onSubmit={submit} className="flex-1 overflow-y-auto p-5 space-y-5">
          {/* Backend URL */}
          <div>
            <label className="block text-[11px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1.5">
              Backend URL <span className="text-status-error">*</span>
            </label>
            <input
              type="text"
              required
              placeholder="host:port"
              value={cfg.backendUrl}
              onChange={(e) => setCfg({ ...cfg, backendUrl: e.target.value })}
              className="w-full px-3 py-2 rounded-lg bg-kb-elevated border border-kb-border text-sm text-kb-text-primary font-mono placeholder:text-kb-text-tertiary focus:outline-none focus:ring-1 focus:ring-kb-accent"
            />
            <div className="mt-2 flex flex-wrap gap-1.5">
              {backendPresets.map((p) => (
                <button
                  key={p.value}
                  type="button"
                  onClick={() => setCfg({ ...cfg, backendUrl: p.value })}
                  className="text-[10px] px-2 py-1 rounded-md bg-kb-elevated hover:bg-kb-card-hover text-kb-text-secondary border border-kb-border"
                >
                  {p.label}
                </button>
              ))}
            </div>
          </div>

          {/* Cluster name */}
          <div>
            <label className="block text-[11px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1.5">
              Cluster name <span className="text-kb-text-tertiary font-normal normal-case">(optional label)</span>
            </label>
            <input
              type="text"
              placeholder="e.g. prod-eks-us-east-1"
              value={cfg.clusterName ?? ''}
              onChange={(e) => setCfg({ ...cfg, clusterName: e.target.value })}
              className="w-full px-3 py-2 rounded-lg bg-kb-elevated border border-kb-border text-sm text-kb-text-primary font-mono placeholder:text-kb-text-tertiary focus:outline-none focus:ring-1 focus:ring-kb-accent"
            />
            <p className="text-[11px] text-kb-text-tertiary mt-1">
              The canonical cluster ID is auto-discovered from the kube-system namespace UID; this is just a display label.
            </p>
          </div>

          {/* Hubble toggle */}
          <div className="flex items-start justify-between gap-3 p-3 rounded-lg bg-kb-elevated border border-kb-border">
            <div>
              <div className="text-sm text-kb-text-primary font-medium">Hubble flow collector</div>
              <p className="text-[11px] text-kb-text-secondary mt-0.5">
                Streams L4 + L7 HTTP + DNS flows from Cilium. Silent no-op when Cilium isn't installed — safe to leave on.
              </p>
            </div>
            <button
              type="button"
              role="switch"
              aria-checked={cfg.hubbleEnabled}
              onClick={() => setCfg({ ...cfg, hubbleEnabled: !cfg.hubbleEnabled })}
              className={`relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors ${cfg.hubbleEnabled ? 'bg-kb-accent' : 'bg-kb-border'}`}
            >
              <span
                className={`inline-block h-4 w-4 rounded-full bg-white transition-transform ${cfg.hubbleEnabled ? 'translate-x-[18px]' : 'translate-x-0.5'} mt-0.5`}
              />
            </button>
          </div>

          {/* Advanced */}
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
                {/* Image */}
                <section className="space-y-2">
                  <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Image</div>
                  <div>
                    <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Namespace</label>
                    <input
                      type="text" placeholder="kubebolt-system"
                      value={cfg.namespace ?? ''}
                      onChange={(e) => setCfg({ ...cfg, namespace: e.target.value })}
                      className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                    />
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Image repo</label>
                      <input
                        type="text" placeholder="ghcr.io/clm-cloud-solutions/kubebolt/agent"
                        value={cfg.imageRepo ?? ''}
                        onChange={(e) => setCfg({ ...cfg, imageRepo: e.target.value })}
                        className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                      />
                    </div>
                    <div>
                      <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Image tag</label>
                      <input
                        type="text" placeholder="latest"
                        value={cfg.imageTag ?? ''}
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
                      <option value="">auto (Always for :latest, IfNotPresent otherwise)</option>
                      <option value="Always">Always</option>
                      <option value="IfNotPresent">IfNotPresent</option>
                      <option value="Never">Never (local-only image)</option>
                    </select>
                  </div>
                </section>

                {/* Hubble */}
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
                      type="text" placeholder="name of a pre-created Secret in the target namespace"
                      value={cfg.hubbleRelayTls?.existingSecret ?? ''}
                      onChange={(e) => setCfg({
                        ...cfg,
                        hubbleRelayTls: { ...(cfg.hubbleRelayTls ?? { existingSecret: '' }), existingSecret: e.target.value },
                      })}
                      className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                    />
                    <p className="text-[10px] text-kb-text-tertiary mt-1">
                      Expected keys: <code className="font-mono">ca.crt</code> (TLS) + optional <code className="font-mono">tls.crt</code> and <code className="font-mono">tls.key</code> (mTLS). Install fails fast when the Secret doesn't exist.
                    </p>
                  </div>
                  <div>
                    <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Server name (SNI)</label>
                    <input
                      type="text" placeholder="e.g. *.hubble-relay.cilium.io"
                      value={cfg.hubbleRelayTls?.serverName ?? ''}
                      onChange={(e) => setCfg({
                        ...cfg,
                        hubbleRelayTls: { ...(cfg.hubbleRelayTls ?? { existingSecret: '' }), serverName: e.target.value },
                      })}
                      className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                    />
                  </div>
                </section>

                {/* Scheduling */}
                <section className="space-y-2 pt-3 border-t border-kb-border/60">
                  <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Scheduling</div>
                  <div>
                    <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Priority class name</label>
                    <input
                      type="text" placeholder="e.g. system-cluster-critical"
                      value={cfg.priorityClassName ?? ''}
                      onChange={(e) => setCfg({ ...cfg, priorityClassName: e.target.value })}
                      className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                    />
                  </div>
                  <div>
                    <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Node selector</label>
                    <div className="space-y-1.5">
                      {nodeSelector.map((pair, i) => (
                        <KVRow
                          key={i}
                          k={pair.k}
                          v={pair.v}
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

                {/* Resources */}
                <section className="space-y-2 pt-3 border-t border-kb-border/60">
                  <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Resources</div>
                  <p className="text-[10px] text-kb-text-tertiary">
                    Kubernetes quantity strings. Defaults: requests 10m / 30Mi, limits 100m / 80Mi.
                  </p>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">CPU request</label>
                      <input
                        type="text" placeholder="10m"
                        value={cfg.resources?.cpuRequest ?? ''}
                        onChange={(e) => setCfg({ ...cfg, resources: { ...(cfg.resources ?? {}), cpuRequest: e.target.value } })}
                        className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                      />
                    </div>
                    <div>
                      <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">CPU limit</label>
                      <input
                        type="text" placeholder="100m"
                        value={cfg.resources?.cpuLimit ?? ''}
                        onChange={(e) => setCfg({ ...cfg, resources: { ...(cfg.resources ?? {}), cpuLimit: e.target.value } })}
                        className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                      />
                    </div>
                    <div>
                      <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Memory request</label>
                      <input
                        type="text" placeholder="30Mi"
                        value={cfg.resources?.memoryRequest ?? ''}
                        onChange={(e) => setCfg({ ...cfg, resources: { ...(cfg.resources ?? {}), memoryRequest: e.target.value } })}
                        className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                      />
                    </div>
                    <div>
                      <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Memory limit</label>
                      <input
                        type="text" placeholder="80Mi"
                        value={cfg.resources?.memoryLimit ?? ''}
                        onChange={(e) => setCfg({ ...cfg, resources: { ...(cfg.resources ?? {}), memoryLimit: e.target.value } })}
                        className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                      />
                    </div>
                  </div>
                </section>

                {/* Logging */}
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

          {/* Errors */}
          {conflict && (
            <div className="flex items-start gap-2 px-3 py-2.5 rounded-lg bg-status-warn-dim border border-status-warn/30">
              <AlertTriangle className="w-4 h-4 text-status-warn shrink-0 mt-0.5" />
              <div className="text-[11px] text-kb-text-primary">
                <div className="font-semibold mb-0.5">Install conflict</div>
                <div>
                  {conflict.kind}{conflict.namespace ? ` ${conflict.namespace}/` : ' '}{conflict.name}: {conflict.reason}
                </div>
                <div className="mt-1 text-kb-text-secondary">
                  KubeBolt won't overwrite resources it didn't create. Uninstall the existing one first, or edit it in place with <code className="font-mono">helm upgrade</code>.
                </div>
              </div>
            </div>
          )}
          {mut.isError && !conflict && (
            <div className="flex items-start gap-2 px-3 py-2.5 rounded-lg bg-status-error-dim border border-status-error/30">
              <AlertTriangle className="w-4 h-4 text-status-error shrink-0 mt-0.5" />
              <div className="text-[11px] text-status-error">
                {(mut.error as Error).message}
              </div>
            </div>
          )}
        </form>

        {/* Footer */}
        <div className="flex items-center justify-end gap-2 px-5 py-3 border-t border-kb-border shrink-0">
          <button
            onClick={onClose}
            className="px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={submit}
            disabled={!cfg.backendUrl.trim() || mut.isPending}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-accent hover:bg-kb-accent-hover text-kb-on-accent text-xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {mut.isPending ? (
              <>
                <Loader2 className="w-3.5 h-3.5 animate-spin" />
                Installing…
              </>
            ) : mut.isSuccess ? (
              <>
                <Check className="w-3.5 h-3.5" />
                Installed
              </>
            ) : (
              <>Install agent</>
            )}
          </button>
        </div>
      </div>
    </div>
  )
}
