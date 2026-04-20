import type { ClusterInfo } from '@/types/kubernetes'

/**
 * Extracts a human-readable cluster name from ClusterInfo.
 * Priority: user-defined displayName > cloud provider parsing > context/name.
 * Parses cloud provider ARNs/identifiers:
 * - EKS: arn:aws:eks:us-east-1:123456789:cluster/my-cluster → "my-cluster"
 * - GKE: gke_project_zone_cluster-name → "cluster-name"
 * - AKS/others: returns context if short, otherwise name
 */
export function parseClusterDisplayName(cluster: ClusterInfo): string {
  // User-defined display name always wins
  if (cluster.displayName) return cluster.displayName
  for (const val of [cluster.context, cluster.name]) {
    const arnMatch = val.match(/^arn:aws:eks:[^:]+:[^:]+:cluster\/(.+)$/)
    if (arnMatch) return arnMatch[1]
  }
  const gkeMatch = cluster.context.match(/^gke_[^_]+_[^_]+_(.+)$/)
  if (gkeMatch) return gkeMatch[1]
  if (cluster.context.length < 50) return cluster.context
  if (cluster.name.length < 50) return cluster.name
  return cluster.context
}
