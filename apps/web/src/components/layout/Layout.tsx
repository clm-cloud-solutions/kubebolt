import { useCallback, useEffect, useState } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useIsMutating, useQuery, useQueryClient } from '@tanstack/react-query'
import { Unplug, ShieldAlert, Loader2, Cable, X } from 'lucide-react'
import { Sidebar } from './Sidebar'
import { resolveDocumentTitle } from '@/utils/pageTitles'
import { Topbar } from './Topbar'
import { useClusterOverview } from '@/hooks/useClusterOverview'
import { useWebSocket } from '@/hooks/useWebSocket'
import { api, ApiError } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import { CopilotPanel } from '@/components/copilot/CopilotPanel'
import { CopilotToggle } from '@/components/copilot/CopilotToggle'
import { useCopilot } from '@/contexts/CopilotContext'
import { SetupWizard } from '@/components/setup/SetupWizard'

const WS_RESOURCES = ['pods', 'nodes', 'deployments', 'services', 'events']

export function Layout() {
  const { data: rawOverview, error, refetch } = useClusterOverview()
  const isSwitching = useIsMutating({ mutationKey: ['switch-cluster'] }) > 0
  const location = useLocation()
  const navigate = useNavigate()
  const { hasRole, isAuthEnabled } = useAuth()
  const isAdmin = hasRole('admin')
  const queryClient = useQueryClient()
  useWebSocket(WS_RESOURCES)

  // First-login wizard gate. Fires only for admins on installs where
  // auth is on (the wizard's step 1 is password rotation; meaningless
  // when auth is off). Single fetch — staleTime infinite after a
  // result because the flag only flips once. Errors fail-soft: if
  // we can't read the status, just don't show the wizard.
  const { data: setupStatus } = useQuery({
    queryKey: ['setup-status'],
    queryFn: api.getSetupStatus,
    enabled: isAdmin && isAuthEnabled,
    staleTime: Infinity,
    retry: false,
  })
  const showWizard = isAdmin && isAuthEnabled && setupStatus?.complete === false

  // Browser tab title — updates on every route change so a row of tabs
  // reads as distinct pages rather than 12 copies of the same string.
  // Page-name leads, product name follows (Linear / Notion convention).
  // The home route (/) is special-cased to show the marketing title.
  useEffect(() => {
    document.title = resolveDocumentTitle(location.pathname)
  }, [location.pathname])

  // Sidebar collapse preference — persisted to localStorage so the layout
  // reads the same after a refresh. The toggle lives in the sidebar header
  // (chevron icon) so operators can reclaim screen real estate when they
  // already know their way around without losing the nav.
  const [sidebarCollapsed, setSidebarCollapsed] = useState<boolean>(() => {
    try { return localStorage.getItem('kb-sidebar-collapsed') === 'true' } catch { return false }
  })
  const toggleSidebar = useCallback(() => {
    setSidebarCollapsed((v) => {
      const next = !v
      try { localStorage.setItem('kb-sidebar-collapsed', String(next)) } catch { /* private mode */ }
      return next
    })
  }, [])

  // First-use detection: zero clusters configured. Distinct from
  // "Cluster unreachable" (a selected cluster failed to connect) — here
  // there is nothing TO connect to, so retrying is pointless and the
  // user should be sent to the add-cluster flow.
  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    refetchInterval: 30_000,
  })
  // Go's encoding/json renders nil slices as `null`, not `[]`, so the
  // "no clusters" state surfaces as null here. Treat both as empty;
  // only `undefined` means "still loading first fetch".
  const noClusters = clusters !== undefined && (clusters === null || clusters.length === 0)
  // Routes that don't depend on a connected cluster. /clusters owns the
  // add-cluster wizard (must render so the user has a way out of the
  // empty state), and /admin /settings are platform-level pages whose
  // backend endpoints are cluster-agnostic. For these we bypass BOTH
  // the no-clusters CTA and the cluster-unreachable error page.
  const PLATFORM_ROUTE_PREFIXES = ['/clusters', '/admin', '/settings']
  const isPlatformRoute = PLATFORM_ROUTE_PREFIXES.some(p =>
    location.pathname === p || location.pathname.startsWith(p + '/')
  )

  const isUnavailable = error instanceof ApiError && error.status === 503
  // "Waiting for agent" is the transient post-restart state where the
  // backend's agent-proxy connector fast-fails because no agent has
  // dialed in yet. Distinct from a real "cluster unreachable" because
  // the cluster IS up — the agent's gRPC backoff is what we're waiting
  // for. The backend pushes a `cluster:connected` WS event the moment
  // an agent registers and the connector recovers, so the UI doesn't
  // need a manual Retry button — the empty state auto-heals.
  const isAwaitingAgent = isUnavailable && /no agent connected yet|waiting for agent to register/i.test(error.message || '')
  // When cluster is unreachable, don't pass stale data from the previous cluster
  const overview = isUnavailable ? undefined : rawOverview

  // Detect limited permissions
  const permissions = overview?.permissions
  const permittedCount = permissions
    ? Object.values(permissions).filter(Boolean).length
    : undefined
  const totalResources = permissions ? Object.keys(permissions).length : undefined
  const isLimited = permittedCount != null && totalResources != null && permittedCount < totalResources

  // The limited-access banner is dismissible, persisted per cluster + access
  // shape — so dismissing one cluster's banner doesn't hide it on another, and
  // it re-appears if this cluster's accessible-type count later changes (an RBAC
  // shift the operator should notice).
  const limitedDismissKey =
    isLimited && overview
      ? `kb-limited-access-dismissed:${overview.clusterUID ?? overview.clusterName ?? ''}:${permittedCount}/${totalResources}`
      : ''
  const [limitedBannerDismissed, setLimitedBannerDismissed] = useState(false)
  useEffect(() => {
    if (!limitedDismissKey) {
      setLimitedBannerDismissed(false)
      return
    }
    setLimitedBannerDismissed(localStorage.getItem(limitedDismissKey) === 'true')
  }, [limitedDismissKey])
  const dismissLimitedBanner = () => {
    try {
      if (limitedDismissKey) localStorage.setItem(limitedDismissKey, 'true')
    } catch {
      /* localStorage unavailable — dismiss for this session only */
    }
    setLimitedBannerDismissed(true)
  }

  // Copilot layout — when the panel is docked AND open, give the
  // main content column a matching margin-right so it reflows to
  // the left instead of being hidden underneath the panel. The
  // panel itself stays position:fixed; we just reserve the space.
  const { isOpen: copilotOpen, layout: copilotLayoutState } = useCopilot()
  const copilotReservation =
    copilotOpen && copilotLayoutState.layout.mode === 'docked'
      ? copilotLayoutState.layout.dockedWidth
      : 0

  return (
    <div className="flex h-screen w-screen bg-kb-bg overflow-hidden">
      <Sidebar overview={overview} collapsed={sidebarCollapsed} />
      <div className="flex-1 flex flex-col min-w-0">
        {/* Topbar stays full-width across the content column —
            even above the docked Copilot panel. The Copilot's
            top:52 avoids covering it, so the cluster switcher,
            search and theme toggle stay reachable.*/}
        <Topbar overview={overview} sidebarCollapsed={sidebarCollapsed} onToggleSidebar={toggleSidebar} />
        {isLimited && !limitedBannerDismissed && (
          <div className="px-4 py-1.5 bg-status-warn-dim border-b border-kb-border text-xs text-status-warn flex items-center gap-2 shrink-0">
            <ShieldAlert className="w-3.5 h-3.5 shrink-0" />
            <span className="flex-1">Limited access — showing {permittedCount} of {totalResources} resource types</span>
            <button
              onClick={dismissLimitedBanner}
              className="text-status-warn/70 hover:text-status-warn p-0.5 rounded hover:bg-status-warn/15 transition-colors shrink-0"
              title="Dismiss"
              aria-label="Dismiss limited-access banner"
            >
              <X className="w-3.5 h-3.5" />
            </button>
          </div>
        )}
        {/* Reservation lives on <main> instead of the column so the
            topbar above remains untouched — what was empty space to
            the right of the topbar now stays as topbar. */}
        <main
          className="flex-1 overflow-y-auto p-5 transition-[margin] duration-200 ease-out"
          style={{ marginRight: copilotReservation }}
        >
          {isSwitching ? (
            <div className="flex flex-col items-center justify-center h-full text-center">
              <Loader2 className="w-8 h-8 text-status-info animate-spin mb-4" />
              <h3 className="text-sm font-semibold text-kb-text-primary mb-1">Connecting to cluster</h3>
              <p className="text-xs text-kb-text-tertiary max-w-xs">
                Probing permissions and syncing resources...
              </p>
            </div>
          ) : isPlatformRoute ? (
            <Outlet />
          ) : noClusters ? (
            <div className="flex flex-col items-center justify-center h-full text-center">
              <div className="w-12 h-12 rounded-2xl bg-status-info-dim flex items-center justify-center mb-4">
                <Cable className="w-6 h-6 text-status-info" />
              </div>
              <h3 className="text-sm font-semibold text-kb-text-primary mb-1">No clusters configured</h3>
              <p className="text-xs text-kb-text-tertiary mb-5 max-w-sm">
                {isAdmin
                  ? 'Connect your first cluster to start monitoring. Install the KubeBolt agent inside the cluster, or import a kubeconfig.'
                  : 'No clusters have been added to KubeBolt yet. Ask an administrator to connect a cluster.'}
              </p>
              {isAdmin && (
                <button
                  type="button"
                  onClick={() => navigate('/clusters')}
                  className="px-3 py-1.5 text-xs font-medium bg-kb-accent text-white rounded-md hover:bg-kb-accent-bright transition-colors"
                >
                  Add cluster
                </button>
              )}
            </div>
          ) : isAwaitingAgent ? (
            <div className="flex flex-col items-center justify-center h-full text-center">
              <Loader2 className="w-8 h-8 text-status-info animate-spin mb-4" />
              <h3 className="text-sm font-semibold text-kb-text-primary mb-1">Waiting for agent to register</h3>
              <p className="text-xs text-kb-text-tertiary max-w-sm">
                The cluster is reachable through the KubeBolt agent. The agent's gRPC channel is reconnecting after the last backend restart — this view will refresh automatically as soon as it dials in (typically within a minute).
              </p>
            </div>
          ) : isUnavailable ? (
            <div className="flex flex-col items-center justify-center h-full text-center">
              <div className="w-12 h-12 rounded-2xl bg-status-warn-dim flex items-center justify-center mb-4">
                <Unplug className="w-6 h-6 text-status-warn" />
              </div>
              <h3 className="text-sm font-semibold text-kb-text-primary mb-1">Cluster unreachable</h3>
              <p className="text-xs text-kb-text-tertiary mb-5 max-w-xs">
                Could not connect to the selected cluster. Select a different cluster from the dropdown above, or retry once the cluster is back online.
              </p>
              <button
                type="button"
                onClick={() => refetch()}
                className="px-3 py-1.5 text-xs font-mono uppercase tracking-wider bg-kb-elevated text-kb-text-primary rounded-md border border-kb-border hover:border-kb-border-active transition-colors"
              >
                Retry
              </button>
              <p className="text-[10px] font-mono text-kb-text-tertiary mt-3 uppercase tracking-[0.06em]">
                Auto-retrying every 30s
              </p>
            </div>
          ) : (
            <Outlet />
          )}
        </main>
      </div>
      <CopilotToggle />
      <CopilotPanel />
      {showWizard && (
        <SetupWizard
          onDone={() => {
            queryClient.invalidateQueries({ queryKey: ['setup-status'] })
            queryClient.invalidateQueries({ queryKey: ['ui-config'] })
          }}
        />
      )}
    </div>
  )
}
