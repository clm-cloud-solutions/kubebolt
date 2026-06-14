import {
  Box,
  Server,
  Layers,
  Database,
  BarChart3,
  Timer,
  Clock,
  Globe,
  ArrowRightLeft,
  Radio,
  HardDrive,
  Disc,
  FolderClosed,
  FileText,
  Lock,
  Scale,
  FolderOpen,
  Shield,
  ShieldCheck,
  ShieldOff,
  KeyRound,
  Puzzle,
  SlidersHorizontal,
  Package,
  Boxes,
  Lightbulb,
  Activity,
  UserCog,
  type LucideIcon,
} from 'lucide-react'

// resourceTypeIconMap maps a resource type string to its Lucide icon. Mirrors
// the Sidebar nav icons so the page-title icon matches the nav item the
// operator clicked. Approved 2026-05-29 to apply the icon+tinted-title
// pattern across all resource pages (originally only on Applications).
const resourceTypeIconMap: Record<string, LucideIcon> = {
  pods: Box,
  nodes: Server,
  deployments: Layers,
  statefulsets: Database,
  daemonsets: BarChart3,
  jobs: Timer,
  cronjobs: Clock,
  services: Globe,
  ingresses: ArrowRightLeft,
  networkpolicies: Shield,
  pdbs: ShieldOff,
  gateways: Globe,
  httproutes: ArrowRightLeft,
  endpoints: Radio,
  endpointslices: Radio,
  pvcs: HardDrive,
  pvs: Disc,
  storageclasses: FolderClosed,
  configmaps: FileText,
  secrets: Lock,
  serviceaccounts: UserCog,
  hpas: Scale,
  namespaces: FolderOpen,
  certificates: KeyRound,
  argocdapps: Puzzle,
  vpas: SlidersHorizontal,
  ciliumnetworkpolicies: ShieldCheck,
  ciliumclusterwidenetworkpolicies: ShieldCheck,
  applications: Package,
  insights: Lightbulb,
  clusters: Server,
  rbac: Shield,
  events: Activity,
}

// resourceTypeDescriptionMap is the one-line subtitle shown under each page
// title (homologated across all pages 2026-05-29). Terse, plain language —
// no dev-speak (per the UI copy conventions).
const resourceTypeDescriptionMap: Record<string, string> = {
  pods: 'Running containers, grouped into pods.',
  nodes: 'Cluster machines and their capacity.',
  deployments: 'Declarative rollouts and scaling for stateless workloads.',
  statefulsets: 'Stable identity and storage for stateful workloads.',
  daemonsets: 'One pod per node — agents and node-level services.',
  jobs: 'Run-to-completion batch tasks.',
  cronjobs: 'Scheduled jobs on a recurring timetable.',
  services: 'Stable network endpoints for groups of pods.',
  ingresses: 'HTTP and HTTPS routing into the cluster.',
  networkpolicies: 'Pod-to-pod traffic rules.',
  pdbs: 'Disruption budgets that protect availability during drains.',
  gateways: 'Gateway API entry points into the cluster.',
  httproutes: 'Gateway API HTTP routing rules.',
  endpoints: 'Backend addresses behind services.',
  endpointslices: 'Backend addresses behind services.',
  pvcs: 'Storage claims requested by workloads.',
  pvs: 'Cluster storage volumes.',
  storageclasses: 'Storage provisioners and their parameters.',
  configmaps: 'Non-secret configuration data.',
  secrets: 'Sensitive configuration — values are redacted.',
  serviceaccounts: 'Pod identities for cluster API access.',
  hpas: 'Horizontal autoscaling rules and current scale.',
  namespaces: 'Logical partitions of the cluster.',
  certificates: 'cert-manager TLS certificates and renewal status.',
  argocdapps: 'ArgoCD applications and their sync/health status.',
  vpas: 'Vertical autoscaling recommendations.',
  ciliumnetworkpolicies: 'Cilium L3-L7 traffic rules (namespaced).',
  ciliumclusterwidenetworkpolicies: 'Cilium L3-L7 traffic rules (cluster-wide).',
  insights: 'Detected issues and recommendations across the cluster.',
  clusters: 'Kubernetes clusters connected to KubeBolt.',
  rbac: 'Roles and bindings that govern access.',
  events: 'Recent events emitted by the cluster.',
}

// resourceTypeDescription returns the subtitle for a resource type, or "" when
// none is defined (caller should then render no subtitle).
export function resourceTypeDescription(type: string): string {
  return resourceTypeDescriptionMap[type] ?? ''
}

// ResourceTypeIcon renders the tinted (kb-accent) icon for a resource type
// next to a page title. Falls back to a generic Boxes icon for unmapped
// types so a new type still gets a sensible default.
export function ResourceTypeIcon({
  type,
  className = 'w-5 h-5',
}: {
  type: string
  className?: string
}) {
  const Icon = resourceTypeIconMap[type] ?? Boxes
  return <Icon className={`${className} text-kb-accent`} />
}
