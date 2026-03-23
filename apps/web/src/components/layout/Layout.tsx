import { Outlet } from 'react-router-dom'
import { Unplug } from 'lucide-react'
import { Sidebar } from './Sidebar'
import { Topbar } from './Topbar'
import { useClusterOverview } from '@/hooks/useClusterOverview'
import { useWebSocket } from '@/hooks/useWebSocket'
import { ApiError } from '@/services/api'

const WS_RESOURCES = ['pods', 'nodes', 'deployments', 'services', 'events']

export function Layout() {
  const { data: rawOverview, error, refetch } = useClusterOverview()
  useWebSocket(WS_RESOURCES)

  const isUnavailable = error instanceof ApiError && error.status === 503
  // When cluster is unreachable, don't pass stale data from the previous cluster
  const overview = isUnavailable ? undefined : rawOverview

  return (
    <div className="flex h-screen w-screen bg-kb-bg overflow-hidden">
      <Sidebar overview={overview} />
      <div className="flex-1 flex flex-col min-w-0">
        <Topbar overview={overview} />
        <main className="flex-1 overflow-y-auto p-5">
          {isUnavailable ? (
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
    </div>
  )
}
