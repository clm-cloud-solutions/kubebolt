import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { AlertTriangle, Check, Copy, Loader2, KeyRound, ExternalLink } from 'lucide-react'
import { api, type AgentInstallDefaults, type AgentIssueTokenResponse } from '@/services/api'
import { Modal } from '@/components/shared/Modal'
import { Link } from 'react-router-dom'

interface Props {
  onClose: () => void
}

type Mode = 'metrics' | 'reader' | 'operator'

const MODE_BLURB: Record<Mode, string> = {
  metrics: 'Metrics + flows only — no resource browsing.',
  reader: 'Cluster-wide read access via the agent. List/view resources, no mutations.',
  operator: 'Cluster-wide read + write — list, scale, restart, exec, delete. Recommended for full management.',
}

// AddClusterWizard registers a REMOTE cluster (not the current one).
// Unlike AgentInstallWizard which installs the agent into the cluster
// KubeBolt is currently connected to, this flow generates a Helm
// command the operator runs on the OTHER cluster — KubeBolt has no
// kubeconfig there, so server-side install isn't an option.
//
// Two preconditions surface as inline guidance:
//   1. The agent-ingest Service must be reachable from outside (LB,
//      NodePort, or Ingress). If it's ClusterIP only, the wizard shows
//      the patch command up-front.
//   2. The backend must have agent auth enforced + tenants configured.
//      Without that, the agent connects but has no identity → can't
//      auto-register as a cluster.
export function AddClusterWizard({ onClose }: Props) {
  const [clusterName, setClusterName] = useState('')
  const [mode, setMode] = useState<Mode>('operator')
  const [backendUrl, setBackendUrl] = useState('')
  const [issuedToken, setIssuedToken] = useState<AgentIssueTokenResponse | null>(null)
  const [issueError, setIssueError] = useState<string | null>(null)
  const [selectedTenantId, setSelectedTenantId] = useState<string>('')
  const [copied, setCopied] = useState(false)

  const { data: defaults } = useQuery({
    queryKey: ['agent-install-defaults'],
    queryFn: () => api.getAgentInstallDefaults(),
    staleTime: 30_000,
  })

  const { data: authInfo } = useQuery({
    queryKey: ['agent-auth-info'],
    queryFn: () => api.getAgentAuthInfo(),
    staleTime: 30_000,
  })

  // Pre-fill the backend URL with the externally-reachable endpoint as
  // soon as the defaults arrive. Empty when agent-ingest is ClusterIP
  // only — the warning panel below tells the operator how to expose it.
  useEffect(() => {
    if (!defaults || backendUrl) return
    if (defaults.externalEndpoint) setBackendUrl(defaults.externalEndpoint)
  }, [defaults, backendUrl])

  // First active tenant becomes the default selection so the operator
  // can hit Generate Token without a dropdown click in the common case.
  useEffect(() => {
    if (selectedTenantId || !authInfo?.tenants?.length) return
    const firstActive = authInfo.tenants.find((t) => !t.disabled)
    if (firstActive) setSelectedTenantId(firstActive.id)
  }, [authInfo, selectedTenantId])

  const issueToken = useMutation({
    mutationFn: () =>
      api.issueAgentTokenAndMaterializeSecret({
        tenantId: selectedTenantId,
        // The Secret is created in the BACKEND's namespace because the
        // operator will recreate the Secret themselves on the remote
        // cluster from the token printed below. Storing it here gives us
        // a server-side audit trail for the issued token.
        namespace: defaults?.selfNamespace || 'kubebolt',
        secretName: `kubebolt-agent-token-${(clusterName.trim() || 'remote').toLowerCase().replace(/[^a-z0-9-]/g, '-')}`.slice(0, 63),
        label: `add-cluster ${(clusterName.trim() || 'remote')} ${new Date().toISOString().slice(0, 10)}`,
      }),
    onSuccess: (resp) => {
      setIssueError(null)
      setIssuedToken(resp)
    },
    onError: (err) => {
      setIssuedToken(null)
      setIssueError(err instanceof Error ? err.message : String(err))
    },
  })

  const deploymentMode = defaults?.deploymentMode ?? 'in-cluster'
  const isExternalDeployment = deploymentMode === 'external'

  // Endpoint counts as "reachable" only when it isn't a NodePort
  // placeholder AND isn't a loopback (localhost / 127.x / ::1 / 0.0.0.0).
  // Loopback endpoints come from external installs accessed via
  // http://localhost — they're useless to a remote agent.
  const isLoopbackEndpoint = (ep?: string) =>
    !!ep && /^(?:\[?(?:::1?|::ffff:127\.\d+\.\d+\.\d+)\]?|localhost|127\.\d+\.\d+\.\d+|0\.0\.0\.0)(?::|$)/.test(ep)
  const externalReachable = !!(
    defaults?.externalEndpoint &&
    !defaults.externalEndpoint.includes('<NODE_IP>') &&
    !isLoopbackEndpoint(defaults.externalEndpoint)
  )

  const exposeServiceCmd = defaults?.agentIngestService
    ? `kubectl -n ${defaults.agentIngestService.namespace} patch svc ${defaults.agentIngestService.name} -p '{"spec":{"type":"LoadBalancer"}}'`
    : ''

  const helmCommand = buildHelmCommand({
    backendUrl: backendUrl || '<BACKEND_URL>',
    clusterName: clusterName.trim() || '<cluster-name>',
    mode,
    tokenSecretName: issuedToken?.secretName || '<token-secret-name>',
  })

  function copyHelm() {
    // Try the modern Clipboard API first; fall back to the deprecated
    // execCommand path for permission-restricted contexts (some browsers
    // block clipboard writes in cross-origin iframes / non-https). The
    // user always gets feedback either way.
    const done = () => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(helmCommand).then(done).catch(() => copyViaTextarea(helmCommand, done))
      return
    }
    copyViaTextarea(helmCommand, done)
  }

  const enforced = authInfo?.enforcement === 'enforced'
  const hasTenants = (authInfo?.tenants?.length ?? 0) > 0
  const canIssueToken = enforced && hasTenants && selectedTenantId !== ''

  return (
    <Modal title="Add cluster" onClose={onClose} size="xl">
      {/* flex-1 + overflow-y-auto so the content scrolls inside the
          modal's max-h-[85vh] container — without it the warning + form
          + helm command section spills past the viewport on smaller
          screens with no way to reach the bottom. */}
      <div className="flex-1 overflow-y-auto p-5 space-y-5">
        <div className="text-[12px] text-kb-text-secondary leading-relaxed">
          Register a remote Kubernetes cluster by installing the KubeBolt agent in
          it. The agent dials this backend over gRPC and auto-registers the
          cluster — it appears in the switcher within ~30 seconds.
        </div>

        {/* Step 0: reachability warning. Two distinct copies depending
            on deployment mode — the in-cluster path inspects a Service
            and tells the operator to flip it to LoadBalancer; the
            external path has no Service and instead instructs to expose
            the backend's gRPC port at the host level. Same lower-opacity
            (status-warn-dim/15) styling either way. */}
        {!externalReachable && !isExternalDeployment && (
          <div className="rounded-lg border border-status-warn/40 bg-status-warn-dim/15 p-3.5 space-y-2">
            <div className="flex items-center gap-2 text-status-warn text-[12px] font-semibold">
              <AlertTriangle className="w-4 h-4 shrink-0" />
              agent-ingest is not reachable from outside the cluster
            </div>
            <p className="text-[11px] text-kb-text-secondary leading-relaxed">
              The Service is currently <span className="font-mono text-kb-text-primary">{defaults?.agentIngestService?.type ?? 'ClusterIP'}</span>.
              A remote cluster's agent can't reach it. Switch it to <span className="font-mono">LoadBalancer</span> (cloud
              clusters with a load-balancer controller) or expose it through an Ingress.
              Quick patch:
            </p>
            {exposeServiceCmd && (
              <pre className="text-[10px] font-mono bg-kb-elevated border border-kb-border rounded p-2 overflow-x-auto text-kb-text-primary">{exposeServiceCmd}</pre>
            )}
            <p className="text-[10px] text-kb-text-secondary">
              After the patch lands and the LoadBalancer gets an IP, refresh this page — the wizard will pre-fill the backend URL automatically.
            </p>
          </div>
        )}

        {!externalReachable && isExternalDeployment && (
          <div className="rounded-lg border border-status-warn/40 bg-status-warn-dim/15 p-3.5 space-y-2">
            <div className="flex items-center gap-2 text-status-warn text-[12px] font-semibold">
              <AlertTriangle className="w-4 h-4 shrink-0" />
              Backend gRPC port 9090 must be reachable from the remote cluster
            </div>
            <p className="text-[11px] text-kb-text-secondary leading-relaxed">
              KubeBolt is running outside Kubernetes (binary, Homebrew, single
              container, or Compose), so there's no Service to expose. The remote
              agent dials this backend directly over gRPC — make sure port{' '}
              <span className="font-mono text-kb-text-primary">9090</span> is
              reachable from where the agent runs:
            </p>
            <ul className="text-[11px] text-kb-text-secondary leading-relaxed list-disc pl-5 space-y-0.5">
              <li>
                <span className="font-mono">docker run</span>: publish the port with{' '}
                <span className="font-mono text-kb-text-primary">-p 9090:9090</span>.
              </li>
              <li>
                Compose: already published in <span className="font-mono">deploy/docker-compose.yml</span>.
              </li>
              <li>
                Bare binary: open firewall / NAT so the remote cluster can reach{' '}
                <span className="font-mono">this-host:9090</span>.
              </li>
              <li>
                Across the public internet: terminate TLS at a reverse proxy and
                point the <span className="font-mono">Backend URL</span> below at it.
              </li>
            </ul>
            <p className="text-[10px] text-kb-text-secondary">
              Then enter the externally-reachable <span className="font-mono">host:9090</span> below.
            </p>
          </div>
        )}

        {/* Step 1: cluster name + mode */}
        <div className="space-y-3">
          <div>
            <label className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">Cluster name (display only)</label>
            <input
              type="text"
              value={clusterName}
              onChange={(e) => setClusterName(e.target.value)}
              placeholder="e.g. prod-eu-west, staging-1"
              className="mt-1 w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
            />
            <p className="text-[10px] text-kb-text-tertiary mt-1">
              Shown in the cluster switcher. The cluster's identity is the kube-system UID, not this label.
            </p>
          </div>

          <div>
            <label className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">Permission tier</label>
            <div className="mt-1 grid grid-cols-3 gap-2">
              {(['metrics', 'reader', 'operator'] as Mode[]).map((m) => (
                <button
                  key={m}
                  type="button"
                  onClick={() => setMode(m)}
                  className={`p-2.5 rounded border text-left transition-colors ${
                    mode === m
                      ? 'border-kb-accent bg-kb-accent/10 ring-1 ring-kb-accent/40'
                      : 'border-kb-border bg-kb-card hover:border-kb-border-active'
                  }`}
                >
                  <div className="text-[11px] font-mono font-semibold text-kb-text-primary uppercase">{m}</div>
                  <div className="text-[10px] text-kb-text-secondary leading-snug mt-0.5">{MODE_BLURB[m]}</div>
                </button>
              ))}
            </div>
          </div>

          <div>
            <label className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">Backend URL (this cluster's agent-ingest)</label>
            <input
              type="text"
              value={backendUrl}
              onChange={(e) => setBackendUrl(e.target.value)}
              placeholder="hostname-or-ip:9090"
              className="mt-1 w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
            />
            {defaults?.externalEndpoint && (
              <p className="text-[10px] text-kb-text-tertiary mt-1">
                Auto-detected from the Service: <span className="font-mono text-kb-text-secondary">{defaults.externalEndpoint}</span>
              </p>
            )}
          </div>
        </div>

        {/* Step 2: token */}
        <div className="rounded-lg border border-kb-border bg-kb-card/50 p-3.5 space-y-2">
          <div className="flex items-center gap-2 text-[12px] font-semibold text-kb-text-primary">
            <KeyRound className="w-3.5 h-3.5" />
            Issue an ingest token
          </div>

          {!enforced && (
            <p className="text-[11px] text-status-warn">
              Backend agent-auth is "{authInfo?.enforcement || 'disabled'}". Token-based registration requires <span className="font-mono">enforced</span> mode.
              Set <span className="font-mono">agentIngest.authMode=enforced</span> on the chart and reinstall.
            </p>
          )}

          {enforced && !hasTenants && (
            <p className="text-[11px] text-status-warn">
              No tenants configured. Create one under <Link to="/admin/tenants" className="underline text-kb-accent">Administration → Tenants</Link>.
            </p>
          )}

          {enforced && hasTenants && (
            <>
              <div className="flex gap-2 items-center">
                <select
                  value={selectedTenantId}
                  onChange={(e) => setSelectedTenantId(e.target.value)}
                  className="flex-1 px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:ring-1 focus:ring-kb-accent"
                >
                  {(authInfo?.tenants || []).map((t) => (
                    <option key={t.id} value={t.id} disabled={t.disabled}>{t.name}{t.disabled ? ' (disabled)' : ''}</option>
                  ))}
                </select>
                <button
                  type="button"
                  onClick={() => issueToken.mutate()}
                  disabled={!canIssueToken || issueToken.isPending || !!issuedToken}
                  className="px-3 py-1.5 text-[11px] font-medium bg-kb-accent text-white rounded hover:bg-kb-accent/90 disabled:opacity-40 transition-colors flex items-center gap-1.5"
                >
                  {issueToken.isPending ? <Loader2 className="w-3 h-3 animate-spin" /> : <KeyRound className="w-3 h-3" />}
                  {issuedToken ? 'Issued' : 'Generate'}
                </button>
              </div>
              {issueError && <div className="text-[11px] text-status-error">{issueError}</div>}
              {issuedToken && (
                <div className="text-[11px] text-kb-text-secondary">
                  Secret <span className="font-mono text-kb-text-primary">{issuedToken.secretName}</span> created in <span className="font-mono">{issuedToken.namespace}</span> ·
                  prefix <span className="font-mono">{issuedToken.tokenPrefix}…</span>
                  <br />
                  <span className="text-[10px] text-kb-text-tertiary">
                    The plaintext token is in <span className="font-mono">data.token</span> of that Secret.
                    On the remote cluster, recreate it as <span className="font-mono">kubebolt-agent-token</span> in <span className="font-mono">kubebolt-system</span>.
                  </span>
                </div>
              )}
            </>
          )}
        </div>

        {/* Step 3: helm command */}
        <div>
          <div className="flex items-center justify-between mb-1">
            <label className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">Run on the remote cluster</label>
            <button
              type="button"
              onClick={copyHelm}
              className={`flex items-center gap-1 px-2 py-0.5 text-[10px] font-mono border rounded transition-colors ${
                copied
                  ? 'text-status-ok border-status-ok/40 bg-status-ok-dim'
                  : 'text-kb-text-secondary hover:text-kb-text-primary border-kb-border hover:bg-kb-card-hover'
              }`}
            >
              {copied ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
              {copied ? 'Copied!' : 'Copy'}
            </button>
          </div>
          <pre className="text-[10px] font-mono bg-kb-elevated border border-kb-border rounded p-3 overflow-x-auto text-kb-text-primary leading-relaxed">{helmCommand}</pre>
          <p className="text-[10px] text-kb-text-tertiary mt-2">
            Need to recreate the token Secret first? Pull it from this cluster with:{' '}
            <span className="font-mono text-kb-text-secondary">{`kubectl -n ${defaults?.selfNamespace || 'kubebolt'} get secret ${issuedToken?.secretName ?? '<secret>'} -o yaml`}</span>{' '}
            and apply it on the remote cluster after creating the namespace.
          </p>
        </div>

        <div className="flex justify-end gap-2 pt-2 border-t border-kb-border">
          <a
            href="https://github.com/clm-cloud-solutions/kubebolt/blob/main/deploy/helm/kubebolt-agent/README.md"
            target="_blank"
            rel="noopener noreferrer"
            className="flex items-center gap-1 px-3 py-1.5 text-[11px] text-kb-text-secondary hover:text-kb-text-primary border border-kb-border rounded transition-colors"
          >
            <ExternalLink className="w-3 h-3" />
            Agent docs
          </a>
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1.5 text-[11px] font-medium bg-kb-card border border-kb-border rounded text-kb-text-primary hover:bg-kb-card-hover transition-colors"
          >
            Done
          </button>
        </div>
      </div>
    </Modal>
  )
}

interface HelmCmdInput {
  backendUrl: string
  clusterName: string
  mode: Mode
  tokenSecretName: string
}

// copyViaTextarea fakes a clipboard write by inserting a textarea,
// selecting it, and triggering execCommand('copy'). Used when the
// modern Clipboard API is unavailable or rejected by the browser
// (insecure-context, permissions, etc).
function copyViaTextarea(text: string, done: () => void) {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'absolute'
  ta.style.left = '-9999px'
  document.body.appendChild(ta)
  ta.select()
  try {
    document.execCommand('copy')
    done()
  } catch {
    /* swallow — user can manually select */
  } finally {
    document.body.removeChild(ta)
  }
}

function buildHelmCommand(in_: HelmCmdInput): string {
  // Token Secret is referenced via existingSecret so the operator can
  // recreate it on the remote cluster from `kubectl get secret ... -o yaml`
  // without retyping the plaintext token.
  return [
    'helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \\',
    '  --namespace kubebolt-system --create-namespace \\',
    `  --set backendUrl=${in_.backendUrl} \\`,
    `  --set cluster.name=${in_.clusterName} \\`,
    `  --set rbac.mode=${in_.mode} \\`,
    `  --set tls.enabled=true \\`,
    `  --set auth.mode=ingest-token \\`,
    `  --set auth.ingestToken.existingSecret=${in_.tokenSecretName}`,
  ].join('\n')
}
