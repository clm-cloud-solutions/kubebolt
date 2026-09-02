import { createContext, useContext, useState, useEffect, useCallback, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api, setAccessToken, clearAccessToken, ApiError } from '@/services/api'

/**
 * sessionIsGone decides whether a failed /auth/me means the session ended.
 *
 * ONLY a definitive 401 does. fetchWithAuth has already tried a silent refresh
 * and retried once, so a 401 arriving here means there was no valid refresh
 * cookie either — the session really is over.
 *
 * Everything else says nothing about the session: a 503 while a cluster is
 * mid-reconnect, a 5xx, the network dropping for a second. Treating those as a
 * logout is the bug this replaces — refreshUser runs on every tab focus
 * (VerifyEmail's onFocus), so a single blip bounced the operator to /login,
 * a reload restored them from the httpOnly cookie, and the next focus bounced
 * them again. Reproduced 2026-08-20 against a degraded cluster.
 */
export function sessionIsGone(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401
}
import type { AuthUser, UserRole } from '@/types/auth'

const ROLE_LEVELS: Record<UserRole, number> = { viewer: 1, editor: 2, admin: 3 }

interface AuthContextValue {
  isAuthEnabled: boolean
  /** True only on the multi-org edition — gates the self-service signup link. */
  isSignupEnabled: boolean
  isAuthenticated: boolean
  isLoading: boolean
  user: AuthUser | null
  login: (username: string, password: string) => Promise<void>
  signup: (data: { orgName: string; name: string; email: string; password: string }) => Promise<void>
  logout: () => Promise<void>
  hasRole: (minRole: UserRole) => boolean
  refreshUser: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const [isAuthEnabled, setIsAuthEnabled] = useState<boolean | null>(null)
  const [isSignupEnabled, setIsSignupEnabled] = useState(false)
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
        setIsSignupEnabled(!!config.signupEnabled)

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
    // The login payload carries the bare user; /auth/me adds the org+team
    // context (the hierarchy the topbar + teams page render). Fetch it now so
    // those surfaces are populated immediately rather than after the next
    // refresh. Fall back to the login user if the follow-up call fails.
    try {
      setUser(await api.getMe())
    } catch {
      setUser(response.user)
    }
  }, [])

  const signup = useCallback(
    async (data: { orgName: string; name: string; email: string; password: string }) => {
      const response = await api.signup(data)
      setAccessToken(response.accessToken)
      // Mirror login: the signup payload carries the bare user; /auth/me adds
      // the org+team context the topbar renders. Fetch it now, falling back to
      // the signup user if the follow-up call fails.
      try {
        setUser(await api.getMe())
      } catch {
        setUser(response.user)
      }
    },
    [],
  )

  const logout = useCallback(async () => {
    try {
      await api.logout()
    } catch {
      // Logout endpoint may fail if token expired, that's fine
    }
    clearAccessToken()
    setUser(null)
    // Drop every cached server query so the next user who logs in on this
    // same browser never sees the previous user's data (e.g. Kobi's
    // conversation list). Per-user in-memory state is reset separately by
    // the contexts that watch the active user id.
    queryClient.clear()
  }, [queryClient])

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
    } catch (err) {
      // Mirrors the boot path above, which already separates "definitively no
      // session" from "transient error — keep the optimistic cache". See
      // sessionIsGone for why only a 401 counts.
      if (sessionIsGone(err)) {
        clearAccessToken()
        setUser(null)
        return
      }
      // Kept, not swallowed: this path is invisible from the UI, and the bug it
      // replaced was invisible for exactly that reason.
      console.warn('refreshUser failed; keeping the session', err)
    }
  }, [])

  const value: AuthContextValue = {
    isAuthEnabled: isAuthEnabled ?? true,
    isSignupEnabled,
    isAuthenticated: !!user,
    isLoading,
    user,
    login,
    signup,
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
