import { useCallback, useEffect, useState } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useIsMutating, useQuery, useQueryClient } from '@tanstack/react-query'
import { Unplug, ShieldAlert, Loader2, Cable, X, Info } from 'lucide-react'
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

// Friendly names for optional-CRD permission keys, so the "not detected" note
// reads in product terms. Both Cilium CRDs collapse to one "Cilium".
const ABSENT_INTEGRATION_NAMES: Record<string, string> = {
  certificates: 'cert-manager',
  argocdapps: 'ArgoCD',
  vpas: 'VPA',
  ciliumnetworkpolicies: 'Cilium',
  ciliumclusterwidenetworkpolicies: 'Cilium',
}

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
  // Metrics-only cluster: the agent ships metrics but advertises no kube-proxy, so it
  // has no live-resource connector. We render the metrics dashboards (VM-direct) and
  // degrade the resource views, rather than treating the overview's "monitored-only"
  // 503 as "cluster unreachable".
  const isMetricsOnly = (clusters ?? []).find(c => c.active)?.mode === 'metrics-only'
  // Routes that don't depend on a connected cluster. /clusters owns the
  // add-cluster wizard (must render so the user has a way out of the
  // empty state), and /admin /settings are platform-level pages whose
  // backend endpoints are cluster-agnostic. For these we bypass BOTH
  // the no-clusters CTA and the cluster-unreachable error page.
  // /account (EE org plan + usage) is also cluster-agnostic — a brand-new org
  // has no clusters yet but must still reach its plan page. Empty path in OSS.
  const PLATFORM_ROUTE_PREFIXES = ['/clusters', '/admin', '/settings', '/account']
  const isPlatformRoute = PLATFORM_ROUTE_PREFIXES.some(p =>
    location.pathname === p || location.pathname.startsWith(p + '/')
  )

  // A metrics-only cluster's resource endpoints 503 by design (no connector) — that is
  // NOT "unreachable", so don't route it to the error page; it renders the dashboards.
  const isUnavailable = error instanceof ApiError && error.status === 503 && !isMetricsOnly
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
  // Optional CRDs the cluster lacks (absentResources) are CanList=false but NOT a
  // permission restriction — exclude them from the banner count.
  const permissions = overview?.permissions
  const absent = new Set(overview?.absentResources ?? [])
  const permittedCount = permissions
    ? Object.entries(permissions).filter(([k, v]) => !absent.has(k) && Boolean(v)).length
    : undefined
  const totalResources = permissions
    ? Object.keys(permissions).filter((k) => !absent.has(k)).length
    : undefined
  const isLimited = permittedCount != null && totalResources != null && permittedCount < totalResources

  // Case B: optional integrations RBAC grants but the cluster lacks. Informational,
  // not a problem. Map keys to friendly names, deduped (Cilium has two CRDs).
  const absentIntegrations = [
    ...new Set((overview?.absentResources ?? []).map((k) => ABSENT_INTEGRATION_NAMES[k] ?? k)),
  ]

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

  // Separate dismiss state for the (neutral) optional-integrations note — keyed
  // per cluster + the set absent, so it re-appears if that set changes.
  const absentDismissKey =
    absentIntegrations.length > 0 && overview
      ? `kb-absent-integrations-dismissed:${overview.clusterUID ?? overview.clusterName ?? ''}:${absentIntegrations.slice().sort().join(',')}`
      : ''
  const [absentNoteDismissed, setAbsentNoteDismissed] = useState(false)
  useEffect(() => {
    if (!absentDismissKey) {
      setAbsentNoteDismissed(false)
      return
    }
    setAbsentNoteDismissed(localStorage.getItem(absentDismissKey) === 'true')
  }, [absentDismissKey])
  const dismissAbsentNote = () => {
    try {
      if (absentDismissKey) localStorage.setItem(absentDismissKey, 'true')
    } catch {
      /* localStorage unavailable — dismiss for this session only */
    }
    setAbsentNoteDismissed(true)
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
            <span className="flex-1">
              Limited access — {permittedCount} of {totalResources} resource types restricted by RBAC. The agent&apos;s ClusterRole may have been narrowed.
            </span>
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
        {/* Case B — optional integrations not installed. Neutral + dismissible,
            NOT the amber RBAC banner: a missing optional CRD isn't a problem. */}
        {absentIntegrations.length > 0 && !absentNoteDismissed && (
          <div className="px-4 py-1.5 bg-kb-card border-b border-kb-border text-xs text-kb-text-tertiary flex items-center gap-2 shrink-0">
            <Info className="w-3.5 h-3.5 shrink-0" />
            <span className="flex-1">Optional integrations not detected: {absentIntegrations.join(', ')}</span>
            <button
              onClick={dismissAbsentNote}
              className="text-kb-text-tertiary/70 hover:text-kb-text-secondary p-0.5 rounded hover:bg-kb-card-hover transition-colors shrink-0"
              title="Dismiss"
              aria-label="Dismiss optional-integrations note"
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
