import type { ClusterOverview, Insight } from '@/types/kubernetes'

/**
 * Generates contextual chat suggestions based on the actual cluster state.
 * Rules-based, no LLM call — uses overview + insights data already available
 * via existing react-query queries.
 */
export function generateCopilotSuggestions(
  overview?: ClusterOverview,
  insights?: Insight[],
): string[] {
  const out: string[] = []

  if (insights && insights.length > 0) {
    const critical = insights.filter((i) => i.severity === 'critical')
    const warnings = insights.filter((i) => i.severity === 'warning')

    if (critical.length === 1) {
      out.push(`Why is there a critical issue in ${formatResource(critical[0])}?`)
    } else if (critical.length > 1) {
      out.push(`Investigate the ${critical.length} critical issues in the cluster`)
    }

    if (warnings.length === 1 && out.length < 3) {
      out.push(`What does the warning on ${formatResource(warnings[0])} mean?`)
    } else if (warnings.length > 1 && out.length < 3) {
      out.push(`Analyze the ${warnings.length} active warnings`)
    }
  }

  if (overview?.pods?.notReady && overview.pods.notReady > 0 && out.length < 3) {
    if (overview.pods.notReady === 1) {
      out.push(`Why is 1 pod not ready in the cluster?`)
    } else {
      out.push(`Why are ${overview.pods.notReady} pods not ready?`)
    }
  }

  if (overview?.deployments?.notReady && overview.deployments.notReady > 0 && out.length < 3) {
    out.push(`Which deployments have unhealthy replicas?`)
  }

  if (overview?.cpu?.percentUsed && overview.cpu.percentUsed > 70 && out.length < 3) {
    out.push(`Cluster CPU is at ${Math.round(overview.cpu.percentUsed)}% — which pods use the most?`)
  }

  if (overview?.memory?.percentUsed && overview.memory.percentUsed > 70 && out.length < 3) {
    out.push(`Memory is at ${Math.round(overview.memory.percentUsed)}% — which pods use the most?`)
  }

  // Fill the rest with generic suggestions
  const generic = [
    'What is the overall status of my cluster?',
    'Is there anything that needs my attention?',
    'Show me a summary of resource usage',
    'List the workloads with the highest metrics',
  ]

  for (const g of generic) {
    if (out.length >= 3) break
    if (!out.includes(g)) out.push(g)
  }

  return out.slice(0, 3)
}

function formatResource(insight: Insight): string {
  if (insight.namespace && insight.resource) {
    return `${insight.namespace}/${insight.resource}`
  }
  return insight.resource || insight.title || 'the affected resource'
}
