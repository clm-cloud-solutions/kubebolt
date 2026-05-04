import { useState, useRef, useEffect } from 'react'
import { NavLink, useNavigate } from 'react-router-dom'
import { Search, Server, ChevronDown, Check, Sun, Moon, Cable, ExternalLink, X, LogOut, KeyRound, Settings } from 'lucide-react'
import { SearchModal } from '@/components/shared/SearchModal'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { api } from '@/services/api'
import { useTheme } from '@/contexts/ThemeContext'
import { useAuth } from '@/contexts/AuthContext'
import { useCopilot } from '@/contexts/CopilotContext'
import { parseClusterDisplayName } from '@/utils/cluster'
import type { ClusterOverview, ClusterInfo } from '@/types/kubernetes'
import type { UserRole } from '@/types/auth'

interface TopbarProps {
  overview?: ClusterOverview
}

export function Topbar({ overview }: TopbarProps) {
  const [open, setOpen] = useState(false)
  const [searchOpen, setSearchOpen] = useState(false)
  const dropdownRef = useRef<HTMLDivElement>(null)
  const queryClient = useQueryClient()
  const { hasRole } = useAuth()
  const isAdmin = hasRole('admin')

  // Cluster management (add / rename / delete) lives on /clusters —
  // the dropdown only routes there. Keeps the switcher focused on its
  // primary job (switching) and avoids duplicating the wizard wiring
  // in two places. Visible to admins regardless of deployment mode
  // since both kubeconfig and agent paths work in either.

  // Cmd+K / Ctrl+K global shortcut
  useEffect(() => {
    function handleKey(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault()
        setSearchOpen(true)
      }
    }
    document.addEventListener('keydown', handleKey)
    return () => document.removeEventListener('keydown', handleKey)
  }, [])
  const navigate = useNavigate()
  const { theme, toggleTheme } = useTheme()
  const { clearHistory: clearCopilotHistory } = useCopilot()

  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    refetchInterval: 60_000,
  })

  const activeCluster = clusters?.find(c => c.active)
  const clusterName = activeCluster ? parseClusterDisplayName(activeCluster) : (overview?.clusterName || 'loading...')
  const nodeCount = overview?.nodes?.total ?? '-'
  const healthStatus = overview?.health?.status || 'unknown'
  const dotColor = healthStatus === 'healthy' ? 'bg-status-ok' : healthStatus === 'degraded' ? 'bg-status-warn' : 'bg-status-error'

  const switchMutation = useMutation({
    mutationKey: ['switch-cluster'],
    mutationFn: (context: string) => api.switchCluster(context),
    onMutate: (context: string) => {
      // Immediately mark the selected cluster as active — don't wait for the server round-trip
      queryClient.setQueryData(['clusters'], (old: ClusterInfo[] | undefined) =>
        old?.map(c => ({ ...c, active: c.context === context }))
      )
      queryClient.setQueryData(['cluster-overview'], undefined)
      setOpen(false)
    },
    onSuccess: () => {
      // Wipe the Copilot transcript — prior conversation referenced the
      // previous cluster's resources and would mislead the LLM on the new
      // one. The user re-engages with a fresh session on the new cluster.
      clearCopilotHistory()
      queryClient.invalidateQueries()
      navigate('/')
    },
    onError: () => {
      queryClient.invalidateQueries()
    },
  })

  // Close dropdown on outside click
  useEffect(() => {
    function handleClick(e: MouseEvent) {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    if (open) document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [open])

  // The dropdown is interactive when there's more than one cluster to
  // pick from OR when the admin has access to the manage link.
  const hasMultipleClusters = clusters && clusters.length > 1
  const dropdownInteractive = hasMultipleClusters || isAdmin

  return (
    <header className="h-[52px] bg-kb-surface/80 backdrop-blur-md border-b border-kb-border flex items-center justify-between px-4 shrink-0 relative z-[400]">
      {/* Left side */}
      <div className="flex items-center gap-4">
        {/* Cluster selector */}
        <div className="relative" ref={dropdownRef}>
          <button
            onClick={() => dropdownInteractive && setOpen(!open)}
            title={activeCluster?.context || clusterName}
            className={`flex items-center gap-2 px-2.5 py-1 rounded-md bg-kb-card border border-kb-border transition-colors ${
              dropdownInteractive ? 'cursor-pointer hover:border-kb-border-active' : 'cursor-default'
            }`}
          >
            <span className={`w-2 h-2 rounded-full ${dotColor} animate-pulse-live`} />
            <span className="text-xs font-mono text-kb-text-primary">{clusterName}</span>
            {dropdownInteractive && (
              <ChevronDown className={`w-3 h-3 text-kb-text-tertiary transition-transform ${open ? 'rotate-180' : ''}`} />
            )}
          </button>

          {/* Dropdown */}
          {open && clusters && (
            <div className="absolute top-full left-0 mt-1 w-72 bg-kb-card border border-kb-border rounded-lg shadow-xl z-50 py-1 overflow-hidden">
              <div className="px-3 py-1.5 text-[9px] font-mono uppercase tracking-[0.1em] text-kb-text-tertiary">
                Clusters ({clusters.length})
              </div>
              {clusters.map((cl) => (
                <button
                  key={cl.context}
                  onClick={() => !cl.active && switchMutation.mutate(cl.context)}
                  disabled={switchMutation.isPending}
                  title={cl.context}
                  className={`w-full text-left px-3 py-2 flex items-center gap-2 transition-colors ${
                    cl.active
                      ? 'bg-status-info-dim'
                      : 'hover:bg-kb-card-hover'
                  } ${switchMutation.isPending ? 'opacity-50' : ''}`}
                >
                  <span className={`w-2 h-2 rounded-full shrink-0 ${cl.active ? 'bg-status-ok' : 'bg-kb-text-tertiary'}`} />
                  <div className="flex-1 min-w-0">
                    <div className="text-xs text-kb-text-primary truncate">{parseClusterDisplayName(cl)}</div>
                    <div className="text-[10px] font-mono text-kb-text-tertiary truncate">{cl.server}</div>
                  </div>
                  {cl.active && <Check className="w-3.5 h-3.5 text-status-ok shrink-0" />}
                </button>
              ))}

              {isAdmin && (
                <>
                  <div className="border-t border-kb-border my-1" />
                  <button
                    type="button"
                    onClick={() => { setOpen(false); navigate('/clusters') }}
                    className="w-full text-left px-3 py-2 flex items-center gap-2 text-kb-text-secondary hover:bg-kb-card-hover hover:text-kb-text-primary transition-colors"
                  >
                    <Settings className="w-3.5 h-3.5 shrink-0" />
                    <span className="text-xs">Manage clusters</span>
                    <span className="ml-auto text-[10px] text-kb-text-tertiary">add / rename / delete</span>
                  </button>
                </>
              )}
            </div>
          )}
        </div>

        {/* View toggle */}
        <div className="flex rounded-md border border-kb-border overflow-hidden">
          <NavLink
            to="/"
            end
            className={({ isActive }) =>
              `px-3 py-1 text-[10px] font-mono uppercase tracking-[0.08em] transition-colors ${
                isActive ? 'bg-kb-elevated text-kb-text-primary' : 'bg-kb-card text-kb-text-tertiary hover:text-kb-text-secondary'
              }`
            }
          >
            Dashboard
          </NavLink>
          <NavLink
            to="/map"
            className={({ isActive }) =>
              `px-3 py-1 text-[10px] font-mono uppercase tracking-[0.08em] transition-colors border-l border-kb-border ${
                isActive ? 'bg-kb-elevated text-kb-text-primary' : 'bg-kb-card text-kb-text-tertiary hover:text-kb-text-secondary'
              }`
            }
          >
            Cluster Map
          </NavLink>
        </div>
      </div>

      {/* Right side */}
      <div className="flex items-center gap-3">
        {/* Search trigger */}
        <button
          onClick={() => setSearchOpen(true)}
          className="flex items-center gap-2 w-52 pl-3 pr-2 py-1.5 bg-kb-card border border-kb-border rounded-md text-xs text-kb-text-tertiary hover:border-kb-border-active transition-colors"
        >
          <Search className="w-3.5 h-3.5" />
          <span className="flex-1 text-left">Search...</span>
          <kbd className="px-1.5 py-0.5 rounded text-[9px] font-mono bg-kb-bg border border-kb-border">⌘K</kbd>
        </button>
        {searchOpen && <SearchModal onClose={() => setSearchOpen(false)} />}

        {/* Theme toggle */}
        <button
          type="button"
          onClick={toggleTheme}
          className="p-1.5 rounded-md text-kb-text-tertiary hover:text-kb-text-primary hover:bg-kb-card border border-kb-border transition-colors"
          title={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
        >
          {theme === 'dark' ? <Sun className="w-3.5 h-3.5" /> : <Moon className="w-3.5 h-3.5" />}
        </button>

        {/* Live indicator */}
        <div className="flex items-center gap-1.5 px-2 py-1 rounded-md bg-status-ok-dim">
          <span className="w-1.5 h-1.5 rounded-full bg-status-ok animate-pulse-live" />
          <span className="text-[10px] font-mono font-medium text-status-ok uppercase tracking-[0.08em]">Live</span>
        </div>

        {/* Active port-forwards */}
        <PortForwardIndicator />

        {/* Node count */}
        <div className="flex items-center gap-1.5 text-kb-text-secondary">
          <Server className="w-3.5 h-3.5" />
          <span className="text-xs font-mono">{nodeCount} nodes</span>
        </div>

        {/* User menu */}
        <UserMenu />
      </div>
    </header>
  )
}

function PortForwardIndicator() {
  const [open, setOpen] = useState(false)
  const dropdownRef = useRef<HTMLDivElement>(null)

  const { data: forwards } = useQuery({
    queryKey: ['port-forwards'],
    queryFn: api.listPortForwards,
    refetchInterval: 5_000,
  })

  const active = forwards?.filter(f => f.status === 'active') ?? []

  useEffect(() => {
    function handleClick(e: MouseEvent) {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    if (open) document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [open])

  async function stopForward(id: string) {
    try {
      await api.deletePortForward(id)
    } catch {}
  }

  if (active.length === 0) return null

  return (
    <div className="relative" ref={dropdownRef}>
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-1.5 px-2 py-1 rounded-md bg-status-info-dim text-status-info hover:bg-status-info/20 transition-colors"
      >
        <Cable className="w-3.5 h-3.5" />
        <span className="text-[10px] font-mono font-medium uppercase tracking-[0.08em]">
          {active.length} forward{active.length !== 1 ? 's' : ''}
        </span>
      </button>

      {open && (
        <div className="absolute top-full right-0 mt-1 w-80 bg-kb-card border border-kb-border rounded-lg shadow-xl z-50 py-1 overflow-hidden">
          <div className="px-3 py-1.5 text-[9px] font-mono uppercase tracking-[0.1em] text-kb-text-tertiary">
            Active Port Forwards
          </div>
          {active.map(pf => {
            const url = `${window.location.protocol}//${window.location.hostname}:${pf.localPort}`
            return (
              <div key={pf.id} className="px-3 py-2 flex items-center gap-2 hover:bg-kb-card-hover transition-colors">
                <div className="flex-1 min-w-0">
                  <div className="text-xs text-kb-text-primary truncate">{pf.pod}</div>
                  <div className="text-[10px] font-mono text-kb-text-tertiary">{pf.namespace} · port {pf.remotePort} → {pf.localPort}</div>
                </div>
                <a
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="p-1 rounded text-status-ok hover:bg-status-ok-dim transition-colors"
                  title="Open in new tab"
                >
                  <ExternalLink className="w-3.5 h-3.5" />
                </a>
                <button
                  onClick={() => stopForward(pf.id)}
                  className="p-1 rounded text-kb-text-tertiary hover:text-status-error hover:bg-status-error-dim transition-colors"
                  title="Stop forward"
                >
                  <X className="w-3.5 h-3.5" />
                </button>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

const ROLE_COLORS: Record<UserRole, string> = {
  admin: 'bg-status-error-dim text-status-error',
  editor: 'bg-status-info-dim text-status-info',
  viewer: 'bg-status-ok-dim text-status-ok',
}

function UserMenu() {
  const { user, isAuthEnabled, logout } = useAuth()
  const [open, setOpen] = useState(false)
  const [changingPw, setChangingPw] = useState(false)
  const [currentPw, setCurrentPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [pwError, setPwError] = useState<string | null>(null)
  const [pwSuccess, setPwSuccess] = useState(false)
  const [pwSaving, setPwSaving] = useState(false)
  const dropdownRef = useRef<HTMLDivElement>(null)
  const navigate = useNavigate()

  useEffect(() => {
    function handleClick(e: MouseEvent) {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        setOpen(false)
        setChangingPw(false)
      }
    }
    if (open) document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [open])

  if (!isAuthEnabled || !user) return null

  async function handleChangePassword(e: React.FormEvent) {
    e.preventDefault()
    setPwError(null)
    setPwSaving(true)
    try {
      await api.changePassword(currentPw, newPw)
      setPwSuccess(true)
      setCurrentPw('')
      setNewPw('')
      setTimeout(() => { setChangingPw(false); setPwSuccess(false) }, 1500)
    } catch (err) {
      setPwError(err instanceof Error ? err.message : 'Failed to change password')
    } finally {
      setPwSaving(false)
    }
  }

  async function handleLogout() {
    await logout()
    navigate('/login', { replace: true })
  }

  return (
    <div className="relative" ref={dropdownRef}>
      <button
        onClick={() => setOpen(!open)}
        className="w-7 h-7 rounded-full bg-kb-accent/20 flex items-center justify-center text-[11px] font-mono font-bold text-kb-accent hover:bg-kb-accent/30 transition-colors uppercase"
        title={`${user.username} (${user.role})`}
      >
        {user.username[0]}
      </button>

      {open && (
        <div className="absolute top-full right-0 mt-1 w-64 bg-kb-card border border-kb-border rounded-lg shadow-xl z-50 overflow-hidden">
          {/* User info */}
          <div className="px-4 py-3 border-b border-kb-border">
            <div className="flex items-center gap-2">
              <div className="w-8 h-8 rounded-full bg-kb-elevated flex items-center justify-center text-xs font-mono font-bold text-kb-text-secondary uppercase">
                {user.username[0]}
              </div>
              <div className="flex-1 min-w-0">
                <div className="text-xs font-medium text-kb-text-primary truncate">{user.name || user.username}</div>
                <div className="text-[10px] text-kb-text-tertiary truncate">{user.email || user.username}</div>
              </div>
              <span className={`px-1.5 py-0.5 rounded-full text-[9px] font-mono font-medium uppercase ${ROLE_COLORS[user.role]}`}>
                {user.role}
              </span>
            </div>
          </div>

          {/* Actions */}
          {!changingPw ? (
            <div className="py-1">
              <button
                onClick={() => setChangingPw(true)}
                className="w-full text-left px-4 py-2 flex items-center gap-2 text-xs text-kb-text-secondary hover:text-kb-text-primary hover:bg-kb-card-hover transition-colors"
              >
                <KeyRound className="w-3.5 h-3.5" />
                Change password
              </button>
              <button
                onClick={handleLogout}
                className="w-full text-left px-4 py-2 flex items-center gap-2 text-xs text-status-error hover:bg-status-error-dim transition-colors"
              >
                <LogOut className="w-3.5 h-3.5" />
                Sign out
              </button>
            </div>
          ) : (
            <form onSubmit={handleChangePassword} className="p-3 space-y-2">
              {pwError && <div className="px-2 py-1 rounded bg-status-error-dim text-status-error text-[10px]">{pwError}</div>}
              {pwSuccess && <div className="px-2 py-1 rounded bg-status-ok-dim text-status-ok text-[10px]">Password changed</div>}
              <input
                type="password"
                value={currentPw}
                onChange={e => setCurrentPw(e.target.value)}
                placeholder="Current password"
                required
                className="w-full px-2 py-1 text-xs bg-kb-bg border border-kb-border rounded text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent"
              />
              <input
                type="password"
                value={newPw}
                onChange={e => setNewPw(e.target.value)}
                placeholder="New password (min. 8 chars)"
                required
                minLength={8}
                className="w-full px-2 py-1 text-xs bg-kb-bg border border-kb-border rounded text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent"
              />
              <div className="flex gap-2">
                <button type="button" onClick={() => setChangingPw(false)} className="flex-1 px-2 py-1 text-[10px] border border-kb-border rounded text-kb-text-secondary hover:bg-kb-card-hover transition-colors">Cancel</button>
                <button type="submit" disabled={pwSaving || pwSuccess} className="flex-1 px-2 py-1 text-[10px] font-medium text-white bg-kb-accent rounded hover:bg-kb-accent/90 disabled:opacity-50 transition-colors">
                  {pwSaving ? 'Saving...' : 'Save'}
                </button>
              </div>
            </form>
          )}
        </div>
      )}
    </div>
  )
}
