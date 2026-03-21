import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Layout } from '@/components/layout/Layout'
import { OverviewPage } from '@/components/dashboard/OverviewPage'
import { ResourceListPage } from '@/components/resources/ResourceListPage'
import { NodesPage } from '@/components/resources/NodesPage'
import { EventsPage } from '@/components/resources/EventsPage'
import { NamespacesPage } from '@/components/resources/NamespacesPage'
import { RBACPage } from '@/components/resources/RBACPage'
import { SettingsPage } from '@/components/resources/SettingsPage'
import { ClusterMap } from '@/components/map/ClusterMap'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      retry: 2,
      refetchOnWindowFocus: false,
    },
  },
})

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <Routes>
          <Route element={<Layout />}>
            <Route path="/" element={<OverviewPage />} />
            <Route path="/map" element={<ClusterMap />} />
            <Route path="/pods" element={<ResourceListPage resourceType="pods" />} />
            <Route path="/nodes" element={<NodesPage />} />
            <Route path="/deployments" element={<ResourceListPage resourceType="deployments" />} />
            <Route path="/statefulsets" element={<ResourceListPage resourceType="statefulsets" />} />
            <Route path="/daemonsets" element={<ResourceListPage resourceType="daemonsets" />} />
            <Route path="/jobs" element={<ResourceListPage resourceType="jobs" />} />
            <Route path="/cronjobs" element={<ResourceListPage resourceType="cronjobs" />} />
            <Route path="/services" element={<ResourceListPage resourceType="services" />} />
            <Route path="/ingresses" element={<ResourceListPage resourceType="ingresses" />} />
            <Route path="/gateways" element={<ResourceListPage resourceType="gateways" />} />
            <Route path="/httproutes" element={<ResourceListPage resourceType="httproutes" />} />
            <Route path="/endpoints" element={<ResourceListPage resourceType="endpoints" />} />
            <Route path="/pvcs" element={<ResourceListPage resourceType="pvcs" />} />
            <Route path="/pvs" element={<ResourceListPage resourceType="pvs" />} />
            <Route path="/storageclasses" element={<ResourceListPage resourceType="storageclasses" />} />
            <Route path="/configmaps" element={<ResourceListPage resourceType="configmaps" />} />
            <Route path="/secrets" element={<ResourceListPage resourceType="secrets" />} />
            <Route path="/hpas" element={<ResourceListPage resourceType="hpas" />} />
            <Route path="/namespaces" element={<NamespacesPage />} />
            <Route path="/events" element={<EventsPage />} />
            <Route path="/rbac" element={<RBACPage />} />
            <Route path="/settings" element={<SettingsPage />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  )
}
