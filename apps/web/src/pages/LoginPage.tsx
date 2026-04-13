import { useState, type FormEvent } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { Zap, Eye, EyeOff } from 'lucide-react'
import { useAuth } from '@/contexts/AuthContext'

export function LoginPage() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const { login, isAuthEnabled, isAuthenticated } = useAuth()
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

  return (
    <div className="min-h-screen bg-kb-bg flex items-center justify-center p-4">
      <div className="w-full max-w-sm">
        {/* Logo */}
        <div className="flex flex-col items-center mb-8">
          <div className="w-12 h-12 rounded-xl bg-kb-accent-light flex items-center justify-center mb-3">
            <Zap className="w-7 h-7 text-kb-accent" />
          </div>
          <h1 className="text-xl font-semibold text-kb-text-primary">KubeBolt</h1>
          <p className="text-xs text-kb-text-tertiary mt-1">Sign in to your account</p>
        </div>

        {/* Form */}
        <form onSubmit={handleSubmit} className="bg-kb-card border border-kb-border rounded-xl p-6 shadow-sm space-y-4">
          {error && (
            <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
              {error}
            </div>
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
              className="w-full px-3 py-2 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent transition-colors"
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
                className="w-full px-3 py-2 pr-10 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent transition-colors"
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
            className="w-full py-2 px-4 text-sm font-medium rounded-lg bg-kb-accent text-white hover:bg-kb-accent/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            {loading ? 'Signing in...' : 'Sign in'}
          </button>
        </form>

        <p className="text-center text-[10px] text-kb-text-tertiary mt-6 font-mono">
          KubeBolt v0.1.0 beta
        </p>
      </div>
    </div>
  )
}
