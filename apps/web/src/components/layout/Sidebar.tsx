import { NavLink } from 'react-router-dom'
import {
  Zap,
  LayoutDashboard,
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
  Activity,
  Settings,
} from 'lucide-react'
import type { ClusterOverview } from '@/types/kubernetes'

interface SidebarProps {
  overview?: ClusterOverview
}

interface NavItem {
  label: string
  path: string
  icon: React.ReactNode
  countKey?: keyof ClusterOverview
}

interface NavSection {
  title: string
  items: NavItem[]
}

const sections: NavSection[] = [
  {
    title: 'Pinned',
    items: [
      { label: 'Pods', path: '/pods', icon: <Box className="w-4 h-4" />, countKey: 'pods' },
      { label: 'Nodes', path: '/nodes', icon: <Server className="w-4 h-4" />, countKey: 'nodes' },
    ],
  },
  {
    title: 'Workloads',
    items: [
      { label: 'Deployments', path: '/deployments', icon: <Layers className="w-4 h-4" />, countKey: 'deployments' },
      { label: 'StatefulSets', path: '/statefulsets', icon: <Database className="w-4 h-4" />, countKey: 'statefulSets' },
      { label: 'DaemonSets', path: '/daemonsets', icon: <BarChart3 className="w-4 h-4" />, countKey: 'daemonSets' },
      { label: 'Jobs', path: '/jobs', icon: <Timer className="w-4 h-4" />, countKey: 'jobs' },
      { label: 'CronJobs', path: '/cronjobs', icon: <Clock className="w-4 h-4" />, countKey: 'cronJobs' },
    ],
  },
  {
    title: 'Traffic',
    items: [
      { label: 'Services', path: '/services', icon: <Globe className="w-4 h-4" />, countKey: 'services' },
      { label: 'Ingresses', path: '/ingresses', icon: <ArrowRightLeft className="w-4 h-4" />, countKey: 'ingresses' },
      { label: 'Gateways', path: '/gateways', icon: <Globe className="w-4 h-4" /> },
      { label: 'HTTPRoutes', path: '/httproutes', icon: <ArrowRightLeft className="w-4 h-4" /> },
      { label: 'Endpoints', path: '/endpoints', icon: <Radio className="w-4 h-4" /> },
    ],
  },
  {
    title: 'Storage',
    items: [
      { label: 'PVCs', path: '/pvcs', icon: <HardDrive className="w-4 h-4" />, countKey: 'pvcs' },
      { label: 'PVs', path: '/pvs', icon: <Disc className="w-4 h-4" />, countKey: 'pvs' },
      { label: 'StorageClasses', path: '/storageclasses', icon: <FolderClosed className="w-4 h-4" /> },
    ],
  },
  {
    title: 'Config',
    items: [
      { label: 'ConfigMaps', path: '/configmaps', icon: <FileText className="w-4 h-4" />, countKey: 'configMaps' },
      { label: 'Secrets', path: '/secrets', icon: <Lock className="w-4 h-4" />, countKey: 'secrets' },
      { label: 'HPAs', path: '/hpas', icon: <Scale className="w-4 h-4" />, countKey: 'hpas' },
    ],
  },
  {
    title: 'Cluster',
    items: [
      { label: 'Namespaces', path: '/namespaces', icon: <FolderOpen className="w-4 h-4" />, countKey: 'namespaces' },
      { label: 'RBAC', path: '/rbac', icon: <Shield className="w-4 h-4" /> },
      { label: 'Events', path: '/events', icon: <Activity className="w-4 h-4" /> },
    ],
  },
]

function getCount(overview: ClusterOverview | undefined, key?: keyof ClusterOverview): number | undefined {
  if (!overview || !key) return undefined
  const val = overview[key]
  if (val && typeof val === 'object' && 'total' in val) {
    return (val as { total: number }).total
  }
  return undefined
}

export function Sidebar({ overview }: SidebarProps) {
  return (
    <aside className="w-[220px] h-full bg-kb-sidebar border-r border-kb-border flex flex-col shrink-0">
      {/* Logo */}
      <div className="px-4 h-[52px] flex items-center gap-2 border-b border-kb-border">
        <div className="w-7 h-7 rounded-lg bg-gradient-to-br from-status-info to-blue-600 flex items-center justify-center">
          <Zap className="w-4 h-4 text-white" />
        </div>
        <div className="flex flex-col">
          <span className="text-sm font-semibold text-kb-text-primary leading-tight">KubeBolt</span>
          <span className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em]">v0.1.0 beta</span>
        </div>
      </div>

      {/* Nav sections */}
      <nav className="flex-1 overflow-y-auto py-3 px-2 space-y-4">
        {/* Overview link */}
        <div>
          <NavLink
            to="/"
            end
            className={({ isActive }) =>
              `flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] transition-colors relative ${
                isActive
                  ? 'bg-status-info-dim text-status-info'
                  : 'text-kb-text-secondary hover:text-kb-text-primary hover:bg-kb-card'
              }`
            }
          >
            {({ isActive }) => (
              <>
                {isActive && (
                  <div className="absolute left-0 top-1 bottom-1 w-[2px] rounded-full bg-status-info" />
                )}
                <span className="shrink-0"><LayoutDashboard className="w-4 h-4" /></span>
                <span className="flex-1 truncate">Overview</span>
              </>
            )}
          </NavLink>
        </div>

        {sections.map((section) => (
          <div key={section.title}>
            <div className="px-2 mb-1 text-[9px] font-mono font-medium uppercase tracking-[0.1em] text-kb-text-tertiary">
              {section.title}
            </div>
            <div className="space-y-0.5">
              {section.items.map((item) => {
                const count = getCount(overview, item.countKey)
                return (
                  <NavLink
                    key={item.path}
                    to={item.path}
                    className={({ isActive }) =>
                      `flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] transition-colors group relative ${
                        isActive
                          ? 'bg-status-info-dim text-status-info'
                          : 'text-kb-text-secondary hover:text-kb-text-primary hover:bg-kb-card'
                      }`
                    }
                  >
                    {({ isActive }) => (
                      <>
                        {isActive && (
                          <div className="absolute left-0 top-1 bottom-1 w-[2px] rounded-full bg-status-info" />
                        )}
                        <span className="shrink-0">{item.icon}</span>
                        <span className="flex-1 truncate">{item.label}</span>
                        {count !== undefined && (
                          <span className="text-[10px] font-mono text-kb-text-tertiary">{count}</span>
                        )}
                      </>
                    )}
                  </NavLink>
                )
              })}
            </div>
          </div>
        ))}
      </nav>

      {/* Settings */}
      <div className="px-2 py-3 border-t border-kb-border">
        <NavLink
          to="/settings"
          className={({ isActive }) =>
            `flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] transition-colors ${
              isActive
                ? 'bg-status-info-dim text-status-info'
                : 'text-kb-text-secondary hover:text-kb-text-primary hover:bg-kb-card'
            }`
          }
        >
          <Settings className="w-4 h-4" />
          <span>Settings</span>
        </NavLink>
      </div>
    </aside>
  )
}
