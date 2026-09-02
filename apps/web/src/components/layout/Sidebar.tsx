import { useState, useCallback, useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { NavLink, useLocation } from 'react-router-dom'
import { api } from '@/services/api'
import { isDashboardPath } from '@/utils/routes'
import { useScope, routeScope, type RouteScope } from '@/utils/scope'
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
  ShieldCheck,
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
  UserCog,
  ChevronRight,
  MessageSquarePlus,
  Boxes,
  House,
  ShieldAlert,
} from 'lucide-react'
import { useAuth } from '@/contexts/AuthContext'
import { useUIConfig } from '@/hooks/useUIConfig'
import { VERSION } from '@/version'
import { AboutModal } from '@/components/layout/AboutModal'
import { KubeBoltLogo } from '@/components/shared/KubeBoltLogo'
import type { ClusterOverview } from '@/types/kubernetes'
import { eePinnedNavItems } from '@/ee/registry'
import { useMetricsOnly } from '@/hooks/useMetricsOnly'
import { useAdminLanding } from '@/hooks/useAdminLanding'

interface SidebarProps {
  overview?: ClusterOverview
  // Icons-only mode — width shrinks, labels/counts/section titles hide,
  // each NavLink keeps a native title tooltip so the operator can still
  // discover what each icon means. The toggle button lives in the Topbar
  // (top-left), not here, so the sidebar header always shows the logo.
  collapsed: boolean
}

export interface NavItem {
  label: string
  path: string
  icon: React.ReactNode
  countKey?: keyof ClusterOverview
  permissionKey?: string
  // Render the item non-clickable (dimmed, no navigation) — for features that
  // are shipped in the nav but not yet enabled in this release.
  disabled?: boolean
  // Small pill shown after the label (e.g. "Soon"). Pairs with `disabled`.
  badge?: string
}

interface NavSection {
  title: string
  items: NavItem[]
}

// Las dos alturas del producto tienen menú propio (§3 del plan de dos ámbitos).
// `public` no aparece: esas rutas se montan fuera de Layout, así que nunca hay
// sidebar que elegir.
export type SidebarVariant = 'global' | 'cluster'

// El ancho es por variante y no una constante: el menú global lleva cuatro
// enlaces de una palabra, y 220px reservados para "Cilium Cluster Policies"
// dejaban una columna medio vacía en la primera pantalla del producto.
const EXPANDED_WIDTH: Record<SidebarVariant, string> = {
  global: 'w-[190px]',
  cluster: 'w-[220px]',
}
const RAIL_WIDTH = 'w-[56px]'

// Los ítems fijados que aporta la edición se reparten por SU ámbito, no por
// donde estén declarados: hoy `eePinnedNavItems` trae Autopilot (de cluster) y
// Plan & usage (global) en la misma lista, y meterlos juntos en cualquiera de
// los dos menús pondría un enlace que salta de altitud al pulsarlo.
const eePinnedFor = (scope: RouteScope) =>
  eePinnedNavItems.filter((i) => routeScope(i.path) === scope)

// ─── El menú de CLUSTER ──────────────────────────────────────────────────────
// Los 7 grupos de siempre, menos las superficies que suben a global (Home,
// Fleet, Security, Clusters). El contenido no cambia; cambia dónde vive.
const clusterSections: NavSection[] = [
  {
    title: 'Pinned',
    items: [
      { label: 'Insights', path: '/insights', icon: <Lightbulb className="w-4 h-4" /> },
      // EE extension point — edition-specific pinned items (e.g. Autopilot).
      // Empty in OSS via @/ee/registry; the Enterprise build overrides it.
      ...eePinnedFor('cluster'),
      { label: 'Applications', path: '/applications', icon: <Package className="w-4 h-4" />, countKey: 'helmReleases' },
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
      { label: 'Cilium Policies', path: '/ciliumnetworkpolicies', icon: <ShieldCheck className="w-4 h-4" />, countKey: 'ciliumNetworkPolicies', permissionKey: 'ciliumnetworkpolicies' },
      { label: 'Cilium Cluster Policies', path: '/ciliumclusterwidenetworkpolicies', icon: <ShieldCheck className="w-4 h-4" />, countKey: 'ciliumClusterwideNetworkPolicies', permissionKey: 'ciliumclusterwidenetworkpolicies' },
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
      { label: 'Service Accounts', path: '/serviceaccounts', icon: <UserCog className="w-4 h-4" />, countKey: 'serviceAccounts', permissionKey: 'serviceaccounts' },
      { label: 'HPAs', path: '/hpas', icon: <Scale className="w-4 h-4" />, countKey: 'hpas', permissionKey: 'hpas' },
    ],
  },
  {
    // Optional standard CRDs (Sprint 3) — shown only-useful when the CRD is
    // installed; the list is empty otherwise. No count/permission gating.
    title: 'Extensions',
    items: [
      { label: 'Certificates', path: '/certificates', icon: <KeyRound className="w-4 h-4" />, countKey: 'certificates' },
      { label: 'ArgoCD Apps', path: '/argocdapps', icon: <Puzzle className="w-4 h-4" />, countKey: 'argocdApps' },
      { label: 'VPA', path: '/vpas', icon: <SlidersHorizontal className="w-4 h-4" />, countKey: 'vpas' },
    ],
  },
  {
    title: 'Cluster',
    items: [
      // Administrar clusters —alta, renombrado, borrado— es trabajo DE cluster.
      // Fleet, arriba, es la superficie global de la flota: mirarla entera.
      // Ésta es la de operarla, y por eso vive aquí abajo.
      { label: 'Clusters', path: '/clusters', icon: <Server className="w-4 h-4" /> },
      { label: 'Namespaces', path: '/namespaces', icon: <FolderOpen className="w-4 h-4" />, countKey: 'namespaces', permissionKey: 'namespaces' },
      { label: 'RBAC', path: '/rbac', icon: <Shield className="w-4 h-4" />, permissionKey: 'roles' },
      { label: 'Events', path: '/events', icon: <Activity className="w-4 h-4" />, permissionKey: 'events' },
    ],
  },
]

// ─── El menú GLOBAL ──────────────────────────────────────────────────────────
// Un solo grupo fijado. Nada de `countKey` ni `permissionKey`: esos salen del
// overview del cluster activo, que aquí no existe — y ésa es exactamente la
// razón de que Fleet y Security se apagaran en un cluster metrics-only pese a
// no necesitar connector para nada.
//
// **Fleet** es la superficie de flota que se queda arriba: mirar todos los
// clusters a la vez. Administrarlos —`/clusters`— baja al menú de cluster, que
// es donde se opera. Las dos siguen la misma regla que sostiene el split y que
// vigila un test: **todo ítem de un menú apunta a una ruta de la altitud de ese
// menú**, para que pulsar un enlace nunca cambie el menú bajo el cursor.
const globalSections: NavSection[] = [
  {
    title: 'Pinned',
    items: [
      { label: 'Home', path: '/home', icon: <House className="w-4 h-4" /> },
      { label: 'Fleet', path: '/fleet', icon: <Boxes className="w-4 h-4" /> },
      { label: 'Security', path: '/security', icon: <ShieldAlert className="w-4 h-4" /> },
      ...eePinnedFor('global'),
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

// Administration grouped into domain hubs (each a tabbed page) so the area
// reads by concern instead of a flat dump. The former Settings tabs are split
// across these hubs; the per-domain management pages (Users, Teams, Agent
// Tokens, Activity, Integrations, Kobi Usage) become tabs within their hub.
const adminItems = [
  // Access — identity & sign-in: Users · Teams · Authentication.
  { label: 'Access', path: '/admin/access', icon: <Shield className="w-4 h-4" /> },
  // Agents & Ingest — getting data in: Agent Tokens · Activity · Integrations · Config.
  { label: 'Agents & Ingest', path: '/admin/agents', icon: <Radio className="w-4 h-4" /> },
  // AI (Kobi) — copilot Config · Usage.
  { label: 'AI (Kobi)', path: '/admin/ai', icon: <Bot className="w-4 h-4" /> },
  // Insights — the #44 rule matrix + both suppression layers + history.
  { label: 'Insights', path: '/admin/insights', icon: <Lightbulb className="w-4 h-4" /> },
  // System — instance-wide: General · Notifications.
  { label: 'System', path: '/admin/system', icon: <SlidersHorizontal className="w-4 h-4" /> },
  // API Tokens — long-lived REST tokens (kbs_/kbk_). Standalone.
  { label: 'API Tokens', path: '/admin/api-tokens', icon: <KeyRound className="w-4 h-4" /> },
]

// Every nav group EXCEPT Pinned is collapsible. Pinned is always visible so the
// operator's most-used links never hide. Collapsed state persists in
// localStorage (mirrors kb-sidebar-collapsed / kb-theme / kb-refresh-interval).
//
// Los dos menús tienen grupos DISTINTOS, así que tanto los defaults como el
// sitio donde se guardan van por variante. Compartirlos era la trampa que
// avisaba el plan (riesgo 2 y 3 de §9):
//
//   · Defaults por título — el mapa se sembraba con los títulos del menú de
//     cluster. El global, cuyos únicos grupos plegables son Administración y
//     Platform, heredaba `true` para los dos y arrancaba mostrando sólo los
//     ítems fijados: un menú de tres grupos con dos cerrados de fábrica.
//   · Una sola key — plegar Workloads abajo escribía en el mismo sitio que
//     plegar Administración arriba, así que los dos menús se pisaban el estado
//     y el usuario veía cerrarse cosas que él no había tocado.
const COLLAPSIBLE_BY_VARIANT: Record<SidebarVariant, string[]> = {
  cluster: ['Workloads', 'Traffic', 'Storage', 'Config', 'Extensions', 'Cluster'],
  // Administración y Platform sólo existen arriba; no son plegables por defecto
  // porque en un menú de esta talla plegar no compra nada.
  global: [],
}

// El grupo que arranca ABIERTO en cada menú. En cluster es Workloads —el resto
// se pliega para que un usuario nuevo vea un menú corto en vez del firehose.
const DEFAULT_OPEN_GROUP: Record<SidebarVariant, string> = {
  cluster: 'Workloads',
  global: '',
}

// Key distinta por variante. La vieja (`kb-sidebar-groups-collapsed`) se deja
// morir sin migrar: guarda el estado de plegado de un menú que ya no existe con
// esa forma, y lo peor que pasa al ignorarla es que el usuario vuelva a ver sus
// defaults una vez.
const groupsStorageKey = (variant: SidebarVariant) => `kb-sidebar-groups-collapsed:${variant}`

// A map of group-title -> collapsed(bool). On first visit (nothing stored) every
// group starts COLLAPSED except the variant's default-open one.
function loadCollapsedGroups(variant: SidebarVariant): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(groupsStorageKey(variant))
    if (raw) {
      const parsed = JSON.parse(raw)
      if (parsed && typeof parsed === 'object') return parsed as Record<string, boolean>
    }
  } catch { /* private mode / bad JSON — fall through to defaults */ }
  const init: Record<string, boolean> = {}
  for (const g of COLLAPSIBLE_BY_VARIANT[variant]) init[g] = g !== DEFAULT_OPEN_GROUP[variant]
  return init
}

export function Sidebar({ overview, collapsed: collapsedProp }: SidebarProps) {
  // Collapsed rail "peeks" open while hovered — it expands to a floating overlay
  // and snaps back on mouse-leave. `collapsed` is the EFFECTIVE state every label /
  // header / width decision below reads, so the whole sidebar renders expanded
  // during the peek without touching each call site. The flow width stays pinned to
  // the rail on the wrapper <div> so the content never reflows — the panel floats
  // over it (see the return). Peek only arms when the persisted state is collapsed.
  const [peeking, setPeeking] = useState(false)
  const collapsed = collapsedProp && !peeking
  // Qué menú toca. `public` no llega aquí (esas rutas no montan Layout), pero
  // se mapea a cluster para que el tipo cierre sin un default silencioso.
  const scope = useScope()
  const variant: SidebarVariant = scope === 'global' ? 'global' : 'cluster'
  const sections = variant === 'global' ? globalSections : clusterSections
  // Primer hub de administración que este usuario puede abrir, o null si ninguno.
  // Compartido con el menú del avatar (Topbar) para que ambos ofrezcan el mismo
  // destino: dos derivaciones distintas acabarían mostrando una entrada que
  // lleva a un 403.
  const adminLanding = useAdminLanding()
  const [clickCount, setClickCount] = useState(0)
  const [celebrating, setCelebrating] = useState(false)
  const [aboutOpen, setAboutOpen] = useState(false)
  const { hasRole, isAuthEnabled, user } = useAuth()
  // Cluster count for the Clusters nav badge — reuses the ['clusters'] cache the
  // ClustersPage already populates, so it's effectively free here.
  const { data: clusters } = useQuery({ queryKey: ['clusters'], queryFn: api.listClusters })
  const location = useLocation()
  const uiConfig = useUIConfig()
  // Metrics-only active cluster → dim + disable the resource-view nav (Pods, workloads,
  // etc.); those endpoints have no connector. The metrics dashboards stay reachable via
  // the Overview link.
  const isMetricsOnly = useMetricsOnly()
  const brandLabel = uiConfig.displayName?.trim() || 'KubeBolt'
  // Overview is the entry point for the whole dashboard surface
  // (Overview / Capacity / Reliability sub-tabs). All three should
  // light up this nav item — the user is "on the dashboard"
  // regardless of which sub-tab they picked. NavLink's `end` prop
  // would only match `/` exact, which is why we drive active state
  // from the central path list instead.
  const dashboardActive = isDashboardPath(location.pathname)

  // Per-group collapse (Pinned excluded). Persisted so the layout is stable
  // across visits, like the other localStorage UI prefs.
  //
  // Se re-lee al cambiar de variante: cada menú guarda su plegado en su propia
  // key, así que subir a global y volver devuelve el menú de cluster tal como
  // lo dejaste, no como lo dejó el otro.
  const [collapsedGroups, setCollapsedGroups] = useState<Record<string, boolean>>(() =>
    loadCollapsedGroups(variant),
  )
  useEffect(() => {
    setCollapsedGroups(loadCollapsedGroups(variant))
  }, [variant])
  const toggleGroup = useCallback((title: string) => {
    setCollapsedGroups((prev) => {
      const next = { ...prev, [title]: !prev[title] }
      try { localStorage.setItem(groupsStorageKey(variant), JSON.stringify(next)) } catch { /* private mode */ }
      return next
    })
  }, [variant])
  // Collapsible group header with a chevron; Pinned renders a plain label.
  // In rail mode (sidebar collapsed) headers are hidden entirely — there's no
  // room for the chevron and all items already render as icon-only.
  const renderSectionHeader = (title: string, collapsible: boolean) => {
    if (collapsed) return null
    if (!collapsible) {
      return (
        <div className="px-2 mb-1 text-[10px] font-mono font-semibold uppercase tracking-[0.1em] text-kb-text-secondary">
          {title}
        </div>
      )
    }
    const isColl = !!collapsedGroups[title]
    return (
      <button
        type="button"
        onClick={() => toggleGroup(title)}
        aria-expanded={!isColl}
        className="w-full flex items-center gap-1 px-2 py-1 mb-1 rounded-md text-[10px] font-mono font-semibold uppercase tracking-[0.1em] text-kb-text-secondary hover:text-kb-text-primary hover:bg-kb-card transition-colors"
      >
        <ChevronRight className={`w-3 h-3 shrink-0 transition-transform ${isColl ? '' : 'rotate-90'}`} />
        <span className="flex-1 text-left">{title}</span>
      </button>
    )
  }
  // Whether a group's items render: Pinned always; otherwise honor the collapsed
  // map in EVERY mode. The collapsed rail (and its hover-peek) now group/ungroup
  // exactly like the expanded sidebar — the peek surfaces the section headers to
  // toggle groups from, so the rail no longer dumps every icon when collapsed.
  const showGroupItems = (title: string, collapsible: boolean) =>
    !collapsible || !collapsedGroups[title]

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
    // Flow spacer — reserves the layout width from the PERSISTED collapse state so
    // the content column never reflows while the rail peeks open. Hover only arms
    // the peek in collapsed mode. No z-index here (plain stacking parent) so the
    // AboutModal sibling below stays free to overlay the app.
    <div
      className={`h-full shrink-0 relative ${collapsedProp ? RAIL_WIDTH : EXPANDED_WIDTH[variant]}`}
      onMouseEnter={collapsedProp ? () => setPeeking(true) : undefined}
      onMouseLeave={collapsedProp ? () => setPeeking(false) : undefined}
    >
    <aside
      className={`absolute inset-y-0 left-0 z-[500] h-full bg-kb-sidebar border-r border-kb-border flex flex-col overflow-hidden transition-[width] duration-200 ease-out ${
        collapsed ? RAIL_WIDTH : EXPANDED_WIDTH[variant]
      } ${collapsedProp && peeking ? 'shadow-2xl' : ''}`}
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
        {/* Overview link — sólo en el menú de cluster: apunta al dashboard del
            cluster ACTIVO, así que en global era el único enlace del menú que
            cambiaba de altitud sin decirlo. */}
        {variant === 'cluster' && (
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
        )}

        {sections.map((section) => {
          const collapsible = section.title !== 'Pinned'
          return (
          <div key={section.title}>
            {renderSectionHeader(section.title, collapsible)}
            {showGroupItems(section.title, collapsible) && (
            <div className="space-y-0.5">
              {section.items.map((item) => {
                // Clusters isn't a per-cluster ClusterOverview count — it's the
                // size of the cluster list (durable membership).
                const count = item.path === '/clusters'
                  ? clusters?.length
                  : getCount(overview, item.countKey)
                const isRestricted = item.permissionKey != null
                  && overview?.permissions != null
                  && overview.permissions[item.permissionKey] === false
                // Metrics-only: every resource view needs a live API (connector), so dim +
                // disable them — except /clusters, the platform escape hatch. The metrics
                // dashboards stay reachable via the Overview link above.
                const metricsBlocked = isMetricsOnly && item.path !== '/clusters'

                // Not-yet-enabled feature (visible as a teaser, non-clickable, "Soon"
                // badge). Inert in OSS today — kept in sync with the EE sidebar.
                if (item.disabled) {
                  return (
                    <div
                      key={item.path}
                      title={`${item.label} — coming soon`}
                      className="flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] text-kb-text-tertiary cursor-not-allowed relative"
                    >
                      <span className="shrink-0 opacity-60">{item.icon}</span>
                      {!collapsed && <span className="flex-1 truncate opacity-60">{item.label}</span>}
                      {!collapsed && item.badge && (
                        <span className="text-[9px] font-mono font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded-full bg-kb-accent-light text-kb-accent">
                          {item.badge}
                        </span>
                      )}
                    </div>
                  )
                }

                if (metricsBlocked) {
                  return (
                    <div
                      key={item.path}
                      title="Monitored-only — needs the agent-proxy or a direct API connection"
                      className="flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] text-kb-text-primary opacity-40 cursor-not-allowed relative"
                    >
                      <span className="shrink-0">{item.icon}</span>
                      {!collapsed && <span className="flex-1 truncate">{item.label}</span>}
                      {!collapsed && <ShieldOff className="w-3 h-3 text-status-warn" />}
                    </div>
                  )
                }

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
                        {!collapsed && item.badge && (
                          <span className="text-[9px] font-mono font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded-full bg-kb-accent-light text-kb-accent">
                            {item.badge}
                          </span>
                        )}
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
            )}
          </div>
          )
        })}

        {/* Administration — admin only (or when auth disabled).

            Sólo en global: identidad, agentes, IA y sistema son de la
            ORGANIZACIÓN, no del cluster que tengas seleccionado. Que vivieran
            en el mismo menú que Pods es la razón de que un cluster
            metrics-only apagara media administración. */}
        {variant === 'global' && hasRole('admin') && (
          <div>
            {renderSectionHeader('Administration', true)}
            {showGroupItems('Administration', true) && (
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
            )}
          </div>
        )}
      </nav>

      {/* Administration (sólo DENTRO de un cluster) + Feedback + About */}
      <div className="px-2 py-3 border-t border-kb-border space-y-0.5">
        {/* En global, Administration ya tiene su propia sección arriba con sus
            hubs. Aquí, dentro de un cluster, va UNA entrada que navega a ella —
            no el árbol entero.

            No contradice el `variant !== 'global'` de arriba: aquella regla
            existe para que identidad/agentes/IA no vivan en el mismo menú que
            Pods, donde un cluster metrics-only apagaba media administración.
            Un enlace que SALE de ahí no tiene ese problema, y este grupo del
            pie ya es la excepción para lo que no es del cluster — Feedback y
            About llevan aquí desde siempre y tampoco lo son.

            El motivo de existir: un usuario en producción preguntó cómo llegar
            a la administración de su cuenta estando dentro de un cluster. La
            respuesta era pulsar «Fleet» en el topbar, que nombra sus clusters,
            no su cuenta. Navegar aquí NO cambia el cluster activo — ese es
            estado del backend, no de la ruta. */}
        {variant !== 'global' && adminLanding && (
          <NavLink
            to={adminLanding}
            title={collapsed ? 'Administration' : undefined}
            className="w-full flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] text-kb-text-primary hover:bg-kb-card transition-colors"
          >
            <Shield className="w-4 h-4 shrink-0" />
            {!collapsed && <span>Administration</span>}
          </NavLink>
        )}
        {/* Links to the public feedback form with the signed-in email prefilled,
            in a new tab so the operator never loses their place in the app. */}
        <a
          href={`https://kubebolt.io/feedback${user?.email ? `?email=${encodeURIComponent(user.email)}` : ''}`}
          target="_blank"
          rel="noopener noreferrer"
          title={collapsed ? 'Send feedback' : undefined}
          className="w-full flex items-center gap-2.5 px-2 py-1.5 rounded-md text-[13px] text-kb-text-primary hover:bg-kb-card transition-colors"
        >
          <MessageSquarePlus className="w-4 h-4 shrink-0" />
          {!collapsed && <span>Feedback</span>}
        </a>
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

    </aside>
      {/* Rendered as a sibling of the floating <aside> (outside its z-[500]
          stacking context) so the full-screen modal can overlay the whole app. */}
      {aboutOpen && <AboutModal onClose={() => setAboutOpen(false)} />}
    </div>
  )
}
