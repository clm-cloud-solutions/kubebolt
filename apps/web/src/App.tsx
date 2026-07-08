import { Component, type ReactNode } from 'react'
import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from '@/contexts/ThemeContext'
import { RefreshProvider } from '@/contexts/RefreshContext'
import { AuthProvider } from '@/contexts/AuthContext'
import { ApiError } from '@/services/api'
import { Layout } from '@/components/layout/Layout'
import { ViewportGate } from '@/components/layout/ViewportGate'
import { RequireAuth } from '@/components/auth/RequireAuth'
import { RequireRole } from '@/components/auth/RequireRole'
import { OverviewPage } from '@/components/dashboard/OverviewPage'
import { CapacityPage } from '@/components/dashboard/CapacityPage'
import { ReliabilityPage } from '@/components/dashboard/ReliabilityPage'
import { ResourceListPage } from '@/components/resources/ResourceListPage'
import { NodesPage } from '@/components/resources/NodesPage'
import { EventsPage } from '@/components/resources/EventsPage'
import { NamespacesPage } from '@/components/resources/NamespacesPage'
import { RBACPage } from '@/components/resources/RBACPage'
import { ResourceDetailPage } from '@/components/resources/ResourceDetailPage'
import { InsightsList } from '@/components/insights/InsightsList'
import { ClusterMap } from '@/components/map/ClusterMap'
import { ClustersPage } from '@/pages/ClustersPage'
import { ApplicationsPage } from '@/pages/ApplicationsPage'
import { HelmReleaseDetailPage } from '@/pages/HelmReleaseDetailPage'
import { LoginPage } from '@/pages/LoginPage'
import { APITokensPage } from '@/pages/admin/APITokensPage'
import { AccessHub } from '@/pages/admin/AccessHub'
import { AgentsHub } from '@/pages/admin/AgentsHub'
import { AIHub } from '@/pages/admin/AIHub'
import { SystemHub } from '@/pages/admin/SystemHub'
import { CopilotProvider } from '@/contexts/CopilotContext'
import { eeRoutes, eePublicRoutes } from '@/ee/registry'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      retry: (failureCount, error) => {
        // Never retry cluster-unavailable, permission, or auth errors
        if (error instanceof ApiError && (error.status === 503 || error.status === 403 || error.status === 401)) return false
        return failureCount < 2
      },
      refetchOnWindowFocus: false,
    },
  },
})

interface ErrorBoundaryState {
  hasError: boolean
  error: Error | null
}

class ErrorBoundary extends Component<{ children: ReactNode }, ErrorBoundaryState> {
  state: ErrorBoundaryState = { hasError: false, error: null }

  static getDerivedStateFromError(error: Error) {
    return { hasError: true, error }
  }

  render() {
    if (this.state.hasError) {
      return (
        <div className="flex items-center justify-center min-h-screen bg-kb-bg text-white">
          <div className="text-center p-8">
            <h1 className="text-xl font-semibold mb-2">Something went wrong</h1>
            <p className="text-sm text-kb-text-tertiary mb-4">{this.state.error?.message}</p>
            <button
              onClick={() => this.setState({ hasError: false, error: null })}
              className="px-4 py-2 text-sm bg-kb-card border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors"
            >
              Try again
            </button>
          </div>
        </div>
      )
    }
    return this.props.children
  }
}

export default function App() {
  return (
    <ThemeProvider>
      <ErrorBoundary>
        <QueryClientProvider client={queryClient}>
        <RefreshProvider>
        <BrowserRouter>
        <AuthProvider>
        <CopilotProvider>
          <Routes>
            {/* Login page — outside Layout */}
            <Route path="/login" element={<LoginPage />} />

            {/* EE public routes — outside Layout, alongside /login (signup,
                onboarding). Empty in OSS via @/ee/registry. */}
            {eePublicRoutes}

            {/* All app routes — require auth when enabled */}
            <Route element={<RequireAuth><ViewportGate><Layout /></ViewportGate></RequireAuth>}>
              <Route path="/" element={<OverviewPage />} />
              <Route path="/capacity" element={<CapacityPage />} />
              <Route path="/reliability" element={<ReliabilityPage />} />
              <Route path="/insights" element={<InsightsList />} />
              <Route path="/applications" element={<ApplicationsPage />} />
              <Route path="/applications/:namespace/:name" element={<HelmReleaseDetailPage />} />
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
              <Route path="/networkpolicies" element={<ResourceListPage resourceType="networkpolicies" />} />
              <Route path="/ciliumnetworkpolicies" element={<ResourceListPage resourceType="ciliumnetworkpolicies" />} />
              <Route path="/ciliumclusterwidenetworkpolicies" element={<ResourceListPage resourceType="ciliumclusterwidenetworkpolicies" />} />
              <Route path="/pdbs" element={<ResourceListPage resourceType="pdbs" />} />
              <Route path="/certificates" element={<ResourceListPage resourceType="certificates" />} />
              <Route path="/argocdapps" element={<ResourceListPage resourceType="argocdapps" />} />
              <Route path="/vpas" element={<ResourceListPage resourceType="vpas" />} />
              <Route path="/serviceaccounts" element={<ResourceListPage resourceType="serviceaccounts" />} />
              <Route path="/gateways" element={<ResourceListPage resourceType="gateways" />} />
              <Route path="/httproutes" element={<ResourceListPage resourceType="httproutes" />} />
              <Route path="/endpoints" element={<ResourceListPage resourceType="endpoints" />} />
              <Route path="/pvcs" element={<ResourceListPage resourceType="pvcs" />} />
              <Route path="/pvs" element={<ResourceListPage resourceType="pvs" />} />
              <Route path="/storageclasses" element={<ResourceListPage resourceType="storageclasses" />} />
              <Route path="/configmaps" element={<ResourceListPage resourceType="configmaps" />} />
              <Route path="/secrets" element={<ResourceListPage resourceType="secrets" />} />
              <Route path="/hpas" element={<ResourceListPage resourceType="hpas" />} />
              <Route path="/:type/:namespace/:name" element={<ResourceDetailPage />} />
              {/* EE extension point — edition-specific routes (e.g. Autopilot).
                  Empty in OSS via @/ee/registry; the Enterprise build overrides
                  that module to inject its routes. Keeps App.tsx identical OSS↔EE. */}
              {eeRoutes}
              <Route path="/clusters" element={<ClustersPage />} />
              <Route path="/namespaces" element={<NamespacesPage />} />
              <Route path="/events" element={<EventsPage />} />
              <Route path="/rbac" element={<RBACPage />} />

              {/* Admin — grouped into domain hubs (Access / Agents & Ingest /
                  AI / System) so the area reads by concern instead of a flat
                  list. Each hub is a tabbed shell; the active tab is URL-driven
                  via ?tab=. API Tokens stays standalone. */}
              <Route path="/admin/access" element={<RequireRole role="admin"><AccessHub /></RequireRole>} />
              <Route path="/admin/agents" element={<RequireRole role="admin"><AgentsHub /></RequireRole>} />
              <Route path="/admin/ai" element={<RequireRole role="admin"><AIHub /></RequireRole>} />
              <Route path="/admin/system" element={<RequireRole role="admin"><SystemHub /></RequireRole>} />
              <Route path="/admin/api-tokens" element={<RequireRole role="admin"><APITokensPage /></RequireRole>} />
            </Route>
          </Routes>
        </CopilotProvider>
        </AuthProvider>
        </BrowserRouter>
        </RefreshProvider>
        </QueryClientProvider>
      </ErrorBoundary>
    </ThemeProvider>
  )
}
