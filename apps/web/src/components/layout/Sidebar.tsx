import { useState, useCallback } from 'react'
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
  ShieldOff,
  Users,
  UsersRound,
  Bell,
  Bot,
  KeyRound,
  Lightbulb,
} from 'lucide-react'
import { useAuth } from '@/contexts/AuthContext'
import type { ClusterOverview } from '@/types/kubernetes'

interface SidebarProps {
  overview?: ClusterOverview
}

interface NavItem {
  label: string
  path: string
  icon: React.ReactNode
  countKey?: keyof ClusterOverview
  permissionKey?: string
}

interface NavSection {
  title: string
  items: NavItem[]
}

const sections: NavSection[] = [
  {
    title: 'Pinned',
    items: [
      { label: 'Insights', path: '/insights', icon: <Lightbulb className="w-4 h-4" /> },
      { label: 'Pods', path: '/pods', icon: <Box className="w-4 h-4" />, countKey: 'pods', permissionKey: 'pods' },
      { label: 'Nodes', path: '/nodes', icon: <Server className="w-4 h-4" />, countKey: 'nodes', permissionKey: 'nodes' },
    ],
  },
  {
    title: 'Workloads',
    items: [
      { label: 'Deployments', path: '/deployments', icon: <Layers className="w-4 h-4" />, countKey: 'deployments', permissionKey: 'deployments' },
      { label: 'StatefulSets', path: '/statefulsets', icon: <Database className="w-4 h-4" />, countKey: 'statefulSets', permissionKey: 'statefulsets' },
      { label: 'DaemonSets', path: '/daemonsets', icon: <BarChart3 className="w-4 h-4" />, countKey: 'daemonSets', permissionKey: 'daemonsets' },
      { label: 'Jobs', path: '/jobs', icon: <Timer className="w-4 h-4" />, countKey: 'jobs', permissionKey: 'jobs' },
      { label: 'CronJobs', path: '/cronjobs', icon: <Clock className="w-4 h-4" />, countKey: 'cronJobs', permissionKey: 'cronjobs' },
    ],
  },
  {
    title: 'Traffic',
    items: [
      { label: 'Services', path: '/services', icon: <Globe className="w-4 h-4" />, countKey: 'services', permissionKey: 'services' },
      { label: 'Ingresses', path: '/ingresses', icon: <ArrowRightLeft className="w-4 h-4" />, countKey: 'ingresses', permissionKey: 'ingresses' },
      { label: 'Gateways', path: '/gateways', icon: <Globe className="w-4 h-4" /> },
      { label: 'HTTPRoutes', path: '/httproutes', icon: <ArrowRightLeft className="w-4 h-4" /> },
      { label: 'Endpoints', path: '/endpoints', icon: <Radio className="w-4 h-4" />, permissionKey: 'endpointslices' },
    ],
  },
  {
    title: 'Storage',
    items: [
      { label: 'PVCs', path: '/pvcs', icon: <HardDrive className="w-4 h-4" />, countKey: 'pvcs', permissionKey: 'pvcs' },
      { label: 'PVs', path: '/pvs', icon: <Disc className="w-4 h-4" />, countKey: 'pvs', permissionKey: 'pvs' },
      { label: 'StorageClasses', path: '/storageclasses', icon: <FolderClosed className="w-4 h-4" />, permissionKey: 'storageclasses' },
    ],
  },
  {
    title: 'Config',
    items: [
      { label: 'ConfigMaps', path: '/configmaps', icon: <FileText className="w-4 h-4" />, countKey: 'configMaps', permissionKey: 'configmaps' },
      { label: 'Secrets', path: '/secrets', icon: <Lock className="w-4 h-4" />, countKey: 'secrets', permissionKey: 'secrets' },
      { label: 'HPAs', path: '/hpas', icon: <Scale className="w-4 h-4" />, countKey: 'hpas', permissionKey: 'hpas' },
    ],
  },
  {
    title: 'Cluster',
    items: [
      { label: 'Clusters', path: '/clusters', icon: <Server className="w-4 h-4" /> },
      { label: 'Namespaces', path: '/namespaces', icon: <FolderOpen className="w-4 h-4" />, countKey: 'namespaces', permissionKey: 'namespaces' },
      { label: 'RBAC', path: '/rbac', icon: <Shield className="w-4 h-4" />, permissionKey: 'roles' },
      { label: 'Events', path: '/events', icon: <Activity className="w-4 h-4" />, permissionKey: 'events' },
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

const BOLT_EMOJIS = ['⚡', '🔥', '🌟', '💫', '✨', '🚀', '💜']

const adminItems = [
  { label: 'Users', path: '/admin/users', icon: <Users className="w-4 h-4" /> },
  { label: 'Notifications', path: '/admin/notifications', icon: <Bell className="w-4 h-4" /> },
  { label: 'Copilot Usage', path: '/admin/copilot-usage', icon: <BarChart3 className="w-4 h-4" /> },
  { label: 'Teams', path: '/admin/teams', icon: <UsersRound className="w-4 h-4" /> },
  { label: 'Service Accounts', path: '/admin/service-accounts', icon: <Bot className="w-4 h-4" /> },
  { label: 'Authentication', path: '/admin/authentication', icon: <KeyRound className="w-4 h-4" /> },
]

export function Sidebar({ overview }: SidebarProps) {
  const [clickCount, setClickCount] = useState(0)
  const [celebrating, setCelebrating] = useState(false)
  const { hasRole, isAuthEnabled } = useAuth()

  const handleLogoClick = useCallback(() => {
    const next = clickCount + 1
    if (next >= 7) {
      setCelebrating(true)
      setClickCount(0)
      setTimeout(() => setCelebrating(false), 2500)
    } else {
      setClickCount(next)
    }
  }, [clickCount])

  return (
    <aside className="w-[220px] h-full bg-kb-sidebar border-r border-kb-border flex flex-col shrink-0 relative overflow-hidden">
      {/* Celebration particles */}
      {celebrating && (
        <div className="absolute inset-0 pointer-events-none z-50 overflow-hidden">
          {BOLT_EMOJIS.map((emoji, i) => (
            <span
              key={i}
              className="absolute text-lg animate-celebrate"
              style={{
                left: `${10 + (i * 28) % 80}%`,
                animationDelay: `${i * 0.15}s`,
              }}
            >
              {emoji}
            </span>
          ))}
          <div className="absolute top-12 left-0 right-0 text-center">
            <span className="text-[10px] font-mono font-bold text-yellow-400 animate-pulse tracking-wider">
              FIRST STAR ★ THANK YOU!
            </span>
          </div>
        </div>
      )}

      {/* Logo */}
      <div
        className="px-4 h-[52px] flex items-center gap-2 border-b border-kb-border cursor-pointer select-none"
        onClick={handleLogoClick}
      >
        <div className={`w-7 h-7 rounded-lg bg-kb-accent-light flex items-center justify-center transition-transform ${celebrating ? 'animate-spin' : ''}`}>
          <Zap className="w-4 h-4 text-kb-accent" />
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
                const isRestricted = item.permissionKey != null
                  && overview?.permissions != null
                  && overview.permissions[item.permissionKey] === false
                return (
                  <NavLink
                    key={item.path}
                    to={item.path}
                    className={({ isActive }) =>
                      `flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] transition-colors group relative ${
                        isActive
                          ? 'bg-status-info-dim text-status-info'
                          : 'text-kb-text-secondary hover:text-kb-text-primary hover:bg-kb-card'
                      } ${isRestricted ? 'opacity-40' : ''}`
                    }
                  >
                    {({ isActive }) => (
                      <>
                        {isActive && (
                          <div className="absolute left-0 top-1 bottom-1 w-[2px] rounded-full bg-status-info" />
                        )}
                        <span className="shrink-0">{item.icon}</span>
                        <span className="flex-1 truncate">{item.label}</span>
                        {isRestricted ? (
                          <ShieldOff className="w-3 h-3 text-status-warn" />
                        ) : count !== undefined ? (
                          <span className="text-[10px] font-mono text-kb-text-tertiary">{count}</span>
                        ) : null}
                      </>
                    )}
                  </NavLink>
                )
              })}
            </div>
          </div>
        ))}

        {/* Administration section — admin only (or when auth disabled) */}
        {hasRole('admin') && (
          <div>
            <div className="px-2 mb-1 text-[9px] font-mono font-medium uppercase tracking-[0.1em] text-kb-text-tertiary">
              Administration
            </div>
            <div className="space-y-0.5">
              {adminItems.map((item) => (
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
                    </>
                  )}
                </NavLink>
              ))}
            </div>
          </div>
        )}
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
