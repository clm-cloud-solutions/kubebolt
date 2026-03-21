import { Outlet } from 'react-router-dom'
import { Sidebar } from './Sidebar'
import { Topbar } from './Topbar'
import { useClusterOverview } from '@/hooks/useClusterOverview'
import { useWebSocket } from '@/hooks/useWebSocket'

const WS_RESOURCES = ['pods', 'nodes', 'deployments', 'services', 'events']

export function Layout() {
  const { data: overview } = useClusterOverview()
  useWebSocket(WS_RESOURCES)

  return (
    <div className="flex h-screen w-screen bg-kb-bg overflow-hidden">
      <Sidebar overview={overview} />
      <div className="flex-1 flex flex-col min-w-0">
        <Topbar overview={overview} />
        <main className="flex-1 overflow-y-auto p-5">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
