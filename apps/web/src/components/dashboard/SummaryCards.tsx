import { Server, Box, FolderOpen, Globe, ShieldOff } from 'lucide-react'
import type { ClusterOverview } from '@/types/kubernetes'

interface SummaryCardsProps {
  overview: ClusterOverview
}

export function SummaryCards({ overview }: SummaryCardsProps) {
  const perms = overview.permissions
  const restricted = (key: string) => perms != null && perms[key] === false

  const cards = [
    {
      label: 'Nodes',
      total: overview.nodes?.total ?? 0,
      ready: overview.nodes?.ready ?? 0,
      status: (overview.nodes?.notReady ?? 0) > 0 ? 'warn' : 'ok',
      statusText: (overview.nodes?.notReady ?? 0) > 0 ? `${overview.nodes?.notReady} not ready` : 'All ready',
      icon: <Server className="w-4 h-4" />,
      color: 'text-status-info',
      bg: 'bg-status-info-dim',
      permissionKey: 'nodes',
    },
    {
      label: 'Pods',
      total: overview.pods?.total ?? 0,
      ready: overview.pods?.ready ?? 0,
      status: (overview.pods?.notReady ?? 0) > 0 ? 'warn' : 'ok',
      statusText: `${overview.pods?.ready ?? 0} running`,
      icon: <Box className="w-4 h-4" />,
      color: 'text-status-ok',
      bg: 'bg-status-ok-dim',
      permissionKey: 'pods',
    },
    {
      label: 'Namespaces',
      total: overview.namespaces?.total ?? 0,
      ready: overview.namespaces?.ready ?? 0,
      status: 'ok',
      statusText: `${overview.namespaces?.total ?? 0} active`,
      icon: <FolderOpen className="w-4 h-4" />,
      color: 'text-[#a78bfa]',
      bg: 'bg-[rgba(167,139,250,0.10)]',
      permissionKey: 'namespaces',
    },
    {
      label: 'Services',
      total: overview.services?.total ?? 0,
      ready: overview.services?.ready ?? 0,
      status: 'ok',
      statusText: `${overview.services?.total ?? 0} endpoints`,
      icon: <Globe className="w-4 h-4" />,
      color: 'text-status-warn',
      bg: 'bg-status-warn-dim',
      permissionKey: 'services',
    },
  ]

  return (
    <div className="grid grid-cols-4 gap-3">
      {cards.map((card) => {
        const isRestricted = restricted(card.permissionKey)
        return (
          <div
            key={card.label}
            className={`bg-kb-card border border-kb-border rounded-[10px] p-4 hover:bg-kb-card-hover transition-colors ${isRestricted ? 'opacity-60' : ''}`}
          >
            <div className="flex items-center justify-between mb-3">
              <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">{card.label}</span>
              <div className={`w-7 h-7 rounded-lg ${card.bg} flex items-center justify-center ${card.color}`}>
                {card.icon}
              </div>
            </div>
            {isRestricted ? (
              <>
                <div className="flex items-center gap-1.5 mb-1">
                  <ShieldOff className="w-4 h-4 text-status-warn" />
                  <span className="text-sm font-medium text-kb-text-secondary">No access</span>
                </div>
                <div className="text-[10px] font-mono text-kb-text-tertiary">
                  Insufficient permissions
                </div>
              </>
            ) : (
              <>
                <div className="text-2xl font-semibold text-kb-text-primary mb-1">{card.total}</div>
                <div className={`text-[11px] font-mono ${card.status === 'ok' ? 'text-status-ok' : 'text-status-warn'}`}>
                  {card.statusText}
                </div>
              </>
            )}
          </div>
        )
      })}
    </div>
  )
}
