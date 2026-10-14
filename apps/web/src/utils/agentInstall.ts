import type { AgentInstallConfig } from '@/services/api'
import { opencostPromUrlFor, parseOpenCostPrometheus } from '@/utils/opencostPrometheus'

// What the add-cluster wizards hand the operator to paste: the Helm command
// built from the agent config, and — for a first cluster — the whole block
// (namespace, token Secret, Helm) in one copy.

// buildHelmCommand translates the full AgentInstallConfig into `helm install`
// --set flags (chart value paths verified against deploy/helm/kubebolt-agent).
// The optional advanced fields only emit a flag when set, keeping the command
// readable for the common case.
export function buildHelmCommand(
  cfg: AgentInstallConfig,
  nodeSelector: Array<{ k: string; v: string }>,
  opts: { upsert?: boolean } = {},
): string {
  const ns = cfg.namespace?.trim() || 'kubebolt-system'
  // Escape backslashes FIRST, then the metachar — otherwise a backslash already
  // in the input slips through and the escaping is incomplete (CodeQL flags this
  // as js/incomplete-sanitization).
  const escKey = (s: string) => s.replace(/\\/g, '\\\\').replace(/\./g, '\\.') // dots are helm path separators
  const escVal = (s: string) => s.replace(/\\/g, '\\\\').replace(/,/g, '\\,')  // commas separate --set entries
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
  // OpenCost cost integration. 'promread' rides the promRead metrics source
  // (the picker sets metricsSource='promread', so the block above already emits
  // promRead.enabled + url) — here we only flip cost on.
  switch (cfg.opencostMode) {
    case 'bundled': {
      flags.push('opencost.enabled=true')
      // The sub-chart's OWN Prometheus. Omitting it is not a soft default: the
      // official chart points at prometheus-server.prometheus-system and
      // OpenCost dies on boot when that name does not resolve.
      //
      // ONE source for the URL: when the metrics source is promRead, that URL is
      // the one — the dedicated field is not even rendered — otherwise the
      // dedicated field. Never a fallback between them; see opencostPromUrlFor.
      const promUrl = opencostPromUrlFor(cfg.metricsSource, cfg.promRead?.url, cfg.opencostPrometheusUrl)
      const target = parseOpenCostPrometheus(promUrl)
      if (target.kind === 'internal') {
        flags.push(`opencost.opencost.prometheus.internal.serviceName=${escVal(target.serviceName)}`)
        flags.push(`opencost.opencost.prometheus.internal.namespaceName=${escVal(target.namespaceName)}`)
        flags.push(`opencost.opencost.prometheus.internal.port=${target.port}`)
        if (target.scheme !== 'http') flags.push(`opencost.opencost.prometheus.internal.scheme=${target.scheme}`)
        if (target.path) flags.push(`opencost.opencost.prometheus.internal.path=${escVal(target.path)}`)
      } else if (target.kind === 'external') {
        // internal.enabled defaults TRUE in the official chart, so the external
        // branch only takes effect once it is turned off explicitly.
        flags.push('opencost.opencost.prometheus.internal.enabled=false')
        flags.push('opencost.opencost.prometheus.external.enabled=true')
        flags.push(`opencost.opencost.prometheus.external.url=${escVal(target.url)}`)
      } else {
        // Nothing given. A placeholder is the honest rendering — it makes the
        // missing piece visible in the command instead of shipping a default
        // that crash-loops.
        flags.push('opencost.opencost.prometheus.internal.serviceName=<PROMETHEUS_SERVICE>')
        flags.push('opencost.opencost.prometheus.internal.namespaceName=<PROMETHEUS_NAMESPACE>')
      }
      break
    }
    case 'scrape':
      flags.push(`collectors.exporters.opencost=${escVal(cfg.opencostScrapeUrl?.trim() || '<OPENCOST_METRICS_URL>')}`)
      break
    case 'promread':
      flags.push('agent.promRead.cost.enabled=true')
      break
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
    `helm ${opts.upsert ? 'upgrade --install' : 'install'} kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent`,
    `  --namespace ${ns} --create-namespace`,
  ]
  const setLines = flags.map((f) => `  --set ${f}`)
  return [...head, ...setLines].join(' \\\n')
}

// The `<…>` markers a command still carries. A shell reads `<name>` as a
// redirection, so a command with any of them left does not run at all — the
// wizards must not let it be copied (the two orgs that connected in Sept–Oct
// both copied one before generating the token).
export function unresolvedPlaceholders(cmd: string): string[] {
  return [...new Set(cmd.match(/<[A-Za-z_-]+>/g) ?? [])]
}

// The first-cluster block: one paste, safe to repeat. `apply` instead of
// `create` so a second run with a re-generated token replaces the Secret
// instead of failing on AlreadyExists, and `upgrade --install` for the same
// reason on the Helm side.
export function buildFirstClusterScript(cfg: AgentInstallConfig, token: string): string {
  const ns = cfg.namespace?.trim() || 'kubebolt-system'
  const secret = cfg.authTokenSecret?.trim() || 'kubebolt-agent-token'
  return [
    `kubectl create namespace ${ns} --dry-run=client -o yaml | kubectl apply -f -`,
    `kubectl -n ${ns} create secret generic ${secret} \\\n  --from-literal=token=${token} --dry-run=client -o yaml | kubectl apply -f -`,
    buildHelmCommand({ ...cfg, authMode: 'ingest-token', authTokenSecret: secret }, [], { upsert: true }),
  ].join('\n')
}

export interface LocalClusterTool {
  id: 'kind' | 'k3d' | 'minikube'
  label: string
  /** Creates the cluster and points kubectl at it. */
  create: string
  /** What the person needs before running `create`. */
  needs: string
  install: string
}

// A cluster on the laptop for whoever has none at hand. All three run on
// Docker and switch the kubectl context to the new cluster themselves, so the
// install block that follows is the same for every one of them. Each one was
// run end to end with the agent before being listed here.
export const LOCAL_CLUSTER_TOOLS: LocalClusterTool[] = [
  { id: 'kind', label: 'kind', create: 'kind create cluster --name kubebolt-try', needs: 'Docker', install: 'https://kind.sigs.k8s.io/docs/user/quick-start/#installation' },
  { id: 'k3d', label: 'k3d', create: 'k3d cluster create kubebolt-try', needs: 'Docker', install: 'https://k3d.io/#installation' },
  { id: 'minikube', label: 'minikube', create: 'minikube start -p kubebolt-try', needs: 'Docker or a VM driver', install: 'https://minikube.sigs.k8s.io/docs/start/' },
]

// This machine as a cluster on it sees it: Docker Desktop resolves
// host.docker.internal inside kind, k3d and minikube alike.
export const LOCAL_BACKEND_URL = 'host.docker.internal:9090'

// Why an agent could not dial `addr`, or null when it can. A self-hosted
// backend does not know which of its addresses another cluster reaches, so the
// person types it — and the chart takes `host:port`, no scheme.
export function agentAddressProblem(addr: string): string | null {
  const a = addr.trim()
  if (!a) return 'Enter the address the agent should dial.'
  if (/<[^>]*>/.test(a)) return 'Replace the <…> part with a real address.'
  if (a.includes('://')) return 'Leave out the scheme: host:port, for example kubebolt.example.com:9090.'
  const m = a.match(/^(\[[0-9a-fA-F:.]+\]|[^\s:/[\]]+):(\d{1,5})$/)
  if (!m || Number(m[2]) < 1 || Number(m[2]) > 65535) return 'Use host:port, for example kubebolt.example.com:9090.'
  const host = m[1].replace(/^\[|\]$/g, '').toLowerCase()
  if (host === 'localhost' || host === '::1' || host === '0.0.0.0' || /^127\./.test(host)) {
    // Keep the port they typed: a backend on another gRPC port is still this machine.
    return `Inside a cluster, localhost is the agent's own pod. For a cluster on this machine, use host.docker.internal:${m[2]}.`
  }
  return null
}
