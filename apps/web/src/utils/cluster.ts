import type { ClusterInfo } from '@/types/kubernetes'

/**
 * Extracts a human-readable cluster name from ClusterInfo.
 * Priority: user-defined displayName > cloud provider parsing > context/name.
 * Parses cloud provider ARNs/identifiers:
 * - EKS: arn:aws:eks:us-east-1:123456789:cluster/my-cluster → "my-cluster"
 * - GKE: gke_project_zone_cluster-name → "cluster-name"
 * - AKS/others: returns context if short, otherwise name
 *
 * Agent-connected clusters get a " (via agent)" marker appended as a UI
 * DECORATION (source === 'agent-proxy'), not baked into the stored name.
 * Any legacy stored suffix is stripped first so it is never doubled.
 */
export function parseClusterDisplayName(cluster: ClusterInfo): string {
  const suffix = cluster.source === 'agent-proxy' ? ' (via agent)' : ''
  const strip = (s: string) => s.replace(/ \(via agent\)$/, '')

  // User-defined display name always wins.
  if (cluster.displayName) return strip(cluster.displayName) + suffix
  for (const val of [cluster.context, cluster.name]) {
    const arnMatch = val.match(/^arn:aws:eks:[^:]+:[^:]+:cluster\/(.+)$/)
    if (arnMatch) return arnMatch[1] + suffix
  }
  const gkeMatch = cluster.context.match(/^gke_[^_]+_[^_]+_(.+)$/)
  if (gkeMatch) return gkeMatch[1] + suffix
  const base =
    cluster.context.length < 50 ? cluster.context : cluster.name.length < 50 ? cluster.name : cluster.context
  return strip(base) + suffix
}

/**
 * isOpaqueClusterLabel — ¿lo que vamos a pintar sigue siendo un identificador?
 *
 * El contexto de un cluster por agente es `agent:<uid>`. Como clave está bien;
 * como etiqueta no dice nada, y en una fila parece un fallo de la aplicación.
 * Cuando el cluster trae nombre propio (`displayName`, del label
 * `kubebolt.io/cluster-name` del Hello) nunca llegamos aquí; esto cubre al que
 * se registró sin él.
 *
 * Un contexto normal de kubeconfig —`kind-kubebolt-lab`, `docker-desktop`— SÍ
 * es un nombre, así que no se toca: el que decide no es el origen del cluster,
 * es si la cadena se puede leer.
 */
export function isOpaqueClusterLabel(label: string): boolean {
  // parseClusterDisplayName marks a cluster reached through an agent, so the
  // string arrives as `agent:<uid> (via agent)`. The mark is not the name.
  const bare = label.trim().replace(/ \(via agent\)$/, '')
  return /^agent:[0-9a-f-]{8,}$/i.test(bare)
}
