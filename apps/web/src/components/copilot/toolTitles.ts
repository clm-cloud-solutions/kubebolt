import { externalTool } from '@/utils/toolName'
import { azureSubject, fleetTargets, gcpSummary } from './toolPresenters'

// A tool card's title says WHAT is read and its chip WHAT ABOUT:
// "Pods · catalog-api" [tienda], "Logs · catalog-api" [30 min],
// "CloudTrail · LookupEvents" [us-east-2]. The tool's wire name and the
// presenter's long summary stay in the title's tooltip.

export interface ToolTitle {
  title: string
  chip?: string
}

const str = (v: unknown) => (typeof v === 'string' ? v : '')

// Kubernetes kinds by the resource type the tools take.
const KINDS: Record<string, string> = {
  pods: 'Pod', deployments: 'Deployment', statefulsets: 'StatefulSet', daemonsets: 'DaemonSet',
  replicasets: 'ReplicaSet', jobs: 'Job', cronjobs: 'CronJob', services: 'Service', ingresses: 'Ingress',
  configmaps: 'ConfigMap', secrets: 'Secret', nodes: 'Node', namespaces: 'Namespace',
  persistentvolumeclaims: 'PVC', pvcs: 'PVC', persistentvolumes: 'PV', storageclasses: 'StorageClass',
  networkpolicies: 'NetworkPolicy', ciliumnetworkpolicies: 'CiliumNetworkPolicy',
  horizontalpodautoscalers: 'HPA', hpas: 'HPA', poddisruptionbudgets: 'PDB', pdbs: 'PDB',
  serviceaccounts: 'ServiceAccount', roles: 'Role', clusterroles: 'ClusterRole', rolebindings: 'RoleBinding',
  clusterrolebindings: 'ClusterRoleBinding', gateways: 'Gateway', httproutes: 'HTTPRoute', events: 'Event',
  endpoints: 'Endpoints', externalsecrets: 'ExternalSecret', certificates: 'Certificate',
}

export function kindOf(type: string): string {
  const t = type.toLowerCase()
  if (KINDS[t]) return KINDS[t]
  const singular = t.endsWith('ies') ? `${t.slice(0, -3)}y` : t.replace(/s$/, '')
  return singular.charAt(0).toUpperCase() + singular.slice(1)
}

function kindPlural(type: string): string {
  const k = kindOf(type)
  if (/[A-Z]{2,}$/.test(k) || k.endsWith('s')) return k === 'Endpoints' ? k : `${k}s`
  if (k.endsWith('y')) return `${k.slice(0, -1)}ies`
  if (/(ss|sh|ch|x)$/.test(k)) return `${k}es`
  return `${k}s`
}

// Kubernetes writes the hash and the random suffix of a pod's name in an
// alphabet without vowels, so a hash never reads as a word ("exporter").
const NAME_HASH = '[bcdfghjklmnpqrstvwxz2456789]'
const DEPLOYMENT_POD = new RegExp(`-${NAME_HASH}{6,10}-${NAME_HASH}{5}$`)
const OWNED_POD = new RegExp(`-${NAME_HASH}{5}$`)

// workloadOf names a pod by its workload: catalog-api-f958778f8-vg55l →
// catalog-api (a Deployment's pod), node-exporter-x2k4q → node-exporter; a
// StatefulSet's redis-0 stays as it is.
export function workloadOf(pod: string): string {
  if (DEPLOYMENT_POD.test(pod)) return pod.replace(DEPLOYMENT_POD, '')
  return pod.replace(OWNED_POD, '')
}

// "30m" → "30 min", "2h" → "2 h", "15s" → "15 s".
function window(since: string): string {
  const m = /^(\d+)(s|m|h|d)$/.exec(since)
  if (!m) return since
  return `${m[1]} ${{ s: 's', m: 'min', h: 'h', d: 'd' }[m[2] as 's' | 'm' | 'h' | 'd']}`
}

const withName = (lead: string, name: string) => (name ? `${lead} · ${name}` : lead)

// What get_workload_metrics reads, in words: "CPU, memory", "network", "disk".
const METRIC_WORDS: Record<string, string> = {
  cpu: 'CPU', memory: 'memory', network_rx: 'network', network_tx: 'network', filesystem: 'disk',
}

function metricsRead(metrics: unknown): string {
  const list = Array.isArray(metrics) ? metrics.map((m) => METRIC_WORDS[str(m)] ?? str(m)).filter(Boolean) : []
  const read = [...new Set(list)].join(', ')
  return read ? read.charAt(0).toUpperCase() + read.slice(1) : 'Metrics'
}

function builtinTitle(name: string, i: Record<string, unknown>, result?: string): ToolTitle | null {
  const ns = str(i.namespace)
  const n = str(i.name)
  const type = str(i.type)
  switch (name) {
    case 'get_cluster_overview':
      return { title: 'Cluster overview' }
    case 'get_workload_pods':
      return { title: withName('Pods', n), chip: ns }
    case 'get_pod_logs': {
      const since = str(i.since)
      const chip = i.previous === true ? 'previous' : since ? window(since) : str(i.sinceTime) ? 'time range' : ns
      return { title: withName('Logs', workloadOf(n)), chip }
    }
    case 'get_events':
      return { title: withName('Events', str(i.involvedName)), chip: ns || 'all namespaces' }
    case 'get_resource_detail':
      return { title: withName(kindOf(type), n), chip: ns }
    case 'get_resource_describe':
      return { title: `Describe · ${kindOf(type)}`, chip: n }
    case 'get_resource_yaml':
      return { title: `YAML · ${kindOf(type)}`, chip: n }
    case 'list_resources': {
      const status = str(i.status)
      return { title: status ? `${kindPlural(type)} · ${status}` : kindPlural(type), chip: ns || 'all namespaces' }
    }
    case 'get_workload_history':
      return { title: withName('History', n), chip: ns }
    case 'get_cronjob_jobs':
      return { title: withName('Jobs', n), chip: ns }
    case 'get_external_connections': {
      // Once the answer is in, the chip says which workload of the fleet it
      // reaches, as the presenter's summary did.
      const targets = fleetTargets(result)
      const chip = targets.length > 0 ? `→ ${targets[0]}${targets.length > 1 ? ` +${targets.length - 1}` : ''}` : ns
      return { title: withName('External connections', str(i.workload)), chip }
    }
    case 'get_topology':
      return { title: 'Topology' }
    case 'get_insights':
      return { title: 'Insights' }
    case 'get_insight_episodes':
      return { title: 'Insight history' }
    case 'get_insight_episode':
      return { title: 'Insight episode' }
    case 'get_operational_episodes':
      return { title: 'Operational episodes' }
    case 'get_findings':
      return { title: 'Security findings', chip: str(i.resource) || str(i.image) || str(i.severity) }
    case 'get_finding_workloads':
      return { title: 'Findings by workload' }
    case 'get_finding_detail':
      return { title: 'Security finding' }
    case 'get_runtime_events':
      return { title: 'Runtime events' }
    case 'get_coverage':
      return { title: 'Coverage' }
    case 'get_right_sizing':
      return { title: 'Right-sizing', chip: ns }
    case 'query_metrics':
      return { title: 'Metrics', chip: window(str(i.range)) }
    case 'get_workload_metrics': {
      const target = str(i.kind) === 'Pod' ? workloadOf(n) : n
      return { title: withName(metricsRead(i.metrics), target), chip: window(str(i.range) || '15m') }
    }
    case 'get_recent_deploys':
      return { title: 'Recent deploys', chip: ns || 'all namespaces' }
    case 'get_fleet_summary':
      return { title: 'Fleet summary' }
    case 'search_resources':
      return { title: withName('Search', str(i.q)) }
    case 'get_permissions':
      return { title: 'Permissions' }
    case 'list_clusters':
      return { title: 'Clusters' }
    case 'offer_cluster_switch':
      return { title: withName('Cluster switch', str(i.cluster)) }
    case 'get_kubebolt_docs':
      return { title: withName('Docs', str(i.topic)) }
  }
  return null
}

const AWS_SERVICES: Record<string, string> = {
  cloudtrail: 'CloudTrail', ec2: 'EC2', iam: 'IAM', s3: 'S3', kms: 'KMS', rds: 'RDS', elbv2: 'ELB', elb: 'ELB Classic',
  logs: 'CloudWatch Logs', cloudwatch: 'CloudWatch', sts: 'STS', lambda: 'Lambda', sqs: 'SQS', sns: 'SNS', eks: 'EKS',
  route53: 'Route 53', secretsmanager: 'Secrets Manager', ecr: 'ECR', dynamodb: 'DynamoDB', elasticache: 'ElastiCache',
  autoscaling: 'Auto Scaling', ssm: 'Systems Manager', acm: 'ACM', efs: 'EFS', events: 'EventBridge', kafka: 'MSK',
  opensearch: 'OpenSearch', es: 'OpenSearch', apigateway: 'API Gateway', cloudfront: 'CloudFront',
}

const AZURE_NAMESPACES: Record<string, string> = {
  monitor: 'Monitor', aks: 'AKS', postgres: 'PostgreSQL', mysql: 'MySQL', sql: 'SQL', servicebus: 'Service Bus',
  eventhubs: 'Event Hubs', acr: 'Container Registry', resourcehealth: 'Resource Health', keyvault: 'Key Vault',
  storage: 'Storage', advisor: 'Advisor', quota: 'Quota', group: 'Resource groups', subscription: 'Subscriptions',
  redis: 'Redis', cosmos: 'Cosmos DB', appservice: 'App Service', role: 'Access',
}

const GCP_SERVICES: Record<string, string> = {
  alloydb: 'AlloyDB', cloudasset: 'Cloud Asset', clouderrorreporting: 'Error Reporting', cloudquotas: 'Quotas',
  compute: 'Compute Engine', container: 'GKE', databaseinsights: 'Database Insights', logging: 'Cloud Logging',
  memorystore: 'Memorystore', monitoring: 'Cloud Monitoring', pubsub: 'Pub/Sub', recommender: 'Recommender',
  redis: 'Memorystore Redis', sqladmin: 'Cloud SQL',
}

const words = (s: string) => s.replace(/_/g, ' ').trim()

function serviceName(map: Record<string, string>, id: string): string {
  return map[id] ?? (id.length <= 3 ? id.toUpperCase() : id.charAt(0).toUpperCase() + id.slice(1))
}

function cloudTitle(connectorId: string, tool: string, i: Record<string, unknown>): ToolTitle {
  if (connectorId === 'aws' && tool === 'call') {
    const service = str(i.service).toLowerCase()
    const operation = str(i.operation)
    if (service && operation) return { title: `${serviceName(AWS_SERVICES, service)} · ${operation}`, chip: str(i.region) }
  }
  if (connectorId === 'azure') {
    const command = str(i.command)
    if (command) {
      const rest = command.startsWith(`${tool}_`) ? command.slice(tool.length + 1) : command
      return { title: `${serviceName(AZURE_NAMESPACES, tool)} · ${words(rest)}`, chip: azureSubject(i) }
    }
    const [ns, ...rest] = tool.split('_')
    return { title: `${serviceName(AZURE_NAMESPACES, ns)} · ${words(rest.join('_'))}`, chip: azureSubject(i) }
  }
  if (connectorId === 'gcp') {
    const service = Object.keys(GCP_SERVICES)
      .filter((s) => tool.startsWith(`${s}_`))
      .sort((a, b) => b.length - a.length)[0]
    if (service) {
      return { title: `${GCP_SERVICES[service]} · ${words(tool.slice(service.length + 1))}`, chip: gcpSummary(i) }
    }
  }
  return { title: words(tool) }
}

// toolTitle is the card's title and chip for a call; a tool without a title
// of its own reads as its name in words.
export function toolTitle(name: string, input: Record<string, unknown> = {}, result?: string): ToolTitle {
  const ext = externalTool(name)
  const t = ext ? cloudTitle(ext.connectorId, ext.tool, input) : builtinTitle(name, input, result)
  if (!t) return { title: words(name) }
  return t.chip ? t : { title: t.title }
}
