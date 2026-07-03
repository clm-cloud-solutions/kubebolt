import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { AlertTriangle, Check, Copy, Eye, EyeOff, Loader2, KeyRound, ExternalLink } from 'lucide-react'
import { api, type AgentInstallConfig, type AgentIssueTokenResponse } from '@/services/api'
import type { ClusterInfo } from '@/types/kubernetes'
import { useAuth } from '@/contexts/AuthContext'
import { Modal } from '@/components/shared/Modal'
import { AgentConfigFields } from '@/components/admin/AgentConfigFields'
import { Link } from 'react-router-dom'

interface Props {
  onClose: () => void
}

// AddClusterWizard registers a REMOTE cluster (not the current one). Unlike
// AgentInstallWizard which installs the agent into the cluster KubeBolt is
// connected to, this flow emits a Helm command the operator runs on the OTHER
// cluster — KubeBolt has no kubeconfig there. The config surface is the SHARED
// AgentConfigFields (same knobs as the install wizard), translated to --set
// flags by buildHelmCommand. The transport-TLS toggle defaults OFF (the common
// dev/plaintext-backend case) and is honored in the command — no more hardcoded
// tls.enabled=true.
export function AddClusterWizard({ onClose }: Props) {
  const [cfg, setCfg] = useState<AgentInstallConfig>({
    backendUrl: '',
    clusterName: '',
    rbacMode: 'reader',
    hubbleEnabled: false,
    authMode: 'ingest-token',
    tlsEnabled: false,
  })
  const [nodeSelector, setNodeSelector] = useState<Array<{ k: string; v: string }>>([])
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [issuedToken, setIssuedToken] = useState<AgentIssueTokenResponse | null>(null)
  const [issueError, setIssueError] = useState<string | null>(null)
  const [selectedTeamId, setSelectedTeamId] = useState<string>('')
  const [copied, setCopied] = useState(false)
  const [showToken, setShowToken] = useState(false)
  const [copiedKey, setCopiedKey] = useState<string | null>(null)
  const [tokenAcked, setTokenAcked] = useState(false)
  const [agentConnected, setAgentConnected] = useState(false)
  const [baselineContexts, setBaselineContexts] = useState<Set<string> | null>(null)
  const [waiting, setWaiting] = useState(false)
  const [connectedCluster, setConnectedCluster] = useState<ClusterInfo | null>(null)
  const [waitTimedOut, setWaitTimedOut] = useState(false)

  // Multi-tenant (Cloud): pick which team OWNS the cluster registered with this
  // token. OSS / single-tenant skips it.
  const { isSignupEnabled: isMultiTenant } = useAuth()
  const { data: teams } = useQuery({
    queryKey: ['admin-teams'],
    queryFn: api.listTeams,
    enabled: isMultiTenant,
    staleTime: 30_000,
  })

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

  // Poll the clusters list once a token is issued so we can detect the new
  // cluster registering (see the wait-for-agent effect below).
  const { data: clustersList } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    enabled: waiting,
    refetchInterval: waiting && !agentConnected && !waitTimedOut ? 5000 : false,
  })

  // Hosted / SaaS: when the operator configured an agent-ingest URL, it is the
  // deployment's authoritative external endpoint — the sole backendUrl default,
  // and the signal that flips the wizard into SaaS-mode (TLS on, auth required).
  const hostedMode = !!defaults?.agentIngestUrl
  // Pre-fill the backend URL once (guarded on empty backendUrl so later user
  // edits stick). Hosted → seed the SaaS-mode defaults together; otherwise fall
  // back to the inferred externally-reachable endpoint.
  useEffect(() => {
    if (!defaults || cfg.backendUrl) return
    if (defaults.agentIngestUrl) {
      setCfg((prev) => ({ ...prev, backendUrl: defaults.agentIngestUrl!, tlsEnabled: true, authMode: 'ingest-token' }))
    } else if (defaults.externalEndpoint) {
      setCfg((prev) => ({ ...prev, backendUrl: defaults.externalEndpoint! }))
    }
  }, [defaults, cfg.backendUrl])

  // Wait-for-agent: there's NO WS event for a NEW cluster registering
  // (cluster:connected only fires for the ACTIVE cluster's connector recovery),
  // so once the operator copies the install command we poll the clusters list
  // and watch for a context that wasn't present when they copied it.
  useEffect(() => {
    if (!waiting || !clustersList) return
    if (!baselineContexts) {
      setBaselineContexts(new Set(clustersList.map((c) => c.context)))
      return
    }
    if (!agentConnected) {
      const fresh = clustersList.find((c) => !baselineContexts.has(c.context))
      if (fresh) {
        setConnectedCluster(fresh)
        setAgentConnected(true)
      }
    }
  }, [waiting, clustersList, baselineContexts, agentConnected])

  // Stop the poll after a generous window so a wizard left open (helm never run,
  // agent never installed) doesn't poll forever. Closing the wizard also stops
  // it (the query unmounts). "Keep waiting" re-arms it.
  useEffect(() => {
    if (!waiting || agentConnected || waitTimedOut) return
    const t = setTimeout(() => setWaitTimedOut(true), 300_000)
    return () => clearTimeout(t)
  }, [waiting, agentConnected, waitTimedOut])


  const issueToken = useMutation({
    mutationFn: () =>
      // Issue-ONLY (no materialize): this is a REMOTE cluster the backend can't
      // reach, so we get the plaintext token back and the operator creates the
      // Secret there via the kubectl the wizard emits. Scoped to the session org
      // server-side — no tenant picker, no cross-org.
      api.issueAgentTokenAndMaterializeSecret({
        ...(isMultiTenant && selectedTeamId ? { teamId: selectedTeamId } : {}),
        label: `add-cluster ${(cfg.clusterName?.trim() || 'remote')} ${new Date().toISOString().slice(0, 10)}`,
      }),
    onSuccess: (resp) => {
      setIssueError(null)
      setIssuedToken(resp)
      // Default the Secret name the operator will create so the helm command and
      // the shared Token-Secret field reference the same name.
      setCfg((prev) => ({ ...prev, authTokenSecret: prev.authTokenSecret?.trim() || 'kubebolt-agent-token' }))
    },
    onError: (err) => {
      setIssuedToken(null)
      setIssueError(err instanceof Error ? err.message : String(err))
    },
  })

  const deploymentMode = defaults?.deploymentMode ?? 'in-cluster'
  const isExternalDeployment = deploymentMode === 'external'

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

  const helmCommand = buildHelmCommand(cfg, nodeSelector)

  function copyHelm() {
    const done = () => {
      setCopied(true)
      setWaiting(true) // copying the command = about to run it → start watching for the agent
      setTimeout(() => setCopied(false), 2000)
    }
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(helmCommand).then(done).catch(() => copyViaTextarea(helmCommand, done))
      return
    }
    copyViaTextarea(helmCommand, done)
  }

  // copyText copies an arbitrary string (the token or the kubectl command) and
  // flags `key` as copied for a 2s check indicator — the REAL value is always
  // copied even while the on-screen token is masked.
  function copyText(text: string, key: string) {
    const done = () => {
      setCopiedKey(key)
      setTimeout(() => setCopiedKey((k) => (k === key ? null : k)), 2000)
    }
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).then(done).catch(() => copyViaTextarea(text, done))
      return
    }
    copyViaTextarea(text, done)
  }

  const enforced = authInfo?.enforcement === 'enforced'
  const hasTenants = (authInfo?.tenants?.length ?? 0) > 0
  const canIssueToken = enforced && hasTenants

  const tokenSecretName = cfg.authTokenSecret?.trim() || 'kubebolt-agent-token'
  const tokenNamespace = cfg.namespace?.trim() || 'kubebolt-system'

  // Ingest-token issuance (owner team + Generate) injected into the shared auth
  // section's tokenSlot. The token is scoped to the caller's OWN org server-side
  // — no tenant picker. Issue-only: we show the plaintext once and emit the
  // kubectl the operator runs in the REMOTE cluster. Warnings replace it when the
  // backend isn't ready.
  const tokenSlot = !enforced ? (
    <p className="text-[11px] text-status-warn pt-1">
      Backend agent-auth is "{authInfo?.enforcement || 'disabled'}". Token-based registration requires <span className="font-mono">enforced</span> mode (<span className="font-mono">agentIngest.authMode=enforced</span> on the chart).
    </p>
  ) : !hasTenants ? (
    <p className="text-[11px] text-status-warn pt-1">
      No org resolved for your session. <Link to="/admin/tenants" className="underline text-kb-accent">Administration → Tenants</Link>.
    </p>
  ) : (
    <div className="pt-2 space-y-2">
      {isMultiTenant && (
        <div className="space-y-1">
          <label className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider">Owner team</label>
          <select
            value={selectedTeamId}
            onChange={(e) => setSelectedTeamId(e.target.value)}
            className="w-full px-2.5 py-1.5 rounded bg-kb-card border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:ring-1 focus:ring-kb-accent"
          >
            <option value="">— Unassigned (assign later) —</option>
            {(teams ?? []).map((t) => (
              <option key={t.id} value={t.id}>{t.name}</option>
            ))}
          </select>
          <p className="text-[10px] text-kb-text-tertiary">Only this team (and org admins) will see the cluster once it connects.</p>
        </div>
      )}
      <button
        type="button"
        onClick={() => issueToken.mutate()}
        disabled={!canIssueToken || issueToken.isPending}
        className="px-3 py-1.5 text-[11px] font-medium bg-kb-accent text-white rounded hover:bg-kb-accent/90 disabled:opacity-40 transition-colors flex items-center gap-1.5"
      >
        {issueToken.isPending ? <Loader2 className="w-3 h-3 animate-spin" /> : <KeyRound className="w-3 h-3" />}
        {issuedToken ? 'Re-generate token' : 'Generate token'}
      </button>
      {issueError && <div className="text-[11px] text-status-error">{issueError}</div>}
      {issuedToken?.token && (() => {
        const masked = '•'.repeat(24)
        const kubectlCmd = `kubectl create namespace ${tokenNamespace}\nkubectl -n ${tokenNamespace} create secret generic ${tokenSecretName} \\\n  --from-literal=token=${issuedToken.token!}`
        return (
          <div className="space-y-1.5 pt-1">
            <div className="text-[11px] text-status-warn font-medium">Copy this token now — it's shown only once.</div>
            <div className="flex items-center gap-2 p-2 rounded bg-kb-card border border-kb-border">
              <code className="flex-1 text-[10px] font-mono text-kb-text-primary break-all">{showToken ? issuedToken.token : masked}</code>
              <button type="button" onClick={() => setShowToken((s) => !s)} className="shrink-0 text-kb-text-tertiary hover:text-kb-text-primary" title={showToken ? 'Hide' : 'Show'}>
                {showToken ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
              </button>
              <button type="button" onClick={() => copyText(issuedToken.token!, 'token')} className="shrink-0 text-kb-text-tertiary hover:text-kb-text-primary" title="Copy token">
                {copiedKey === 'token' ? <Check className="w-3.5 h-3.5 text-kb-accent" /> : <Copy className="w-3.5 h-3.5" />}
              </button>
            </div>
            <div className="text-[10px] text-kb-text-tertiary">Create the Secret in the target cluster — Copy grabs the real token even while masked:</div>
            <div className="relative">
              <pre className="p-2 pr-8 rounded bg-kb-card border border-kb-border text-[10px] font-mono text-kb-text-secondary whitespace-pre-wrap break-all">{showToken ? kubectlCmd : kubectlCmd.replace(issuedToken.token!, masked)}</pre>
              <button type="button" onClick={() => copyText(kubectlCmd, 'kubectl')} className="absolute top-1.5 right-1.5 text-kb-text-tertiary hover:text-kb-text-primary" title="Copy command">
                {copiedKey === 'kubectl' ? <Check className="w-3.5 h-3.5 text-kb-accent" /> : <Copy className="w-3.5 h-3.5" />}
              </button>
            </div>
            <label className="flex items-center gap-2 text-[10px] text-kb-text-secondary cursor-pointer pt-0.5">
              <input type="checkbox" checked={tokenAcked} onChange={(e) => setTokenAcked(e.target.checked)} className="accent-kb-accent" />
              I've copied and saved the token — it can't be shown again.
            </label>
          </div>
        )
      })()}
    </div>
  )

  return (
    <Modal title="Add cluster" onClose={onClose} size="2xl">
      <div className="flex-1 flex flex-col md:flex-row min-h-0">
        {/* Left: configuration (scrolls independently) */}
        <div className="flex-1 overflow-y-auto p-5 space-y-5 min-w-0">
        <div className="text-[12px] text-kb-text-secondary leading-relaxed">
          Register a remote Kubernetes cluster by installing the KubeBolt agent in it. The agent dials this backend over gRPC and auto-registers the cluster — it appears in the switcher within ~30 seconds.
        </div>

        {/* Reachability warnings */}
        {!hostedMode && !externalReachable && !isExternalDeployment && (
          <div className="rounded-lg border border-status-warn/40 bg-status-warn-dim/15 p-3.5 space-y-2">
            <div className="flex items-center gap-2 text-status-warn text-[12px] font-semibold">
              <AlertTriangle className="w-4 h-4 shrink-0" />
              agent-ingest is not reachable from outside the cluster
            </div>
            <p className="text-[11px] text-kb-text-secondary leading-relaxed">
              The Service is currently <span className="font-mono text-kb-text-primary">{defaults?.agentIngestService?.type ?? 'ClusterIP'}</span>. A remote cluster's agent can't reach it. Switch it to <span className="font-mono">LoadBalancer</span> or expose it through an Ingress. Quick patch:
            </p>
            {exposeServiceCmd && (
              <pre className="text-[10px] font-mono bg-kb-elevated border border-kb-border rounded p-2 overflow-x-auto text-kb-text-primary">{exposeServiceCmd}</pre>
            )}
            <p className="text-[10px] text-kb-text-secondary">After the patch lands and the LoadBalancer gets an IP, refresh — the wizard pre-fills the backend URL automatically.</p>
          </div>
        )}

        {!hostedMode && !externalReachable && isExternalDeployment && (
          <div className="rounded-lg border border-status-warn/40 bg-status-warn-dim/15 p-3.5 space-y-2">
            <div className="flex items-center gap-2 text-status-warn text-[12px] font-semibold">
              <AlertTriangle className="w-4 h-4 shrink-0" />
              Backend gRPC port 9090 must be reachable from the remote cluster
            </div>
            <p className="text-[11px] text-kb-text-secondary leading-relaxed">
              KubeBolt runs outside Kubernetes, so there's no Service to expose. The remote agent dials this backend directly over gRPC — make sure port <span className="font-mono text-kb-text-primary">9090</span> is reachable from where the agent runs (publish it with <span className="font-mono">-p 9090:9090</span>, open firewall/NAT, or front it with a reverse proxy). Then enter that <span className="font-mono">host:9090</span> below.
            </p>
          </div>
        )}

        {/* Shared config surface — identical knobs to the Manage-Agents wizard */}
        <AgentConfigFields
          cfg={cfg}
          setCfg={setCfg}
          nodeSelector={nodeSelector}
          setNodeSelector={setNodeSelector}
          authInfo={authInfo}
          advancedOpen={advancedOpen}
          setAdvancedOpen={setAdvancedOpen}
          showTransportTls
          showFullCapabilities
          hostedMode={hostedMode}
          tokenSlot={tokenSlot}
        />
        </div>

        {/* Right: the resulting helm command — always in view */}
        <div className="w-full md:w-[46%] md:max-w-[480px] shrink-0 border-t md:border-t-0 md:border-l border-kb-border flex flex-col bg-kb-bg/30 min-h-0">
          <div className="flex-1 overflow-y-auto p-5 space-y-3 min-w-0">
            <p className="text-[11px] text-kb-text-secondary leading-relaxed">
              The command to run on the remote cluster — it updates live as you change the options on the left.
            </p>
        <div>
          <div className="flex items-center justify-between mb-1">
            <label className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">Run on the remote cluster</label>
            <button
              type="button"
              onClick={copyHelm}
              className={`flex items-center gap-1 px-2 py-0.5 text-[10px] font-mono border rounded transition-colors ${
                copied ? 'text-status-ok border-status-ok/40 bg-status-ok-dim' : 'text-kb-text-secondary hover:text-kb-text-primary border-kb-border hover:bg-kb-card-hover'
              }`}
            >
              {copied ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
              {copied ? 'Copied!' : 'Copy'}
            </button>
          </div>
          <pre className="text-[10px] font-mono bg-kb-elevated border border-kb-border rounded p-3 overflow-x-auto text-kb-text-primary leading-relaxed">{helmCommand}</pre>
          {issuedToken?.token && (
            <p className="text-[10px] text-kb-text-tertiary mt-2">
              This command references the Secret <span className="font-mono text-kb-text-secondary">{tokenSecretName}</span> — create it in the target cluster with the token + kubectl shown in the auth section on the left.
            </p>
          )}
          {waiting && (
            <div className="mt-3 rounded-lg border border-kb-border bg-kb-elevated p-3">
              {agentConnected && connectedCluster ? (
                <div className="space-y-1.5">
                  <div className="text-[11px] text-status-ok flex items-center gap-2">
                    <Check className="w-4 h-4 shrink-0" /> Agent connected — cluster registered.
                  </div>
                  <div className="text-[10px] pl-6 flex flex-wrap gap-x-3 gap-y-0.5">
                    <span className="text-kb-text-primary font-medium">{connectedCluster.displayName || connectedCluster.name}</span>
                    {connectedCluster.clusterId && (
                      <span className="text-kb-text-tertiary">id <span className="font-mono text-kb-text-secondary">{connectedCluster.clusterId.slice(0, 12)}…</span></span>
                    )}
                    {(connectedCluster.agentConnected ?? true)
                      ? <span className="text-status-ok font-medium">● live</span>
                      : <span className="text-kb-text-tertiary">● not live yet</span>}
                    {connectedCluster.source && <span className="text-kb-text-tertiary">via <span className="font-mono text-kb-text-secondary">{connectedCluster.source}</span></span>}
                  </div>
                </div>
              ) : waitTimedOut ? (
                <div className="space-y-1">
                  <div className="text-[11px] text-status-warn flex items-center gap-2">
                    <AlertTriangle className="w-4 h-4 shrink-0" /> No agent registered yet.
                  </div>
                  <div className="text-[10px] text-kb-text-tertiary pl-6">
                    Make sure you created the Secret and ran the command in the target cluster — the cluster appears within ~30s of the agent starting.{' '}
                    <button type="button" onClick={() => setWaitTimedOut(false)} className="text-kb-accent underline">Keep waiting</button>
                  </div>
                </div>
              ) : (
                <div className="text-[11px] text-kb-text-secondary flex items-center gap-2">
                  <Loader2 className="w-4 h-4 shrink-0 animate-spin" /> Waiting for the agent to register — run the command above in the remote cluster…
                </div>
              )}
            </div>
          )}
        </div>
          </div>
          <div className="px-5 py-3 border-t border-kb-border flex justify-end gap-2 shrink-0">
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
      </div>
    </Modal>
  )
}

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

// buildHelmCommand translates the full AgentInstallConfig into `helm install`
// --set flags (chart value paths verified against deploy/helm/kubebolt-agent).
// The optional advanced fields only emit a flag when set, keeping the command
// readable for the common case.
function buildHelmCommand(cfg: AgentInstallConfig, nodeSelector: Array<{ k: string; v: string }>): string {
  const ns = cfg.namespace?.trim() || 'kubebolt-system'
  const escKey = (s: string) => s.replace(/\./g, '\\.') // dots are helm path separators
  const escVal = (s: string) => s.replace(/,/g, '\\,')  // commas separate --set entries
  const flags: string[] = [
    `backendUrl=${cfg.backendUrl.trim() || '<BACKEND_URL>'}`,
    `cluster.name=${cfg.clusterName?.trim() || '<cluster-name>'}`,
    `rbac.mode=${cfg.rbacMode ?? 'reader'}`,
    `tls.enabled=${cfg.tlsEnabled ? 'true' : 'false'}`,
    `hubble.enabled=${cfg.hubbleEnabled ? 'true' : 'false'}`,
  ]
  if ((cfg.authMode ?? '') === 'ingest-token') {
    flags.push('auth.mode=ingest-token')
    flags.push(`auth.ingestToken.existingSecret=${cfg.authTokenSecret?.trim() || '<token-secret-name>'}`)
  } else {
    flags.push('auth.mode=disabled')
  }
  // Metrics source — scrape XOR promread (kubelet is always on).
  if (cfg.metricsSource === 'scrape') {
    flags.push('scrape.enabled=true')
  } else if (cfg.metricsSource === 'promread') {
    flags.push('scrape.enabled=false')
    flags.push('agent.promRead.enabled=true')
    flags.push(`agent.promRead.url=${cfg.promRead?.url?.trim() || '<PROMETHEUS_URL>'}`)
    const pAuth = cfg.promRead?.authMode ?? 'none'
    if (pAuth !== 'none') {
      flags.push(`agent.promRead.auth.mode=${pAuth}`)
      if (pAuth === 'basicAuth' && cfg.promRead?.basicAuthUsername?.trim()) flags.push(`agent.promRead.auth.basicAuthUsername=${escVal(cfg.promRead.basicAuthUsername.trim())}`)
      if (pAuth === 'bearer' && cfg.promRead?.bearerToken?.trim()) flags.push(`agent.promRead.auth.bearerToken=${escVal(cfg.promRead.bearerToken.trim())}`)
      if (pAuth === 'awsSigV4') flags.push(`agent.promRead.auth.awsRegion=${cfg.promRead?.awsRegion?.trim() || '<AWS_REGION>'}`)
    }
  }
  // mTLS — only meaningful when transport TLS is on.
  if (cfg.tlsEnabled && cfg.tlsCaSecret?.trim()) flags.push(`tls.caSecret=${cfg.tlsCaSecret.trim()}`)
  if (cfg.tlsEnabled && cfg.tlsClientCertSecret?.trim()) flags.push(`tls.clientCertSecret=${cfg.tlsClientCertSecret.trim()}`)
  if (cfg.imageRepo?.trim()) flags.push(`image.repository=${cfg.imageRepo.trim()}`)
  if (cfg.imageTag?.trim()) flags.push(`image.tag=${cfg.imageTag.trim()}`)
  if (cfg.imagePullPolicy) flags.push(`image.pullPolicy=${cfg.imagePullPolicy}`)
  if (cfg.priorityClassName?.trim()) flags.push(`priorityClassName=${cfg.priorityClassName.trim()}`)
  if (cfg.tolerateAll) flags.push('tolerations[0].operator=Exists')
  if (cfg.gomemlimit?.trim()) flags.push(`gomemlimit=${cfg.gomemlimit.trim()}`)
  if (cfg.logLevel && cfg.logLevel !== 'info') flags.push(`logLevel=${cfg.logLevel}`)
  const r = cfg.resources
  if (r?.cpuRequest?.trim()) flags.push(`resources.requests.cpu=${r.cpuRequest.trim()}`)
  if (r?.memoryRequest?.trim()) flags.push(`resources.requests.memory=${r.memoryRequest.trim()}`)
  if (r?.cpuLimit?.trim()) flags.push(`resources.limits.cpu=${r.cpuLimit.trim()}`)
  if (r?.memoryLimit?.trim()) flags.push(`resources.limits.memory=${r.memoryLimit.trim()}`)
  if (cfg.hubbleRelayAddress?.trim()) flags.push(`hubble.relay.address=${cfg.hubbleRelayAddress.trim()}`)
  // Relay TLS — previously collected in the UI but never emitted (bug).
  if (cfg.hubbleRelayTls?.existingSecret?.trim()) flags.push(`hubble.relay.tls.existingSecret=${cfg.hubbleRelayTls.existingSecret.trim()}`)
  if (cfg.hubbleRelayTls?.serverName?.trim()) flags.push(`hubble.relay.tls.serverName=${cfg.hubbleRelayTls.serverName.trim()}`)
  // Node selector — fed from the wizard's separate array state. The old
  // cfg.nodeSelector read never received it, so selectors were silently dropped.
  for (const { k, v } of nodeSelector) {
    if (k.trim()) flags.push(`nodeSelector.${escKey(k.trim())}=${escVal(v.trim())}`)
  }
  // ServiceAccount annotations (IRSA / Workload Identity).
  for (const { k, v } of cfg.serviceAccountAnnotations ?? []) {
    if (k.trim()) flags.push(`serviceAccount.annotations.${escKey(k.trim())}=${escVal(v.trim())}`)
  }
  // Extra env vars — re-indexed contiguously so a blank row doesn't leave a gap.
  ;(cfg.extraEnv ?? []).filter((e) => e.name.trim()).forEach((e, i) => {
    flags.push(`extraEnv[${i}].name=${e.name.trim()}`)
    flags.push(`extraEnv[${i}].value=${escVal(e.value.trim())}`)
  })

  const head = [
    'helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent',
    `  --namespace ${ns} --create-namespace`,
  ]
  const setLines = flags.map((f) => `  --set ${f}`)
  return [...head, ...setLines].join(' \\\n')
}
