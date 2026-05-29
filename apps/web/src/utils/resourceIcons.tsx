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
  ShieldOff,
  KeyRound,
  Puzzle,
  SlidersHorizontal,
  Package,
  Boxes,
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
  hpas: Scale,
  namespaces: FolderOpen,
  certificates: KeyRound,
  argocdapps: Puzzle,
  vpas: SlidersHorizontal,
  applications: Package,
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
