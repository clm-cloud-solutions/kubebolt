import { useClusterOverview } from '@/hooks/useClusterOverview'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { useTheme } from '@/contexts/ThemeContext'
import { Server, Sun, Moon } from 'lucide-react'
import { KubeBoltLogo } from '@/components/shared/KubeBoltLogo'

export function SettingsPage() {
  const { data: overview, isLoading } = useClusterOverview()
  const { theme, toggleTheme } = useTheme()

  if (isLoading) return <LoadingSpinner />

  return (
    <div>
      <h1 className="text-lg font-semibold text-kb-text-primary mb-4">Settings</h1>

      {/* Appearance */}
      <div className="bg-kb-card border border-kb-border rounded-[10px] p-5 mb-4">
        <div className="flex items-center gap-2 mb-4">
          <Sun className="w-4 h-4 text-status-info" />
          <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
            Appearance
          </span>
        </div>
        <div className="flex items-center justify-between">
          <div>
            <div className="text-sm font-medium text-kb-text-primary">Theme</div>
            <div className="text-xs text-kb-text-tertiary mt-0.5">
              {theme === 'dark' ? 'Dark mode' : 'Light mode'}
            </div>
          </div>
          <button
            type="button"
            onClick={toggleTheme}
            className="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-kb-elevated border border-kb-border text-xs text-kb-text-primary hover:border-kb-border-active transition-colors"
          >
            {theme === 'dark' ? (
              <><Sun className="w-3.5 h-3.5" /> Switch to light</>
            ) : (
              <><Moon className="w-3.5 h-3.5" /> Switch to dark</>
            )}
          </button>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-4">
        {/* Cluster Connection */}
        <div className="bg-kb-card border border-kb-border rounded-[10px] p-5">
          <div className="flex items-center gap-2 mb-4">
            <Server className="w-4 h-4 text-status-info" />
            <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
              Cluster Connection
            </span>
          </div>

          <div className="space-y-3">
            <div>
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-[0.06em] mb-1">Cluster Name</div>
              <div className="text-sm font-mono text-kb-text-primary">{overview?.clusterName || '-'}</div>
            </div>
            <div>
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-[0.06em] mb-1">Kubernetes Version</div>
              <div className="text-sm font-mono text-kb-text-primary">{overview?.kubernetesVersion || '-'}</div>
            </div>
            <div>
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-[0.06em] mb-1">Platform</div>
              <div className="text-sm font-mono text-kb-text-primary">{overview?.platform || '-'}</div>
            </div>
            <div>
              <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-[0.06em] mb-1">Status</div>
              <div className="flex items-center gap-2">
                <span className="w-2 h-2 rounded-full bg-status-ok animate-pulse-live" />
                <span className="text-sm font-mono text-status-ok">Connected</span>
              </div>
            </div>
          </div>
        </div>

        {/* KubeBolt Agent */}
        <div className="bg-kb-card border border-kb-border rounded-[10px] p-5">
          <div className="flex items-center gap-2 mb-4">
            <KubeBoltLogo className="w-4 h-4 text-status-warn" />
            <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
              KubeBolt Agent
            </span>
          </div>

          <div className="border-2 border-dashed border-kb-border rounded-lg p-6 flex flex-col items-center text-center">
            <div className="w-10 h-10 rounded-xl bg-status-warn-dim flex items-center justify-center mb-3">
              <KubeBoltLogo className="w-5 h-5 text-status-warn" />
            </div>
            <h3 className="text-sm font-medium text-kb-text-primary mb-1">Install KubeBolt Agent</h3>
            <p className="text-xs text-kb-text-secondary mb-4 max-w-sm">
              Lightweight DaemonSet that ships per-container CPU, memory, network, and filesystem
              time-series to KubeBolt. Enables historical charts on the Monitor tab.
            </p>
            <div className="bg-kb-bg rounded-md px-4 py-2.5 font-mono text-[11px] text-kb-text-secondary border border-kb-border select-all">
              helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
