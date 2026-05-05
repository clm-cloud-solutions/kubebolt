import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Check, Loader2, KeyRound } from 'lucide-react'
import { api, type AgentInstallConfig, type AgentIssueTokenResponse, type Integration } from '@/services/api'
import { Modal } from '@/components/shared/Modal'
import { RBACModePicker } from '@/components/admin/RBACModePicker'

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
    // Default to the typical SaaS-style install — cluster-wide
    // read via the agent's tunnel. Operators with a more
    // restrictive posture switch to "metrics"; operators who want
    // full UI control switch to "operator" + set up auth.
    rbacMode: 'reader',
  })
  // NodeSelector as ordered pairs so users can add empty rows and
  // fill them in; converted to a map on submit.
  const [nodeSelector, setNodeSelector] = useState<Array<{ k: string; v: string }>>([])
  const [advancedOpen, setAdvancedOpen] = useState(false)

  // Backend agent-auth posture. Drives the Save button gating and
  // the tenant dropdown for the Generate Token flow.
  const { data: authInfo } = useQuery({
    queryKey: ['agent-auth-info'],
    queryFn: () => api.getAgentAuthInfo(),
    staleTime: 30_000,
  })

  // Topology defaults. When KubeBolt is in-cluster we pre-fill backendUrl
  // with the internal Service DNS and namespace with kubebolt-system —
  // the operator opens the wizard and most fields are already correct.
  // External (desktop / docker-compose) → leave fields empty so the user
  // picks a preset themselves.
  const { data: defaults } = useQuery({
    queryKey: ['agent-install-defaults'],
    queryFn: () => api.getAgentInstallDefaults(),
    staleTime: 30_000,
  })

  useEffect(() => {
    if (!defaults) return
    setCfg((prev) => ({
      ...prev,
      backendUrl: prev.backendUrl || defaults.internalBackendUrl || '',
      namespace: prev.namespace || defaults.agentNamespace,
    }))
  }, [defaults])

  const [issuedToken, setIssuedToken] = useState<AgentIssueTokenResponse | null>(null)
  const [issueError, setIssueError] = useState<string | null>(null)
  const [selectedTenantId, setSelectedTenantId] = useState<string>('')

  useEffect(() => {
    if (selectedTenantId || !authInfo?.tenants?.length) return
    const firstActive = authInfo.tenants.find((t) => !t.disabled)
    if (firstActive) setSelectedTenantId(firstActive.id)
  }, [authInfo, selectedTenantId])

  // Server-side conflicts come back as HTTP 409 with the conflicting
  // resource broken out. We render a tailored hint for that case.
  const [conflict, setConflict] = useState<{
    kind: string
    namespace?: string
    name: string
    reason: string
  } | null>(null)

  const issueToken = useMutation({
    mutationFn: () =>
      api.issueAgentTokenAndMaterializeSecret({
        tenantId: selectedTenantId,
        namespace: cfg.namespace?.trim() || 'kubebolt-system',
        secretName: cfg.authTokenSecret?.trim() || 'kubebolt-agent-token',
        label: `agent-install ${new Date().toISOString().slice(0, 10)}`,
      }),
    onSuccess: (resp) => {
      setIssueError(null)
      setIssuedToken(resp)
      setCfg((prev) => ({ ...prev, authTokenSecret: resp.secretName }))
    },
    onError: (err) => {
      setIssuedToken(null)
      setIssueError(err instanceof Error ? err.message : String(err))
    },
  })

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
    <Modal badge="Install" title="KubeBolt Agent" onClose={onClose} size="xl">
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

          {/* Auth — must match the backend's KUBEBOLT_AGENT_AUTH_MODE.
              Mismatch → agent gets `unknown auth mode` from Welcome
              and reconnect-loops forever. Wizard surfaces this to
              avoid the day-zero footgun. */}
          <div className="space-y-3 p-3 rounded-lg bg-kb-elevated border border-kb-border">
            <div>
              <div className="text-sm text-kb-text-primary font-medium">Auth to backend</div>
              <p className="text-[11px] text-kb-text-secondary mt-0.5">
                How the agent identifies itself when talking to the backend's gRPC channel. Must match the backend's <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-kb-card">KUBEBOLT_AGENT_AUTH_MODE</code> — when the backend runs <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-kb-card">enforced</code> or <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-kb-card">permissive</code>, leaving this on "Disabled" gets the agent rejected with <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-kb-card">unknown auth mode</code> and a reconnect loop.
              </p>
            </div>
            <select
              value={cfg.authMode ?? ''}
              onChange={(e) => setCfg({ ...cfg, authMode: e.target.value as AgentInstallConfig['authMode'], authTokenSecret: e.target.value === 'ingest-token' ? cfg.authTokenSecret : '' })}
              className="w-full px-3 py-2 rounded-lg bg-kb-card border border-kb-border text-sm text-kb-text-primary focus:outline-none focus:ring-1 focus:ring-kb-accent"
            >
              <option value="">Disabled — backend accepts unauthenticated agents</option>
              <option value="ingest-token">Ingest Token — long-lived bearer (typical for SaaS)</option>
              <option value="tokenreview" disabled>TokenReview — projected SA token (in-cluster only; not yet wizard-supported)</option>
            </select>
            {cfg.authMode === 'ingest-token' && (
              <div className="space-y-2 pt-2 border-t border-kb-border">
                <label className="text-xs font-mono text-kb-text-secondary uppercase tracking-wider">
                  Token Secret <span className="text-status-error">*</span>
                </label>
                <input
                  type="text"
                  required
                  placeholder="kubebolt-agent-token"
                  value={cfg.authTokenSecret ?? ''}
                  onChange={(e) => setCfg({ ...cfg, authTokenSecret: e.target.value })}
                  className="w-full px-3 py-2 rounded-lg bg-kb-card border border-kb-border text-sm text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                />

                {(authInfo?.tenants?.length ?? 0) > 0 && (
                  <div className="pt-2 space-y-2">
                    {(authInfo?.tenants?.length ?? 0) > 1 && (
                      <select
                        value={selectedTenantId}
                        onChange={(e) => setSelectedTenantId(e.target.value)}
                        className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                      >
                        {authInfo!.tenants.map((t) => (
                          <option key={t.id} value={t.id} disabled={t.disabled}>
                            {t.name}{t.disabled ? ' (disabled)' : ''}
                          </option>
                        ))}
                      </select>
                    )}
                    <button
                      type="button"
                      onClick={() => issueToken.mutate()}
                      disabled={!selectedTenantId || issueToken.isPending}
                      className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-card hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      {issueToken.isPending ? (
                        <><Loader2 className="w-3.5 h-3.5 animate-spin" /> Issuing…</>
                      ) : (
                        <><KeyRound className="w-3.5 h-3.5" /> Generate token + create Secret</>
                      )}
                    </button>
                    {issuedToken && (
                      <div className="flex items-start gap-2 px-2.5 py-2 rounded bg-status-ok-dim border border-status-ok/30">
                        <Check className="w-3.5 h-3.5 text-status-ok shrink-0 mt-0.5" />
                        <div className="text-[11px] text-kb-text-primary">
                          <div className="font-semibold">Secret <code className="font-mono">{issuedToken.secretName}</code> ready in <code className="font-mono">{issuedToken.namespace}</code></div>
                          <div className="text-kb-text-secondary mt-0.5">
                            Token <code className="font-mono">{issuedToken.tokenPrefix}…</code> stored under key <code className="font-mono">token</code>. The wizard will reference this Secret on Install.
                          </div>
                        </div>
                      </div>
                    )}
                    {issueError && (
                      <div className="flex items-start gap-2 px-2.5 py-2 rounded bg-status-error-dim border border-status-error/30">
                        <AlertTriangle className="w-3.5 h-3.5 text-status-error shrink-0 mt-0.5" />
                        <div className="text-[11px] text-status-error">{issueError}</div>
                      </div>
                    )}
                  </div>
                )}

                <p className="text-[11px] text-kb-text-tertiary">
                  Use the button above to generate a token + create the Secret in one click, or point at a Secret you've already created.
                </p>
              </div>
            )}
            {/* Refusal banner — Install button is disabled while
                this shows. Two trigger conditions:
                  (1) operator mode + auth missing — hard-coded
                      requirement; the backend rejects this combo
                      regardless of its enforcement setting.
                  (2) backend enforced + proxy on (i.e. mode in
                      reader/operator) + auth missing — backend
                      pre-flight will 400 it. */}
            {(() => {
              const mode = cfg.rbacMode ?? 'reader'
              const proxyOn = mode === 'reader' || mode === 'operator'
              const authMissing = !(cfg.authMode ?? '').trim()
              const operatorBlock = mode === 'operator' && authMissing
              const enforcedBlock = authInfo?.enforcement === 'enforced' && proxyOn && authMissing
              if (!operatorBlock && !enforcedBlock) return null
              return (
                <div className="flex items-start gap-2 px-3 py-2.5 rounded-lg bg-status-error-dim border border-status-error/30">
                  <AlertTriangle className="w-4 h-4 text-status-error shrink-0 mt-0.5" />
                  <div className="text-[11px] text-kb-text-primary">
                    <div className="font-semibold">{operatorBlock ? 'Auth required for cluster-wide read+write' : 'Auth required when proxy is on'}</div>
                    <div className="text-kb-text-secondary">
                      {operatorBlock ? (
                        <>Operator mode grants the agent ServiceAccount cluster-admin power. Without auth, anyone reaching the backend's gRPC port pivots to admin in this cluster. Pick <strong>Ingest Token</strong> above and use Generate to create the Secret.</>
                      ) : (
                        <>Backend is in <code className="font-mono">enforced</code> mode. Pick <strong>Ingest Token</strong> above and use Generate to create the Secret — the agent will be rejected at the welcome handshake otherwise.</>
                      )}
                    </div>
                  </div>
                </div>
              )
            })()}
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

          {/* RBAC mode picker — replaces the binary proxy + operator
              toggles with an explicit 3-tier choice that matches the
              helm chart's rbac.mode value and the OSS manifests. */}
          <RBACModePicker
            mode={cfg.rbacMode ?? 'reader'}
            onChange={(mode) => setCfg({ ...cfg, rbacMode: mode })}
          />


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
            type="button"
            onClick={onClose}
            className="px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors"
          >
            Cancel
          </button>
          {(() => {
            // Combined gating: backend pre-flight (proxy on +
            // enforced auth + empty mode) AND the architectural rule
            // that operator mode REQUIRES auth regardless of backend
            // enforcement (cluster-admin without auth = pivot).
            const mode = cfg.rbacMode ?? 'reader'
            const proxyOn = mode === 'reader' || mode === 'operator'
            const authMissing = !(cfg.authMode ?? '').trim()
            const enforcedBlock = authInfo?.enforcement === 'enforced' && proxyOn && authMissing
            const operatorBlock = mode === 'operator' && authMissing
            const blocked = enforcedBlock || operatorBlock
            const tooltip = operatorBlock
              ? 'Operator mode requires auth — pick Ingest Token (cluster-admin without auth = pivot risk)'
              : enforcedBlock
                ? 'Backend is in enforced auth mode — pick Ingest Token before installing'
                : undefined
            return (
              <button
                type="button"
                onClick={submit}
                disabled={!cfg.backendUrl.trim() || mut.isPending || blocked}
                title={tooltip}
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
            )
          })()}
        </div>
    </Modal>
  )
}
