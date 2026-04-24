import { Lock, ArrowRight, Loader2 } from 'lucide-react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// AgentRequiredPlaceholder is shown where a feature depends on the
// KubeBolt Agent being installed in the target cluster. The
// component polls the integration detection endpoint so it knows
// whether to:
//
//   - hide itself (agent present and healthy — whatever feature
//     follows this placeholder is supposed to work already)
//   - render the full install prompt with a deep link to the
//     Administration → Integrations page
//
// The dashed border + lock icon stays because operators already
// recognize it as "feature locked behind install"; only the
// copy + CTA changes based on state.
interface Props {
  title?: string
  description?: string
  // Suppress the loading shimmer while the agent state is still
  // unknown. Callers that never want to show a skeleton use this.
  hideWhileLoading?: boolean
}

export function AgentRequiredPlaceholder({
  title = 'Requires KubeBolt Agent',
  description = 'Advanced metrics collection for deeper insights',
  hideWhileLoading,
}: Props) {
  const { data: agent, isLoading } = useQuery({
    queryKey: ['integration', 'agent'],
    queryFn: () => api.getIntegration('agent'),
    // Refresh often — if the operator just installed the agent
    // from another tab, the placeholder should disappear within a
    // few seconds rather than stick around until the next reload.
    refetchInterval: 10_000,
    staleTime: 5_000,
  })

  if (isLoading) {
    return hideWhileLoading ? null : <Shell />
  }

  const installed =
    agent && (agent.status === 'installed' || agent.status === 'degraded')

  // Agent is there — the feature this placeholder guards is
  // presumed to be live. Returning null lets the caller's own
  // "no data yet" state (typically the chart's empty message)
  // take over.
  if (installed) return null

  return (
    <div className="border-2 border-dashed border-kb-border rounded-lg p-8 flex flex-col items-center justify-center text-center">
      <Lock className="w-8 h-8 text-kb-text-tertiary mb-3" />
      <h3 className="text-sm font-medium text-kb-text-secondary mb-1">{title}</h3>
      <p className="text-xs text-kb-text-tertiary mb-4 max-w-md">{description}</p>

      <Link
        to="/admin/integrations"
        className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-accent text-white text-xs font-medium hover:bg-kb-accent-bright transition-colors"
      >
        Install agent
        <ArrowRight className="w-3.5 h-3.5" />
      </Link>

      <div className="mt-4 text-[10px] text-kb-text-tertiary">Or install via Helm:</div>
      <div className="mt-1.5 bg-kb-bg rounded-md px-3 py-2 font-mono text-[10px] text-kb-text-secondary border border-kb-border max-w-full overflow-x-auto whitespace-nowrap">
        helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent --namespace kubebolt-system --create-namespace
      </div>
    </div>
  )
}

function Shell() {
  return (
    <div className="border-2 border-dashed border-kb-border rounded-lg p-8 flex items-center justify-center opacity-60">
      <Loader2 className="w-4 h-4 text-kb-text-tertiary animate-spin" />
    </div>
  )
}
