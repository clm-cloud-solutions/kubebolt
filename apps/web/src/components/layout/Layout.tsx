import { Outlet } from 'react-router-dom'
import { useIsMutating } from '@tanstack/react-query'
import { Unplug, ShieldAlert, Loader2 } from 'lucide-react'
import { Sidebar } from './Sidebar'
import { Topbar } from './Topbar'
import { useClusterOverview } from '@/hooks/useClusterOverview'
import { useWebSocket } from '@/hooks/useWebSocket'
import { ApiError } from '@/services/api'
import { CopilotPanel } from '@/components/copilot/CopilotPanel'
import { CopilotToggle } from '@/components/copilot/CopilotToggle'
import { useCopilot } from '@/contexts/CopilotContext'

const WS_RESOURCES = ['pods', 'nodes', 'deployments', 'services', 'events']

export function Layout() {
  const { data: rawOverview, error, refetch } = useClusterOverview()
  const isSwitching = useIsMutating({ mutationKey: ['switch-cluster'] }) > 0
  useWebSocket(WS_RESOURCES)

  const isUnavailable = error instanceof ApiError && error.status === 503
  // When cluster is unreachable, don't pass stale data from the previous cluster
  const overview = isUnavailable ? undefined : rawOverview

  // Detect limited permissions
  const permissions = overview?.permissions
  const permittedCount = permissions
    ? Object.values(permissions).filter(Boolean).length
    : undefined
  const totalResources = permissions ? Object.keys(permissions).length : undefined
  const isLimited = permittedCount != null && totalResources != null && permittedCount < totalResources

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
      <Sidebar overview={overview} />
      <div className="flex-1 flex flex-col min-w-0">
        {/* Topbar stays full-width across the content column —
            even above the docked Copilot panel. The Copilot's
            top:52 avoids covering it, so the cluster switcher,
            search and theme toggle stay reachable.*/}
        <Topbar overview={overview} />
        {isLimited && (
          <div className="px-4 py-1.5 bg-status-warn-dim border-b border-kb-border text-xs text-status-warn flex items-center gap-2 shrink-0">
            <ShieldAlert className="w-3.5 h-3.5" />
            <span>Limited access — showing {permittedCount} of {totalResources} resource types</span>
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
    </div>
  )
}
