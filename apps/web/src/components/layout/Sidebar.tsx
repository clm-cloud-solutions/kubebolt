import { useState, useCallback } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { isDashboardPath } from '@/utils/routes'
import {
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
  SlidersHorizontal,
  ShieldOff,
  Users,
  UsersRound,
  Bot,
  KeyRound,
  Lightbulb,
  Puzzle,
  Info,
  Package,
} from 'lucide-react'
import { useAuth } from '@/contexts/AuthContext'
import { useUIConfig } from '@/hooks/useUIConfig'
import { VERSION } from '@/version'
import { AboutModal } from '@/components/layout/AboutModal'
import { KubeBoltLogo } from '@/components/shared/KubeBoltLogo'
import type { ClusterOverview } from '@/types/kubernetes'

interface SidebarProps {
  overview?: ClusterOverview
  // Icons-only mode — width shrinks, labels/counts/section titles hide,
  // each NavLink keeps a native title tooltip so the operator can still
  // discover what each icon means. The toggle button lives in the Topbar
  // (top-left), not here, so the sidebar header always shows the logo.
  collapsed: boolean
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
      { label: 'Applications', path: '/applications', icon: <Package className="w-4 h-4" /> },
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
      { label: 'NetworkPolicies', path: '/networkpolicies', icon: <Shield className="w-4 h-4" />, countKey: 'networkPolicies', permissionKey: 'networkpolicies' },
      { label: 'PodDisruptionBudgets', path: '/pdbs', icon: <ShieldOff className="w-4 h-4" />, countKey: 'podDisruptionBudgets', permissionKey: 'pdbs' },
      { label: 'Gateways', path: '/gateways', icon: <Globe className="w-4 h-4" />, countKey: 'gateways' },
      { label: 'HTTPRoutes', path: '/httproutes', icon: <ArrowRightLeft className="w-4 h-4" />, countKey: 'httpRoutes' },
      { label: 'Endpoints', path: '/endpoints', icon: <Radio className="w-4 h-4" />, countKey: 'endpoints', permissionKey: 'endpointslices' },
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
  // "Settings" first — single home for the env-only config that's now
  // UI-editable. Other admin pages stay in place for their dedicated
  // surfaces (Users, Tenants, Integrations, etc.).
  { label: 'Settings', path: '/admin/settings', icon: <SlidersHorizontal className="w-4 h-4" /> },
  { label: 'Users', path: '/admin/users', icon: <Users className="w-4 h-4" /> },
  { label: 'Agent Tokens', path: '/admin/agent-tokens', icon: <KeyRound className="w-4 h-4" /> },
  // Ingest Activity sits between Agent Tokens and Integrations because
  // the operator workflow is: issue tokens (Agent Tokens) → install
  // agents → see them connect (Ingest Activity) → wire integrations
  // (Integrations). The page is admin-only and lives behind the same
  // role gate as the rest of /admin/*. Spec #09 V2 Item 5b.
  { label: 'Ingest Activity', path: '/admin/ingest-activity', icon: <Activity className="w-4 h-4" /> },
  { label: 'Integrations', path: '/admin/integrations', icon: <Puzzle className="w-4 h-4" /> },
  { label: 'Kobi Usage', path: '/admin/copilot-usage', icon: <BarChart3 className="w-4 h-4" /> },
  { label: 'Teams', path: '/admin/teams', icon: <UsersRound className="w-4 h-4" /> },
  { label: 'Service Accounts', path: '/admin/service-accounts', icon: <Bot className="w-4 h-4" /> },
  { label: 'Authentication', path: '/admin/authentication', icon: <KeyRound className="w-4 h-4" /> },
]

export function Sidebar({ overview, collapsed }: SidebarProps) {
  const [clickCount, setClickCount] = useState(0)
  const [celebrating, setCelebrating] = useState(false)
  const [aboutOpen, setAboutOpen] = useState(false)
  const { hasRole, isAuthEnabled } = useAuth()
  const location = useLocation()
  const uiConfig = useUIConfig()
  const brandLabel = uiConfig.displayName?.trim() || 'KubeBolt'
  // Overview is the entry point for the whole dashboard surface
  // (Overview / Capacity / Reliability sub-tabs). All three should
  // light up this nav item — the user is "on the dashboard"
  // regardless of which sub-tab they picked. NavLink's `end` prop
  // would only match `/` exact, which is why we drive active state
  // from the central path list instead.
  const dashboardActive = isDashboardPath(location.pathname)

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
    <aside
      className={`h-full bg-kb-sidebar border-r border-kb-border flex flex-col shrink-0 relative overflow-hidden transition-[width] duration-200 ease-out ${
        collapsed ? 'w-[56px]' : 'w-[220px]'
      }`}
    >
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

      {/* Logo — always visible. The collapse toggle lives in the Topbar
          so the logo doesn't have to share the 56px header with another
          control. In collapsed mode the logo centers; in expanded mode
          it sits left with name + version next to it. */}
      <div className={`px-3 h-[52px] flex items-center gap-2 border-b border-kb-border select-none ${collapsed ? 'justify-center' : ''}`}>
        <div
          onClick={handleLogoClick}
          className={`w-7 h-7 rounded-lg bg-kb-accent-light flex items-center justify-center transition-transform shrink-0 cursor-pointer ${celebrating ? 'animate-spin' : ''}`}
        >
          <KubeBoltLogo className="w-4 h-4 text-kb-accent" />
        </div>
        {!collapsed && (
          <div onClick={handleLogoClick} className="flex flex-col min-w-0 cursor-pointer">
            <span
              className="text-sm font-semibold text-kb-text-primary leading-tight truncate"
              title={brandLabel === 'KubeBolt' ? undefined : brandLabel}
            >
              {brandLabel}
            </span>
            <span className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em]">v{VERSION}</span>
          </div>
        )}
      </div>

      {/* Nav sections. Scrollbar is hidden (cross-browser) so the menu
          stays visually clean — content still scrolls on shorter viewports.
          Section titles are hidden when collapsed; the space-y-4 between
          sections keeps the visual grouping intact. Idle label color is
          kb-text-primary (instead of secondary) so the nav reads with
          presence; counts and section titles stay subdued as metadata. */}
      <nav className="flex-1 overflow-y-auto py-3 px-2 space-y-4 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {/* Overview link */}
        <div>
          <NavLink
            to="/"
            title={collapsed ? 'Overview' : undefined}
            className={`flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] transition-colors relative ${
              dashboardActive
                ? 'bg-kb-accent-light text-kb-accent'
                : 'text-kb-text-primary hover:bg-kb-card'
            }`}
          >
            {dashboardActive && (
              <div className="absolute left-0 top-1 bottom-1 w-[2px] rounded-full bg-kb-accent" />
            )}
            <span className="shrink-0"><LayoutDashboard className="w-4 h-4" /></span>
            {!collapsed && <span className="flex-1 truncate">Overview</span>}
          </NavLink>
        </div>

        {sections.map((section) => (
          <div key={section.title}>
            {!collapsed && (
              <div className="px-2 mb-1 text-[9px] font-mono font-medium uppercase tracking-[0.1em] text-kb-text-tertiary">
                {section.title}
              </div>
            )}
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
                    title={collapsed ? item.label : undefined}
                    className={({ isActive }) =>
                      `flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] transition-colors group relative ${
                        isActive
                          ? 'bg-kb-accent-light text-kb-accent'
                          : 'text-kb-text-primary hover:bg-kb-card'
                      } ${isRestricted ? 'opacity-40' : ''}`
                    }
                  >
                    {({ isActive }) => (
                      <>
                        {isActive && (
                          <div className="absolute left-0 top-1 bottom-1 w-[2px] rounded-full bg-kb-accent" />
                        )}
                        <span className="shrink-0">{item.icon}</span>
                        {!collapsed && <span className="flex-1 truncate">{item.label}</span>}
                        {!collapsed && (isRestricted ? (
                          <ShieldOff className="w-3 h-3 text-status-warn" />
                        ) : count !== undefined ? (
                          <span className="text-[10px] font-mono text-kb-text-tertiary">{count}</span>
                        ) : null)}
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
            {!collapsed && (
              <div className="px-2 mb-1 text-[9px] font-mono font-medium uppercase tracking-[0.1em] text-kb-text-tertiary">
                Administration
              </div>
            )}
            <div className="space-y-0.5">
              {adminItems.map((item) => (
                <NavLink
                  key={item.path}
                  to={item.path}
                  title={collapsed ? item.label : undefined}
                  className={({ isActive }) =>
                    `flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] transition-colors group relative ${
                      isActive
                        ? 'bg-kb-accent-light text-kb-accent'
                        : 'text-kb-text-primary hover:bg-kb-card'
                    }`
                  }
                >
                  {({ isActive }) => (
                    <>
                      {isActive && (
                        <div className="absolute left-0 top-1 bottom-1 w-[2px] rounded-full bg-kb-accent" />
                      )}
                      <span className="shrink-0">{item.icon}</span>
                      {!collapsed && <span className="flex-1 truncate">{item.label}</span>}
                    </>
                  )}
                </NavLink>
              ))}
            </div>
          </div>
        )}
      </nav>

      {/* About */}
      <div className="px-2 py-3 border-t border-kb-border space-y-0.5">
        <button
          type="button"
          onClick={() => setAboutOpen(true)}
          title={collapsed ? 'About' : undefined}
          className="w-full flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] text-kb-text-primary hover:bg-kb-card transition-colors"
        >
          <Info className="w-4 h-4 shrink-0" />
          {!collapsed && <span>About</span>}
        </button>
      </div>

      {aboutOpen && <AboutModal onClose={() => setAboutOpen(false)} />}
    </aside>
  )
}
