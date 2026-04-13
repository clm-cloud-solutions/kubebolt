import { createContext, useContext, useState, useEffect, useCallback, type ReactNode } from 'react'
import { api, setAccessToken, clearAccessToken } from '@/services/api'
import type { AuthUser, UserRole } from '@/types/auth'

const ROLE_LEVELS: Record<UserRole, number> = { viewer: 1, editor: 2, admin: 3 }

interface AuthContextValue {
  isAuthEnabled: boolean
  isAuthenticated: boolean
  isLoading: boolean
  user: AuthUser | null
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  hasRole: (minRole: UserRole) => boolean
  refreshUser: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [isAuthEnabled, setIsAuthEnabled] = useState<boolean | null>(null)
  const [user, setUser] = useState<AuthUser | null>(null)
  const [isLoading, setIsLoading] = useState(true)

  // On mount: check if auth is enabled, then try silent refresh
  useEffect(() => {
    let cancelled = false

    async function init() {
      try {
        const config = await api.getAuthConfig()
        if (cancelled) return

        setIsAuthEnabled(config.enabled)

        if (!config.enabled) {
          setIsLoading(false)
          return
        }

        // Try silent refresh via httpOnly cookie
        const token = await api.refresh()
        if (cancelled) return

        if (token) {
          setAccessToken(token)
          const me = await api.getMe()
          if (!cancelled) setUser(me)
        }
      } catch {
        // Auth config fetch failed or refresh failed — user needs to login
      } finally {
        if (!cancelled) setIsLoading(false)
      }
    }

    init()
    return () => { cancelled = true }
  }, [])

  const login = useCallback(async (username: string, password: string) => {
    const response = await api.login(username, password)
    setAccessToken(response.accessToken)
    setUser(response.user)
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.logout()
    } catch {
      // Logout endpoint may fail if token expired, that's fine
    }
    clearAccessToken()
    setUser(null)
  }, [])

  const hasRole = useCallback((minRole: UserRole): boolean => {
    // When auth is disabled, everyone has admin access
    if (isAuthEnabled === false) return true
    if (!user) return false
    return ROLE_LEVELS[user.role] >= ROLE_LEVELS[minRole]
  }, [user, isAuthEnabled])

  const refreshUser = useCallback(async () => {
    try {
      const me = await api.getMe()
      setUser(me)
    } catch {
      // If getMe fails, user session is invalid
      clearAccessToken()
      setUser(null)
    }
  }, [])

  const value: AuthContextValue = {
    isAuthEnabled: isAuthEnabled ?? true,
    isAuthenticated: !!user,
    isLoading,
    user,
    login,
    logout,
    hasRole,
    refreshUser,
  }

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error('useAuth must be used within an AuthProvider')
  }
  return ctx
}
