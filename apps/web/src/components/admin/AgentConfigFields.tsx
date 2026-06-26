import type { ReactNode } from 'react'
import { AlertTriangle } from 'lucide-react'
import type { AgentAuthInfo, AgentInstallConfig } from '@/services/api'
import { RBACModePicker } from '@/components/admin/RBACModePicker'

// The agent ships samples to the KubeBolt backend over gRPC :9090.
// These are the shapes that typically work — the user picks the one
// matching their deployment topology. Free text is always allowed.
const backendPresets = [
  { label: 'In-cluster backend (Helm release "kubebolt" in namespace "kubebolt")', value: 'kubebolt.kubebolt.svc.cluster.local:9090' },
  { label: 'Backend on host (Docker Desktop)', value: 'host.docker.internal:9090' },
]

// agentConfigBlocked centralizes the two refusal conditions so the wizards'
// footer gating and the inline banner stay in lockstep:
//   (1) operator mode without auth — architectural rule (cluster-admin without
//       auth = pivot), rejected regardless of backend enforcement.
//   (2) backend enforced + proxy on (reader/operator) + auth missing — backend
//       pre-flight 400s it.
export function agentConfigBlocked(cfg: AgentInstallConfig, authInfo?: AgentAuthInfo): boolean {
  const mode = cfg.rbacMode ?? 'reader'
  const proxyOn = mode === 'reader' || mode === 'operator'
  const authMissing = !(cfg.authMode ?? '').trim()
  const operatorBlock = mode === 'operator' && authMissing
  const enforcedBlock = authInfo?.enforcement === 'enforced' && proxyOn && authMissing
  return operatorBlock || enforcedBlock
}

const input = 'w-full px-3 py-2 rounded-lg bg-kb-elevated border border-kb-border text-sm text-kb-text-primary font-mono placeholder:text-kb-text-tertiary focus:outline-none focus:ring-1 focus:ring-kb-accent'
const subInput = 'w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent'

function KVRow({ k, v, onChange, onRemove }: { k: string; v: string; onChange: (k: string, v: string) => void; onRemove: () => void }) {
  return (
    <div className="flex gap-2">
      <input type="text" placeholder="key" value={k} onChange={(e) => onChange(e.target.value, v)} className={subInput} />
      <input type="text" placeholder="value" value={v} onChange={(e) => onChange(k, e.target.value)} className={subInput} />
      <button type="button" onClick={onRemove} className="px-2 py-1 rounded bg-kb-card border border-kb-border text-xs text-kb-text-tertiary hover:text-status-error">×</button>
    </div>
  )
}

interface Props {
  cfg: AgentInstallConfig
  setCfg: (c: AgentInstallConfig) => void
  nodeSelector: Array<{ k: string; v: string }>
  setNodeSelector: (n: Array<{ k: string; v: string }>) => void
  authInfo?: AgentAuthInfo
  advancedOpen: boolean
  setAdvancedOpen: (v: boolean) => void
  // Per-wizard token UI injected inside the ingest-token section (backend
  // materialize-secret vs remote copy-paste differ between the two wizards).
  tokenSlot?: ReactNode
  // Render the transport-TLS toggle (helm copy-paste flow only). Backed by
  // cfg.tlsEnabled. The backend-applied install decides transport itself.
  showTransportTls?: boolean
  // Render the full agent capability surface in Advanced (Prometheus
  // integration, mTLS, ServiceAccount annotations, tolerations, GOMEMLIMIT,
  // extraEnv). Only the helm-command flow (AddClusterWizard) sets this; the
  // backend-applied install wizard leaves it off so it isn't shown knobs its
  // submit path can't carry yet.
  showFullCapabilities?: boolean
  // Extra control rendered right under Backend URL (e.g. owner-team selector).
  topExtra?: ReactNode
}

// AgentConfigFields is the single source of truth for the agent installer's
// configuration surface — shared by AgentInstallWizard (backend applies the
// install) and AddClusterWizard (emits a copy-paste helm command), so the two
// can never drift apart on which knobs they expose.
export function AgentConfigFields({
  cfg, setCfg, nodeSelector, setNodeSelector, authInfo,
  advancedOpen, setAdvancedOpen, tokenSlot, showTransportTls, showFullCapabilities, topExtra,
}: Props) {
  const saAnnotations = cfg.serviceAccountAnnotations ?? []
  const extraEnv = cfg.extraEnv ?? []
  const promReadAuth = cfg.promRead?.authMode ?? 'none'
  return (
    <>
      {/* Backend URL */}
      <div>
        <label className="block text-[11px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1.5">
          Backend URL <span className="text-status-error">*</span>
        </label>
        <input
          type="text" required placeholder="host:port" value={cfg.backendUrl}
          onChange={(e) => setCfg({ ...cfg, backendUrl: e.target.value })}
          className={input}
        />
        <div className="mt-2 flex flex-wrap gap-1.5">
          {backendPresets.map((p) => (
            <button key={p.value} type="button" onClick={() => setCfg({ ...cfg, backendUrl: p.value })}
              className="text-[10px] px-2 py-1 rounded-md bg-kb-elevated hover:bg-kb-card-hover text-kb-text-secondary border border-kb-border">
              {p.label}
            </button>
          ))}
        </div>
      </div>

      {topExtra}

      {/* Cluster name */}
      <div>
        <label className="block text-[11px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1.5">
          Cluster name <span className="text-kb-text-tertiary font-normal normal-case">(optional label)</span>
        </label>
        <input
          type="text" placeholder="e.g. prod-eks-us-east-1" value={cfg.clusterName ?? ''}
          onChange={(e) => setCfg({ ...cfg, clusterName: e.target.value })}
          className={input}
        />
        <p className="text-[11px] text-kb-text-tertiary mt-1">
          The canonical cluster ID is auto-discovered from the kube-system namespace UID; this is just a display label.
        </p>
      </div>

      {/* Auth */}
      <div className="space-y-3 p-3 rounded-lg bg-kb-elevated border border-kb-border">
        <div>
          <div className="text-sm text-kb-text-primary font-medium">Auth to backend</div>
          <p className="text-[11px] text-kb-text-secondary mt-0.5">
            How the agent identifies itself to the backend's gRPC channel. Must match the backend's <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-kb-card">KUBEBOLT_AGENT_AUTH_MODE</code> — under <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-kb-card">enforced</code>/<code className="font-mono text-[10px] px-1 py-0.5 rounded bg-kb-card">permissive</code>, leaving this "Disabled" gets the agent rejected with <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-kb-card">unknown auth mode</code> and a reconnect loop.
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
              type="text" required placeholder="kubebolt-agent-token" value={cfg.authTokenSecret ?? ''}
              onChange={(e) => setCfg({ ...cfg, authTokenSecret: e.target.value })}
              className="w-full px-3 py-2 rounded-lg bg-kb-card border border-kb-border text-sm text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
            />
            {tokenSlot}
          </div>
        )}
        {agentConfigBlocked(cfg, authInfo) && (() => {
          const operatorBlock = (cfg.rbacMode ?? 'reader') === 'operator' && !(cfg.authMode ?? '').trim()
          return (
            <div className="flex items-start gap-2 px-3 py-2.5 rounded-lg bg-status-error-dim border border-status-error/30">
              <AlertTriangle className="w-4 h-4 text-status-error shrink-0 mt-0.5" />
              <div className="text-[11px] text-kb-text-primary">
                <div className="font-semibold">{operatorBlock ? 'Auth required for cluster-wide read+write' : 'Auth required when proxy is on'}</div>
                <div className="text-kb-text-secondary">
                  {operatorBlock
                    ? <>Operator mode grants the agent ServiceAccount cluster-admin power. Without auth, anyone reaching the backend's gRPC port pivots to admin. Pick <strong>Ingest Token</strong> above.</>
                    : <>Backend is in <code className="font-mono">enforced</code> mode. Pick <strong>Ingest Token</strong> above — the agent is rejected at the welcome handshake otherwise.</>}
                </div>
              </div>
            </div>
          )
        })()}
      </div>

      {/* Transport TLS (helm copy-paste flow only) — mTLS certs reveal inline */}
      {showTransportTls && (
        <div className="p-3 rounded-lg bg-kb-elevated border border-kb-border space-y-3">
          <div className="flex items-start justify-between gap-3">
            <div>
              <div className="text-sm text-kb-text-primary font-medium">Transport TLS to backend</div>
              <p className="text-[11px] text-kb-text-secondary mt-0.5">
                Encrypts the agent's gRPC dial. Turn ON when the backend terminates TLS on its agent-ingest port; leave OFF for a plaintext backend (dev / Docker Desktop). Mismatch → <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-kb-card">first record does not look like a TLS handshake</code>.
              </p>
            </div>
            <button
              type="button" role="switch" aria-checked={!!cfg.tlsEnabled}
              onClick={() => setCfg({ ...cfg, tlsEnabled: !cfg.tlsEnabled })}
              className={`relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors ${cfg.tlsEnabled ? 'bg-kb-accent' : 'bg-kb-border'}`}
            >
              <span className={`inline-block h-4 w-4 rounded-full bg-white transition-transform ${cfg.tlsEnabled ? 'translate-x-[18px]' : 'translate-x-0.5'} mt-0.5`} />
            </button>
          </div>
          {showFullCapabilities && cfg.tlsEnabled && (
            <div className="space-y-2 pt-2 border-t border-kb-border/60">
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Mutual TLS (optional)</div>
              <p className="text-[10px] text-kb-text-tertiary">Only when the backend verifies client certs. Secrets must pre-exist in the target namespace.</p>
              <div>
                <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">CA Secret (verify backend)</label>
                <input type="text" placeholder="name of a Secret holding ca.crt" value={cfg.tlsCaSecret ?? ''}
                  onChange={(e) => setCfg({ ...cfg, tlsCaSecret: e.target.value })} className={subInput} />
              </div>
              <div>
                <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Client cert Secret (mutual TLS)</label>
                <input type="text" placeholder="name of a Secret holding tls.crt + tls.key" value={cfg.tlsClientCertSecret ?? ''}
                  onChange={(e) => setCfg({ ...cfg, tlsClientCertSecret: e.target.value })} className={subInput} />
              </div>
            </div>
          )}
        </div>
      )}

      {/* Hubble toggle */}
      <div className="flex items-start justify-between gap-3 p-3 rounded-lg bg-kb-elevated border border-kb-border">
        <div>
          <div className="text-sm text-kb-text-primary font-medium">Hubble flow collector</div>
          <p className="text-[11px] text-kb-text-secondary mt-0.5">
            Streams L4 + L7 HTTP + DNS flows from Cilium. Silent no-op when Cilium isn't installed — safe to leave on.
          </p>
        </div>
        <button
          type="button" role="switch" aria-checked={cfg.hubbleEnabled}
          onClick={() => setCfg({ ...cfg, hubbleEnabled: !cfg.hubbleEnabled })}
          className={`relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors ${cfg.hubbleEnabled ? 'bg-kb-accent' : 'bg-kb-border'}`}
        >
          <span className={`inline-block h-4 w-4 rounded-full bg-white transition-transform ${cfg.hubbleEnabled ? 'translate-x-[18px]' : 'translate-x-0.5'} mt-0.5`} />
        </button>
      </div>

      {/* RBAC mode */}
      <RBACModePicker mode={cfg.rbacMode ?? 'reader'} onChange={(mode) => setCfg({ ...cfg, rbacMode: mode })} />

      {/* Advanced */}
      <div>
        <button
          type="button" onClick={() => setAdvancedOpen(!advancedOpen)}
          className="text-[11px] font-mono text-kb-text-tertiary uppercase tracking-wider hover:text-kb-text-primary"
        >
          {advancedOpen ? '▾' : '▸'} Advanced
        </button>
        {advancedOpen && (
          <div className="mt-3 space-y-4 p-3 rounded-lg bg-kb-elevated border border-kb-border">
            {/* Prometheus integration (full-capability flow only) */}
            {showFullCapabilities && (
              <section className="space-y-2">
                <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Prometheus integration</div>
                <p className="text-[10px] text-kb-text-tertiary">Kubelet/cAdvisor metrics always ship. Optionally add ONE more source (mutually exclusive):</p>
                <div className="space-y-1.5">
                  {([
                    ['kubelet', 'None — kubelet metrics only'],
                    ['scrape', 'Scrape annotated pods (prometheus.io/scrape)'],
                    ['promread', 'Read from existing Prometheus (AMP / GMP / Azure Monitor / self-hosted)'],
                  ] as const).map(([val, label]) => (
                    <label key={val} className="flex items-center gap-2 text-xs text-kb-text-secondary cursor-pointer">
                      <input type="radio" name="metricsSource" checked={(cfg.metricsSource ?? 'kubelet') === val}
                        onChange={() => setCfg({ ...cfg, metricsSource: val })} className="accent-kb-accent" />
                      {label}
                    </label>
                  ))}
                </div>
                {cfg.metricsSource === 'promread' && (
                  <div className="space-y-2 pt-2 pl-3 border-l-2 border-kb-border">
                    <div>
                      <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Prometheus URL <span className="text-status-error">*</span></label>
                      <input type="text" placeholder="https://prometheus.monitoring.svc:9090" value={cfg.promRead?.url ?? ''}
                        onChange={(e) => setCfg({ ...cfg, promRead: { ...(cfg.promRead ?? {}), url: e.target.value } })} className={subInput} />
                    </div>
                    <div>
                      <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Auth</label>
                      <select value={promReadAuth}
                        onChange={(e) => setCfg({ ...cfg, promRead: { ...(cfg.promRead ?? {}), authMode: e.target.value as NonNullable<AgentInstallConfig['promRead']>['authMode'] } })}
                        className={subInput}>
                        <option value="none">None</option>
                        <option value="basicAuth">Basic auth</option>
                        <option value="bearer">Bearer token</option>
                        <option value="awsSigV4">AWS SigV4 — Amazon Managed Prometheus</option>
                        <option value="gcpIam">GCP IAM — Google Managed Prometheus</option>
                        <option value="azureWorkloadIdentity">Azure Workload Identity — Azure Monitor</option>
                      </select>
                    </div>
                    {promReadAuth === 'basicAuth' && (
                      <div>
                        <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Username</label>
                        <input type="text" placeholder="prometheus" value={cfg.promRead?.basicAuthUsername ?? ''}
                          onChange={(e) => setCfg({ ...cfg, promRead: { ...(cfg.promRead ?? {}), basicAuthUsername: e.target.value } })} className={subInput} />
                        <p className="text-[10px] text-kb-text-tertiary mt-1">Supply the password via a Secret + extraEnv valueFrom in production — see docs.</p>
                      </div>
                    )}
                    {promReadAuth === 'bearer' && (
                      <div>
                        <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Bearer token</label>
                        <input type="text" placeholder="prefer a Secret in production — see docs" value={cfg.promRead?.bearerToken ?? ''}
                          onChange={(e) => setCfg({ ...cfg, promRead: { ...(cfg.promRead ?? {}), bearerToken: e.target.value } })} className={subInput} />
                      </div>
                    )}
                    {promReadAuth === 'awsSigV4' && (
                      <div>
                        <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">AWS region <span className="text-status-error">*</span></label>
                        <input type="text" placeholder="us-east-1" value={cfg.promRead?.awsRegion ?? ''}
                          onChange={(e) => setCfg({ ...cfg, promRead: { ...(cfg.promRead ?? {}), awsRegion: e.target.value } })} className={subInput} />
                      </div>
                    )}
                    {(promReadAuth === 'awsSigV4' || promReadAuth === 'gcpIam' || promReadAuth === 'azureWorkloadIdentity') && (
                      <p className="text-[10px] text-status-warn">Uses the pod's cloud identity — set the ServiceAccount annotations below (IRSA / Workload Identity).</p>
                    )}
                    <p className="text-[10px] text-kb-text-tertiary">Adds KSM + load + PSI + disk + network-errs lanes the UI can't get from kubelet. Surgical default matchers — customize via the chart (docs).</p>
                  </div>
                )}
              </section>
            )}

            {/* Image */}
            <section className="space-y-2">
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Image</div>
              <div>
                <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Namespace</label>
                <input type="text" placeholder="kubebolt-system" value={cfg.namespace ?? ''}
                  onChange={(e) => setCfg({ ...cfg, namespace: e.target.value })} className={subInput} />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Image repo</label>
                  <input type="text" placeholder="ghcr.io/clm-cloud-solutions/kubebolt/agent" value={cfg.imageRepo ?? ''}
                    onChange={(e) => setCfg({ ...cfg, imageRepo: e.target.value })} className={subInput} />
                </div>
                <div>
                  <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Image tag</label>
                  <input type="text" placeholder="latest" value={cfg.imageTag ?? ''}
                    onChange={(e) => setCfg({ ...cfg, imageTag: e.target.value })} className={subInput} />
                </div>
              </div>
              <div>
                <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Pull policy</label>
                <select value={cfg.imagePullPolicy ?? ''}
                  onChange={(e) => setCfg({ ...cfg, imagePullPolicy: (e.target.value || undefined) as AgentInstallConfig['imagePullPolicy'] })}
                  className={subInput}>
                  <option value="">auto (Always for :latest, IfNotPresent otherwise)</option>
                  <option value="Always">Always</option>
                  <option value="IfNotPresent">IfNotPresent</option>
                  <option value="Never">Never (local-only image)</option>
                </select>
              </div>
            </section>

            {/* Hubble relay */}
            <section className="space-y-2 pt-3 border-t border-kb-border/60">
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Hubble relay</div>
              <div>
                <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Relay address override</label>
                <input type="text" placeholder="hubble-relay.kube-system.svc.cluster.local:80" value={cfg.hubbleRelayAddress ?? ''}
                  onChange={(e) => setCfg({ ...cfg, hubbleRelayAddress: e.target.value })} className={subInput} />
              </div>
              <div>
                <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">TLS Secret (existing)</label>
                <input type="text" placeholder="name of a pre-created Secret in the target namespace" value={cfg.hubbleRelayTls?.existingSecret ?? ''}
                  onChange={(e) => setCfg({ ...cfg, hubbleRelayTls: { ...(cfg.hubbleRelayTls ?? { existingSecret: '' }), existingSecret: e.target.value } })}
                  className={subInput} />
              </div>
              <div>
                <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Server name (SNI)</label>
                <input type="text" placeholder="e.g. *.hubble-relay.cilium.io" value={cfg.hubbleRelayTls?.serverName ?? ''}
                  onChange={(e) => setCfg({ ...cfg, hubbleRelayTls: { ...(cfg.hubbleRelayTls ?? { existingSecret: '' }), serverName: e.target.value } })}
                  className={subInput} />
              </div>
            </section>

            {/* Scheduling */}
            <section className="space-y-2 pt-3 border-t border-kb-border/60">
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Scheduling</div>
              <div>
                <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Priority class name</label>
                <input type="text" placeholder="e.g. system-cluster-critical" value={cfg.priorityClassName ?? ''}
                  onChange={(e) => setCfg({ ...cfg, priorityClassName: e.target.value })} className={subInput} />
              </div>
              <div>
                <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Node selector</label>
                <div className="space-y-1.5">
                  {nodeSelector.map((pair, i) => (
                    <KVRow key={i} k={pair.k} v={pair.v}
                      onChange={(k, v) => { const next = [...nodeSelector]; next[i] = { k, v }; setNodeSelector(next) }}
                      onRemove={() => setNodeSelector(nodeSelector.filter((_, j) => j !== i))} />
                  ))}
                  <button type="button" onClick={() => setNodeSelector([...nodeSelector, { k: '', v: '' }])}
                    className="text-[10px] font-mono text-kb-accent hover:underline">+ Add selector</button>
                </div>
              </div>
              {showFullCapabilities && (
                <label className="flex items-center gap-2 text-xs text-kb-text-secondary cursor-pointer pt-1">
                  <input type="checkbox" checked={!!cfg.tolerateAll}
                    onChange={() => setCfg({ ...cfg, tolerateAll: !cfg.tolerateAll })} className="accent-kb-accent" />
                  Tolerate all taints (run on every node, incl. control-plane)
                </label>
              )}
            </section>

            {/* ServiceAccount annotations (full-capability flow only) */}
            {showFullCapabilities && (
              <section className="space-y-2 pt-3 border-t border-kb-border/60">
                <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">ServiceAccount annotations</div>
                <p className="text-[10px] text-kb-text-tertiary">IRSA (EKS) / Workload Identity (GKE/AKS) — required when promRead auth uses the cloud's ambient credentials.</p>
                <div className="space-y-1.5">
                  {saAnnotations.map((pair, i) => (
                    <KVRow key={i} k={pair.k} v={pair.v}
                      onChange={(k, v) => { const next = [...saAnnotations]; next[i] = { k, v }; setCfg({ ...cfg, serviceAccountAnnotations: next }) }}
                      onRemove={() => setCfg({ ...cfg, serviceAccountAnnotations: saAnnotations.filter((_, j) => j !== i) })} />
                  ))}
                  <button type="button" onClick={() => setCfg({ ...cfg, serviceAccountAnnotations: [...saAnnotations, { k: '', v: '' }] })}
                    className="text-[10px] font-mono text-kb-accent hover:underline">+ Add annotation</button>
                </div>
              </section>
            )}

            {/* Resources */}
            <section className="space-y-2 pt-3 border-t border-kb-border/60">
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Resources</div>
              <p className="text-[10px] text-kb-text-tertiary">Kubernetes quantity strings. Defaults: requests 10m / 64Mi, limits 100m / 128Mi. Bump the limit (e.g. 256Mi) on busy nodes — Hubble flow parsing is the main driver.</p>
              <div className="grid grid-cols-2 gap-3">
                {([['cpuRequest', 'CPU request', '10m'], ['cpuLimit', 'CPU limit', '100m'], ['memoryRequest', 'Memory request', '64Mi'], ['memoryLimit', 'Memory limit', '128Mi']] as const).map(([key, label, ph]) => (
                  <div key={key}>
                    <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">{label}</label>
                    <input type="text" placeholder={ph} value={cfg.resources?.[key] ?? ''}
                      onChange={(e) => setCfg({ ...cfg, resources: { ...(cfg.resources ?? {}), [key]: e.target.value } })}
                      className={subInput} />
                  </div>
                ))}
              </div>
              {showFullCapabilities && (
                <div>
                  <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">GOMEMLIMIT override</label>
                  <input type="text" placeholder="e.g. 200MiB" value={cfg.gomemlimit ?? ''}
                    onChange={(e) => setCfg({ ...cfg, gomemlimit: e.target.value })} className={subInput} />
                  <p className="text-[10px] text-kb-text-tertiary mt-1">Blank = auto (90% of the memory limit). Format: digits + <span className="font-mono">MiB</span> or <span className="font-mono">GiB</span> (e.g. <span className="font-mono">200MiB</span>).</p>
                </div>
              )}
            </section>

            {/* Logging */}
            <section className="space-y-2 pt-3 border-t border-kb-border/60">
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Logging</div>
              <div>
                <label className="block text-[10px] font-mono text-kb-text-tertiary mb-1">Log level</label>
                <select value={cfg.logLevel ?? 'info'} onChange={(e) => setCfg({ ...cfg, logLevel: e.target.value })} className={subInput}>
                  <option value="debug">debug</option>
                  <option value="info">info</option>
                  <option value="warn">warn</option>
                  <option value="error">error</option>
                </select>
              </div>
            </section>

            {/* Extra env vars (full-capability flow only) */}
            {showFullCapabilities && (
              <section className="space-y-2 pt-3 border-t border-kb-border/60">
                <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Extra env vars</div>
                <p className="text-[10px] text-kb-text-tertiary">Escape hatch for knobs without a first-class field (e.g. KUBEBOLT_AGENT_PPROF_ADDR).</p>
                <div className="space-y-1.5">
                  {extraEnv.map((pair, i) => (
                    <div key={i} className="flex gap-2">
                      <input type="text" placeholder="NAME" value={pair.name}
                        onChange={(e) => { const next = [...extraEnv]; next[i] = { ...next[i], name: e.target.value }; setCfg({ ...cfg, extraEnv: next }) }} className={subInput} />
                      <input type="text" placeholder="value" value={pair.value}
                        onChange={(e) => { const next = [...extraEnv]; next[i] = { ...next[i], value: e.target.value }; setCfg({ ...cfg, extraEnv: next }) }} className={subInput} />
                      <button type="button" onClick={() => setCfg({ ...cfg, extraEnv: extraEnv.filter((_, j) => j !== i) })}
                        className="px-2 py-1 rounded bg-kb-card border border-kb-border text-xs text-kb-text-tertiary hover:text-status-error">×</button>
                    </div>
                  ))}
                  <button type="button" onClick={() => setCfg({ ...cfg, extraEnv: [...extraEnv, { name: '', value: '' }] })}
                    className="text-[10px] font-mono text-kb-accent hover:underline">+ Add env var</button>
                </div>
              </section>
            )}
          </div>
        )}
      </div>
    </>
  )
}
