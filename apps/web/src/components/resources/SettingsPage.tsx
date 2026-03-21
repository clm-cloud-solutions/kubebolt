import { useClusterOverview } from '@/hooks/useClusterOverview'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { Server, Zap } from 'lucide-react'

export function SettingsPage() {
  const { data: overview, isLoading } = useClusterOverview()

  if (isLoading) return <LoadingSpinner />

  return (
    <div>
      <h1 className="text-lg font-semibold text-[#e8e9ed] mb-4">Settings</h1>

      <div className="grid grid-cols-2 gap-4">
        {/* Cluster Connection */}
        <div className="bg-kb-card border border-kb-border rounded-[10px] p-5">
          <div className="flex items-center gap-2 mb-4">
            <Server className="w-4 h-4 text-status-info" />
            <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-[#555770]">
              Cluster Connection
            </span>
          </div>

          <div className="space-y-3">
            <div>
              <div className="text-[10px] font-mono text-[#555770] uppercase tracking-[0.06em] mb-1">Cluster Name</div>
              <div className="text-sm font-mono text-[#e8e9ed]">{overview?.clusterName || '-'}</div>
            </div>
            <div>
              <div className="text-[10px] font-mono text-[#555770] uppercase tracking-[0.06em] mb-1">Kubernetes Version</div>
              <div className="text-sm font-mono text-[#e8e9ed]">{overview?.kubernetesVersion || '-'}</div>
            </div>
            <div>
              <div className="text-[10px] font-mono text-[#555770] uppercase tracking-[0.06em] mb-1">Platform</div>
              <div className="text-sm font-mono text-[#e8e9ed]">{overview?.platform || '-'}</div>
            </div>
            <div>
              <div className="text-[10px] font-mono text-[#555770] uppercase tracking-[0.06em] mb-1">Status</div>
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
            <Zap className="w-4 h-4 text-status-warn" />
            <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-[#555770]">
              KubeBolt Agent
            </span>
          </div>

          <div className="border-2 border-dashed border-kb-border rounded-lg p-6 flex flex-col items-center text-center">
            <div className="w-10 h-10 rounded-xl bg-status-warn-dim flex items-center justify-center mb-3">
              <Zap className="w-5 h-5 text-status-warn" />
            </div>
            <h3 className="text-sm font-medium text-[#e8e9ed] mb-1">Install KubeBolt Agent</h3>
            <p className="text-xs text-[#8b8d9a] mb-4 max-w-sm">
              Unlock advanced monitoring: network traffic, real-time metrics, AI insights, and more.
            </p>
            <div className="bg-kb-bg rounded-md px-4 py-2.5 font-mono text-[11px] text-[#8b8d9a] border border-kb-border select-all">
              kubectl apply -f https://kubebolt.dev/install/agent.yaml
            </div>
            <span className="text-[10px] font-mono text-[#555770] mt-2 uppercase tracking-[0.06em]">
              Coming in Phase 2
            </span>
          </div>
        </div>
      </div>
    </div>
  )
}
