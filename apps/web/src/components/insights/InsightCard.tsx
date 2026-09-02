import { useState } from 'react'
import { AlertTriangle, AlertCircle, BellOff, Info, SlidersHorizontal } from 'lucide-react'
import { Link } from 'react-router-dom'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import type { Insight } from '@/types/kubernetes'
import { eeInsightEnvironments } from '@/ee/registry'
import type { InsightMute } from '@/services/api'
import { api } from '@/services/api'
import { formatAge } from '@/utils/formatters'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import { useAuth } from '@/contexts/AuthContext'
import { MuteMenu } from './MuteMenu'
import { MutationErrorToast } from '@/components/shared/MutationErrorToast'

interface InsightCardProps {
  insight: Insight
  // #54 overlay: present when this insight is muted. pierced marks the
  // muted-but-critical case that re-surfaces in the default view.
  mute?: InsightMute
  pierced?: boolean
}

const severityConfig = {
  critical: {
    icon: <AlertCircle className="w-4 h-4" />,
    bg: 'bg-status-error-dim',
    text: 'text-status-error',
    border: 'border-status-error/20',
  },
  warning: {
    icon: <AlertTriangle className="w-4 h-4" />,
    bg: 'bg-status-warn-dim',
    text: 'text-status-warn',
    border: 'border-status-warn/20',
  },
  info: {
    icon: <Info className="w-4 h-4" />,
    bg: 'bg-status-info-dim',
    text: 'text-status-info',
    border: 'border-status-info/20',
  },
}

export function InsightCard({ insight, mute, pierced }: InsightCardProps) {
  const config = severityConfig[insight.severity]
  const { hasRole } = useAuth()
  const canMute = hasRole('editor')
  const isAdmin = hasRole('admin')
  const [unmuteError, setUnmuteError] = useState<unknown>(null)
  const queryClient = useQueryClient()
  const unmute = useMutation({
    mutationFn: (id: string) => api.deleteInsightMute(id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['insight-mutes'] }),
    onError: (e) => setUnmuteError(e),
  })

  return (
    <div className={`group bg-kb-card border ${config.border} rounded-[10px] p-4 ${mute && !pierced ? 'opacity-70' : ''}`}>
      <div className="flex items-start gap-3">
        <div className={`shrink-0 mt-0.5 p-1.5 rounded-lg ${config.bg} ${config.text}`}>
          {config.icon}
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center justify-between gap-2 mb-1">
            <h3 className="text-sm font-medium text-kb-text-primary truncate">{insight.title}</h3>
            <div className="flex items-center gap-1.5 shrink-0">
              {mute && (
                <span
                  className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[9px] font-mono uppercase tracking-[0.06em] ${
                    pierced ? 'bg-status-error-dim text-status-error' : 'bg-kb-elevated text-kb-text-tertiary'
                  }`}
                  title={
                    pierced
                      ? 'Silenced, but it escalated to critical — escalation pierces the silence'
                      : mute.reason || 'Silenced'
                  }
                >
                  <BellOff className="w-2.5 h-2.5" />
                  {pierced ? 'muted · escalated' : 'muted'}
                </span>
              )}
              <AskCopilotButton
                payload={{
                  type: 'insight',
                  insight: {
                    id: insight.id,
                    fingerprint: insight.fingerprint,
                    severity: insight.severity,
                    title: insight.title,
                    message: insight.message,
                    resource: insight.resource,
                    namespace: insight.namespace,
                    suggestion: insight.suggestion,
                    lastSeen: insight.lastSeen,
                  },
                }}
                label="Ask Kobi about this insight"
              />
              <span className={`px-2 py-0.5 rounded-full text-[9px] font-mono uppercase tracking-[0.06em] ${config.bg} ${config.text}`}>
                {insight.severity}
              </span>
            </div>
          </div>
          <p className="text-xs text-kb-text-secondary mb-2">{insight.message}</p>
          {insight.suggestion && (
            <div className="bg-kb-bg rounded-md px-3 py-2 mb-2">
              <span className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-[0.06em]">Suggestion: </span>
              <span className="text-[11px] text-kb-text-secondary">{insight.suggestion}</span>
            </div>
          )}
          <div className="flex items-center gap-3 text-[10px] font-mono text-kb-text-tertiary">
            <span>{insight.resource}</span>
            {insight.namespace && <span>{insight.namespace}</span>}
            {/* Same rule chip the History card carries (in-vivo 01-sep: the
                active view never named the rule) — and the anchor the
                «Adjust rule» seam will hang from. */}
            {insight.ruleId && (
              <span className="px-1.5 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary text-[10px] shrink-0">
                {insight.ruleId}
              </span>
            )}
            {/* Age from firstSeen (real first-detection time), NOT lastSeen.
                The engine bumps lastSeen every eval cycle (~10s), so showing it
                made every insight read "~10s old" and hid how long a lingering
                one had actually been open — misleading operators and Kobi.
                See docs/insights-rule-lifecycle-audit.md (Bug B). lastSeen stays
                available in the tooltip as secondary context. */}
            <span title={`First seen ${formatAge(insight.firstSeen)} ago${insight.lastSeen ? ` · last seen ${formatAge(insight.lastSeen)} ago` : ''}`}>
              {formatAge(insight.firstSeen)}
            </span>
          </div>

          {/* Action row — the mockup's interaction contract (v1.2): hidden
              until hover on pointer devices, ALWAYS visible below lg (tablet/
              touch has no hover), and revealed by focus-within for keyboard.
              View episode + the #54 silence overlay; Fase 4 adds Adjust rule
              to this same row. */}
          <div className="mt-2 flex gap-2 opacity-100 lg:opacity-0 lg:group-hover:opacity-100 lg:focus-within:opacity-100 transition-opacity">
            <Link
              to={`/insights/episodes/${insight.id}?from=active`}
              className="px-2.5 py-1 rounded-md border border-status-ok/30 text-[10.5px] font-mono text-status-ok hover:bg-status-ok-dim transition-colors"
            >
              View episode →
            </Link>
            {/* «Adjust rule» seam (#44): straight to the matrix with this
                rule's editor open. Admin-only — the hub is admin territory. */}
            {isAdmin && insight.ruleId && (
              <Link
                to={`/admin/insights?tab=${eeInsightEnvironments ? 'environments' : 'rules'}&rule=${encodeURIComponent(insight.ruleId)}`}
                className="px-2.5 py-1 rounded-md border border-kb-border text-[10.5px] font-mono text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active transition-colors inline-flex items-center gap-1.5"
              >
                <SlidersHorizontal className="w-3 h-3" />
                Adjust rule
              </Link>
            )}
            {canMute &&
              (mute ? (
                <button
                  type="button"
                  disabled={unmute.isPending}
                  onClick={() => unmute.mutate(mute.id)}
                  className="px-2.5 py-1 rounded-md border border-kb-border text-[10.5px] font-mono text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active transition-colors disabled:opacity-50"
                >
                  Unsilence
                </button>
              ) : (
                <MuteMenu insight={insight} />
              ))}
          </div>
          {unmuteError != null && (
            <MutationErrorToast error={unmuteError} action="Unsilence" onDismiss={() => setUnmuteError(null)} />
          )}
        </div>
      </div>
    </div>
  )
}
