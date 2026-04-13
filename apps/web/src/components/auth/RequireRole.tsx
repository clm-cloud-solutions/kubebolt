import { useAuth } from '@/contexts/AuthContext'
import { PermissionDenied } from '@/components/shared/PermissionDenied'
import type { UserRole } from '@/types/auth'

interface RequireRoleProps {
  role: UserRole
  children: React.ReactNode
}

export function RequireRole({ role, children }: RequireRoleProps) {
  const { hasRole } = useAuth()

  if (!hasRole(role)) {
    return <PermissionDenied />
  }

  return <>{children}</>
}
