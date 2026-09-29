import type { AgentInstallConfig, AgentInstallDefaults } from '@/services/api'

// Helm command for the first-run wizard's Agent step. Kept pure (no React)
// so the flags the operator copies are unit-tested: the chart marks
// backendUrl as `required`, and it silently ignores unknown keys — a wrong
// flag here fails the install, or worse, installs an agent that never
// authenticates.

export type AgentRBACMode = NonNullable<AgentInstallConfig['rbacMode']>

export const AGENT_CHART = 'oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent'
export const BACKEND_URL_PLACEHOLDER = '<kubebolt-host>:9090'

export interface SetupAgentTarget {
  // host:port the agent dials (the backend's gRPC agent-ingest).
  backendUrl: string
  // Namespace the agent is installed in — and where a token Secret must live.
  namespace: string
  // True when backendUrl came from the backend's topology discovery; false
  // when it is the placeholder the operator still has to fill in.
  inferred: boolean
}

// resolveSetupAgentTarget picks where the agent installs and what it dials.
//
// In-cluster (Helm install): the agent goes next to KubeBolt — same
// namespace — and dials the agent-ingest Service over cluster DNS; no
// exposure, no TLS. The backend already discovers that Service
// (GET /agent/install-defaults → internalBackendUrl).
//
// External (desktop binary / docker-compose): the agent runs in some other
// cluster, so it needs an address that cluster can reach — the discovered
// external endpoint when there is one, otherwise a placeholder.
export function resolveSetupAgentTarget(defaults?: AgentInstallDefaults): SetupAgentTarget {
  if (defaults?.deploymentMode === 'in-cluster' && defaults.internalBackendUrl) {
    return {
      backendUrl: defaults.internalBackendUrl,
      namespace: defaults.selfNamespace || 'kubebolt',
      inferred: true,
    }
  }
  const namespace = defaults?.agentNamespace || 'kubebolt-system'
  if (defaults?.externalEndpoint) {
    return { backendUrl: defaults.externalEndpoint, namespace, inferred: true }
  }
  return { backendUrl: BACKEND_URL_PLACEHOLDER, namespace, inferred: false }
}

export interface SetupAgentHelmOptions {
  target: SetupAgentTarget
  rbacMode: AgentRBACMode
  // Name of the Secret holding the ingest token (key `token`, as the backend
  // materializes it). Omit when the channel does not require auth.
  tokenSecretName?: string
}

export function buildSetupAgentHelmCommand({ target, rbacMode, tokenSecretName }: SetupAgentHelmOptions): string {
  const lines = [
    `helm install kubebolt-agent ${AGENT_CHART}`,
    `  --namespace ${target.namespace} --create-namespace`,
    `  --set backendUrl=${target.backendUrl}`,
    `  --set rbac.mode=${rbacMode}`,
  ]
  if (tokenSecretName) {
    lines.push('  --set auth.mode=ingest-token')
    lines.push(`  --set auth.ingestToken.existingSecret=${tokenSecretName}`)
  }
  return lines.join(' \\\n')
}

// Operator mode is cluster-admin scoped to the agent ServiceAccount; without
// agent auth anything that reaches the ingest could drive it. Same rule as
// agentConfigBlocked() in the Add cluster wizard: refuse to render a command
// for operator mode until a token is in place.
export function setupAgentBlocked(rbacMode: AgentRBACMode, hasToken: boolean): boolean {
  return rbacMode === 'operator' && !hasToken
}
