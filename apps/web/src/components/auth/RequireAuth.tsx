import { Navigate, useLocation } from 'react-router-dom'
import { useAuth } from '@/contexts/AuthContext'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'

interface RequireAuthProps {
  children: React.ReactNode
}

export function RequireAuth({ children }: RequireAuthProps) {
  const { isAuthEnabled, isAuthenticated, isLoading } = useAuth()
  const location = useLocation()

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-screen bg-kb-bg">
        <LoadingSpinner size="lg" />
      </div>
    )
  }

  // When auth is disabled, pass through
  if (!isAuthEnabled) {
    return <>{children}</>
  }

  if (!isAuthenticated) {
    // The query travels too: a deep link such as /admin/insights?tab=rules must
    // land on the same tab after signing in, not on the page's default.
    return <Navigate to="/login" state={{ from: location.pathname + location.search }} replace />
  }

  return <>{children}</>
}
