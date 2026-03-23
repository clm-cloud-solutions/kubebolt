import { useState, useRef, useEffect } from 'react'
import { NavLink } from 'react-router-dom'
import { Search, Server, ChevronDown, Check, Sun, Moon } from 'lucide-react'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { api } from '@/services/api'
import { useTheme } from '@/contexts/ThemeContext'
import type { ClusterOverview, ClusterInfo } from '@/types/kubernetes'

interface TopbarProps {
  overview?: ClusterOverview
}

export function Topbar({ overview }: TopbarProps) {
  const [open, setOpen] = useState(false)
  const dropdownRef = useRef<HTMLDivElement>(null)
  const queryClient = useQueryClient()
  const { theme, toggleTheme } = useTheme()

  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    refetchInterval: 60_000,
  })

  const activeCluster = clusters?.find(c => c.active)
  const clusterName = overview?.clusterName || activeCluster?.name || activeCluster?.context || 'loading...'
  const nodeCount = overview?.nodes?.total ?? '-'
  const healthStatus = overview?.health?.status || 'unknown'
  const dotColor = healthStatus === 'healthy' ? 'bg-status-ok' : healthStatus === 'degraded' ? 'bg-status-warn' : 'bg-status-error'

  const switchMutation = useMutation({
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
      queryClient.invalidateQueries()
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

  const hasMultipleClusters = clusters && clusters.length > 1

  return (
    <header className="h-[52px] bg-kb-surface/80 backdrop-blur-md border-b border-kb-border flex items-center justify-between px-4 shrink-0 relative z-[200]">
      {/* Left side */}
      <div className="flex items-center gap-4">
        {/* Cluster selector */}
        <div className="relative" ref={dropdownRef}>
          <button
            onClick={() => hasMultipleClusters && setOpen(!open)}
            className={`flex items-center gap-2 px-2.5 py-1 rounded-md bg-kb-card border border-kb-border transition-colors ${
              hasMultipleClusters ? 'cursor-pointer hover:border-kb-border-active' : 'cursor-default'
            }`}
          >
            <span className={`w-2 h-2 rounded-full ${dotColor} animate-pulse-live`} />
            <span className="text-xs font-mono text-kb-text-primary">{clusterName}</span>
            {hasMultipleClusters && (
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
                  className={`w-full text-left px-3 py-2 flex items-center gap-2 transition-colors ${
                    cl.active
                      ? 'bg-status-info-dim'
                      : 'hover:bg-kb-card-hover'
                  } ${switchMutation.isPending ? 'opacity-50' : ''}`}
                >
                  <span className={`w-2 h-2 rounded-full shrink-0 ${cl.active ? 'bg-status-ok' : 'bg-kb-text-tertiary'}`} />
                  <div className="flex-1 min-w-0">
                    <div className="text-xs text-kb-text-primary truncate">{cl.context}</div>
                    <div className="text-[10px] font-mono text-kb-text-tertiary truncate">{cl.server}</div>
                  </div>
                  {cl.active && <Check className="w-3.5 h-3.5 text-status-ok shrink-0" />}
                </button>
              ))}
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
        {/* Search */}
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-kb-text-tertiary" />
          <input
            type="text"
            placeholder="Search..."
            className="w-52 pl-8 pr-12 py-1.5 bg-kb-card border border-kb-border rounded-md text-xs text-kb-text-primary placeholder-kb-text-tertiary outline-none focus:border-kb-border-active transition-colors"
          />
          <kbd className="absolute right-2.5 top-1/2 -translate-y-1/2 px-1.5 py-0.5 rounded text-[9px] font-mono text-kb-text-tertiary bg-kb-bg border border-kb-border">
            ⌘K
          </kbd>
        </div>

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

        {/* Node count */}
        <div className="flex items-center gap-1.5 text-kb-text-secondary">
          <Server className="w-3.5 h-3.5" />
          <span className="text-xs font-mono">{nodeCount} nodes</span>
        </div>
      </div>
    </header>
  )
}
