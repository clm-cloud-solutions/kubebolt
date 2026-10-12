// insightResourcePath — the detail page of the object an insight is about.
//
// The engine writes `resource` as "Kind/namespace/name", or "Kind/name" for a
// cluster-scoped object (Node, Namespace). Routes are the same ones
// ResourceDetailPage resolves (its kindToRoute); only the kinds the insight
// rules emit are listed. Null when the kind has no detail page here, so the
// caller keeps its list link.
const INSIGHT_KIND_ROUTE: Record<string, string> = {
  Pod: 'pods',
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
  Job: 'jobs',
  CronJob: 'cronjobs',
  Service: 'services',
  Node: 'nodes',
  Namespace: 'namespaces',
  PVC: 'pvcs',
  HPA: 'hpas',
  NetworkPolicy: 'networkpolicies',
  PodDisruptionBudget: 'pdbs',
  Certificate: 'certificates',
  Application: 'argocdapps',
}

export function insightResourcePath(resource: string | undefined): string | null {
  if (!resource) return null
  const parts = resource.split('/')
  const route = INSIGHT_KIND_ROUTE[parts[0]]
  if (!route) return null
  if (parts.length === 3 && parts[1] && parts[2]) return `/${route}/${parts[1]}/${parts[2]}`
  if (parts.length === 2 && parts[1]) return `/${route}/_/${parts[1]}`
  return null
}
