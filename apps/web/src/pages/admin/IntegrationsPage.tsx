import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Puzzle,
  Check,
  AlertTriangle,
  HelpCircle,
  Minus,
  Download,
  ExternalLink,
  RefreshCw,
} from 'lucide-react'
import { api, type Integration, type IntegrationStatus } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { useAuth } from '@/contexts/AuthContext'
import { IntegrationDetailPanel } from '@/components/admin/IntegrationDetailPanel'
import { AgentInstallWizard } from '@/components/admin/AgentInstallWizard'

// Status visuals. Kept in one place so the list card, detail panel,
// and install wizard all render the same pill for the same state.
const statusStyle: Record<IntegrationStatus, { label: string; bg: string; color: string; Icon: typeof Check }> = {
  installed:     { label: 'Installed',     bg: 'bg-status-ok-dim',    color: 'text-status-ok',    Icon: Check },
  degraded:      { label: 'Degraded',      bg: 'bg-status-warn-dim',  color: 'text-status-warn',  Icon: AlertTriangle },
  not_installed: { label: 'Not installed', bg: 'bg-kb-elevated',      color: 'text-kb-text-tertiary', Icon: Minus },
  unknown:       { label: 'Unknown',       bg: 'bg-status-info-dim',  color: 'text-status-info',  Icon: HelpCircle },
}

export function StatusBadge({ status }: { status: IntegrationStatus }) {
  const style = statusStyle[status] || statusStyle.unknown
  const Icon = style.Icon
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full ${style.bg} ${style.color} text-[10px] font-mono font-semibold uppercase tracking-wider`}
    >
      <Icon className="w-3 h-3" />
      {style.label}
    </span>
  )
}

function IntegrationCard({
  integration,
  isAdmin,
  onInstall,
  onOpen,
}: {
  integration: Integration
  isAdmin: boolean
  onInstall: (i: Integration) => void
  onOpen: (i: Integration) => void
}) {
  const isInstalled = integration.status === 'installed' || integration.status === 'degraded'

  return (
    <div className="bg-kb-card border border-kb-border rounded-xl p-5 flex flex-col gap-4">
      {/* Header */}
      <div className="flex items-start justify-between gap-2">
        <div className="flex items-center gap-3 min-w-0">
          <div className="w-10 h-10 rounded-lg flex items-center justify-center shrink-0 bg-kb-accent-light text-kb-accent">
            <Puzzle className="w-5 h-5" />
          </div>
          <div className="min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <span className="text-sm font-semibold text-kb-text-primary truncate">{integration.name}</span>
              {integration.version && (
                <span className="px-1.5 py-0.5 rounded-full bg-kb-elevated text-kb-text-secondary text-[9px] font-mono uppercase tracking-wider">
                  {integration.version}
                </span>
              )}
            </div>
            <div className="text-[10px] font-mono text-kb-text-tertiary mt-0.5 truncate">
              {integration.namespace || 'ns: —'}
            </div>
          </div>
        </div>
        <StatusBadge status={integration.status} />
      </div>

      {/* Description */}
      <p className="text-[11px] text-kb-text-secondary leading-relaxed line-clamp-3">
        {integration.description}
      </p>

      {/* Health / hint */}
      {isInstalled && integration.health && (
        <div className="text-[11px] text-kb-text-secondary">
          {integration.health.podsReady}/{integration.health.podsDesired} pods ready
          {integration.health.message ? ` — ${integration.health.message}` : ''}
        </div>
      )}
      {integration.status === 'unknown' && integration.health?.message && (
        <div className="flex items-start gap-1.5 text-[11px] text-status-info">
          <HelpCircle className="w-3.5 h-3.5 shrink-0 mt-0.5" />
          <span className="line-clamp-2">{integration.health.message}</span>
        </div>
      )}

      {/* Actions */}
      <div className="flex items-center justify-between gap-2 mt-auto pt-2">
        <div className="flex items-center gap-2">
          {integration.docsUrl && (
            <a
              href={integration.docsUrl}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-[11px] text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
            >
              <ExternalLink className="w-3 h-3" />
              Docs
            </a>
          )}
        </div>
        <div className="flex items-center gap-2">
          {!isInstalled && isAdmin && (
            <button
              onClick={() => onInstall(integration)}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-accent hover:bg-kb-accent-hover text-kb-on-accent text-xs font-medium transition-colors"
            >
              <Download className="w-3.5 h-3.5" />
              Install
            </button>
          )}
          <button
            onClick={() => onOpen(integration)}
            className="px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors"
          >
            Manage
          </button>
        </div>
      </div>
    </div>
  )
}

export function IntegrationsPage() {
  const { hasRole } = useAuth()
  const isAdmin = hasRole('admin')

  const { data, isLoading, error, refetch, isRefetching } = useQuery({
    queryKey: ['integrations'],
    queryFn: api.listIntegrations,
    // Refresh on interval so status changes after a helm install
    // show up without the user having to reload.
    refetchInterval: 15_000,
  })

  const [installing, setInstalling] = useState<Integration | null>(null)
  const [managing, setManaging] = useState<Integration | null>(null)

  if (isLoading) {
    return <div className="flex justify-center"><LoadingSpinner /></div>
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold text-kb-text-primary flex items-center gap-2">
            <Puzzle className="w-5 h-5" />
            Integrations
          </h1>
          <p className="text-xs text-kb-text-tertiary mt-0.5">
            Optional adapters that unlock features in KubeBolt — historical metrics, network flows, and more.
          </p>
        </div>
        <button
          onClick={() => refetch()}
          disabled={isRefetching}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors disabled:opacity-50"
        >
          <RefreshCw className={`w-3.5 h-3.5 ${isRefetching ? 'animate-spin' : ''}`} />
          Refresh
        </button>
      </div>

      {error && (
        <div className="mb-6 flex items-start gap-2 px-4 py-3 rounded-lg bg-status-error-dim">
          <AlertTriangle className="w-4 h-4 text-status-error shrink-0 mt-0.5" />
          <span className="text-sm text-status-error">Failed to load integrations.</span>
        </div>
      )}

      {data && data.length === 0 && (
        <div className="px-4 py-8 rounded-lg bg-kb-card border border-kb-border text-center">
          <Puzzle className="w-8 h-8 text-kb-text-tertiary mx-auto mb-2" />
          <p className="text-sm text-kb-text-secondary">No integrations registered.</p>
        </div>
      )}

      {data && data.length > 0 && (
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
          {data.map((i) => (
            <IntegrationCard
              key={i.id}
              integration={i}
              isAdmin={isAdmin}
              onInstall={setInstalling}
              onOpen={setManaging}
            />
          ))}
        </div>
      )}

      {/* Install wizard — today only the agent is installable; when
          future integrations become installable we'll pick the right
          wizard by id here. */}
      {installing && installing.id === 'agent' && (
        <AgentInstallWizard
          integration={installing}
          onClose={() => setInstalling(null)}
        />
      )}

      {managing && (
        <IntegrationDetailPanel
          integration={managing}
          isAdmin={isAdmin}
          onClose={() => setManaging(null)}
        />
      )}
    </div>
  )
}
