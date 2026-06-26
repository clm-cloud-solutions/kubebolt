import { useState, type FormEvent } from 'react'
import { useNavigate, useLocation, Link } from 'react-router-dom'
import { Eye, EyeOff } from 'lucide-react'
import { AuthShell } from '@/components/shared/AuthShell'
import { useAuth } from '@/contexts/AuthContext'

export function LoginPage() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const { login, isAuthEnabled, isSignupEnabled, isAuthenticated } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const from = (location.state as { from?: string })?.from || '/'

  // If auth disabled or already logged in, redirect
  if (!isAuthEnabled || isAuthenticated) {
    navigate(from, { replace: true })
    return null
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setLoading(true)

    try {
      await login(username, password)
      navigate(from, { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Login failed')
    } finally {
      setLoading(false)
    }
  }

  const inputCls =
    'w-full px-3 py-2 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent transition-colors'

  return (
    <AuthShell title="Welcome back" subtitle="Sign in to your account.">
      <form onSubmit={handleSubmit} className="bg-kb-card border border-kb-border rounded-xl p-6 shadow-sm space-y-4">
        {error && (
          <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">{error}</div>
        )}

        <div className="space-y-1.5">
          <label htmlFor="username" className="block text-xs font-medium text-kb-text-secondary">
            Username
          </label>
          <input
            id="username"
            type="text"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            autoFocus
            required
            className={inputCls}
            placeholder="admin"
          />
        </div>

        <div className="space-y-1.5">
          <label htmlFor="password" className="block text-xs font-medium text-kb-text-secondary">
            Password
          </label>
          <div className="relative">
            <input
              id="password"
              type={showPassword ? 'text' : 'password'}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
              required
              className={`${inputCls} pr-10`}
              placeholder="Password"
            />
            <button
              type="button"
              onClick={() => setShowPassword(!showPassword)}
              className="absolute right-2.5 top-1/2 -translate-y-1/2 text-kb-text-tertiary hover:text-kb-text-secondary transition-colors"
            >
              {showPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
            </button>
          </div>
        </div>

        <button
          type="submit"
          disabled={loading || !username || !password}
          className="w-full py-2 px-4 text-sm font-medium rounded-lg bg-kb-accent text-white hover:-translate-y-px hover:shadow-[0_8px_24px_-8px_rgba(29,189,125,0.5)] disabled:opacity-50 disabled:cursor-not-allowed disabled:hover:translate-y-0 disabled:hover:shadow-none transition-all"
        >
          {loading ? 'Signing in...' : 'Sign in'}
        </button>

        {/* Self-service signup — only when the backend reports it's available
            (multi-org / EE edition). Inert in OSS, where the flag is absent. */}
        {isSignupEnabled && (
          <p className="text-center text-xs text-kb-text-tertiary pt-1">
            New to KubeBolt?{' '}
            <Link to="/signup" className="text-kb-accent hover:underline">
              Create an account
            </Link>
          </p>
        )}
      </form>
    </AuthShell>
  )
}
