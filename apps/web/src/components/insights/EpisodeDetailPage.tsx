import { Link, useParams, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronRight, AlertTriangle, BellOff } from 'lucide-react'
import { api } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { statusBadge, sevChip, episodeDuration } from './EpisodeHistory'
import { useSwitchCluster } from '@/hooks/useSwitchCluster'

// EpisodeDetailPage — one episode's whole story (Fase 2, PR 2.3): the
// append-only transition timeline plus the deterministic side cards —
// recurrence (same fingerprint) and the completeness notice built from the
// capability registry's transitions during the episode's window (#50 × F2).
// The operational-window card joins in Fase 3 with episode clustering.

function dotClass(to: string) {
  switch (to) {
    case 'firing':
      return 'border-status-error bg-status-error'
    case 'resolved':
      return 'border-status-ok bg-status-ok'
    case 'expired':
      return 'border-kb-text-tertiary bg-kb-text-tertiary'
    // The #54 overlay entries — neutral hollow dots: the silence never
    // moves the lifecycle, and the timeline's ink says so too.
    case 'muted':
    case 'unmuted':
      return 'border-kb-text-tertiary bg-kb-card'
    default:
      return 'border-kb-border bg-kb-card'
  }
}

// muteTerms — the current silence's terms, in the card's words.
function muteTerms(m: { untilResolved: boolean; expiresAt?: string }): string {
  if (m.untilResolved) return 'until it resolves'
  if (m.expiresAt) return `until ${new Date(m.expiresAt).toLocaleString()}`
  return 'permanently'
}

export function EpisodeDetailPage() {
  const { id = '' } = useParams()
  // Volver AL ORIGEN: quien llegó desde el Historial regresa al Historial;
  // quien llegó desde un insight activo, a Activos.
  const [searchParams] = useSearchParams()
  const from = searchParams.get('from') === 'history' ? 'history' : 'active'
  const backTo = from === 'history' ? '/insights?view=history' : '/insights'
  const fromQS = `?from=${from}`
  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['insight-episode', id],
    queryFn: () => api.getInsightEpisode(id),
    enabled: !!id,
    retry: false,
  })
  // Standing silence on this episode's key (#54): shown as its own card so
  // «¿está silenciado?» has an answer right where the episode lives. The
  // cluster is scoped explicitly — this page also serves dead clusters.
  const clusterId = data?.episode.clusterId
  const { data: muteData } = useQuery({
    queryKey: ['insight-mutes', clusterId],
    queryFn: () => api.getInsightMutes(clusterId),
    enabled: !!clusterId,
    retry: false,
  })
  const { hasRole } = useAuth()
  const queryClient = useQueryClient()
  // For the «Switch context» affordance: which live cluster owns this episode.
  const { data: clusters } = useQuery({ queryKey: ['clusters'], queryFn: api.listClusters })
  const switchMutation = useSwitchCluster({ goHome: false })
  const unmute = useMutation({
    mutationFn: (muteID: string) => api.deleteInsightMute(muteID),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['insight-mutes'] })
      void queryClient.invalidateQueries({ queryKey: ['insight-episode', id] })
    },
  })

  if (isLoading) return <LoadingSpinner />
  if (error || !data) return <ErrorState message="Episode not found" onRetry={() => refetch()} />

  const { episode: ep, transitions, recurrence, capabilityChanges } = data
  const mute = (muteData?.mutes ?? []).find(
    (m) => m.ruleId === ep.ruleId && m.resource === ep.resource,
  )
  // A LIVE, selectable cluster matching this episode that isn't the active
  // one — the only case where offering a context switch is honest.
  const switchTarget = (clusters ?? []).find(
    (c) =>
      !c.active &&
      (c.clusterId === ep.clusterId || c.context === `agent:${ep.clusterId}`),
  )

  return (
    <div className="space-y-4">
      {/* Breadcrumb — the house detail pattern (mirrors ResourceDetailPage):
          the operator always knows where they stand, and the parent segments
          navigate back to the exact view they came from. */}
      <div className="flex items-center gap-1.5 text-[11px] font-mono text-kb-text-tertiary">
        <Link to="/insights" className="hover:text-kb-text-primary transition-colors">Insights</Link>
        <ChevronRight size={12} />
        <Link to={backTo} className="hover:text-kb-text-primary transition-colors">
          {from === 'history' ? 'History' : 'Active'}
        </Link>
        <ChevronRight size={12} />
        <span className="text-kb-text-primary">episode {ep.id.slice(0, 8)}…</span>
      </div>

      {/* Page header — h1 + state badges, same shape as every other detail. */}
      <div>
        <div className="flex items-center gap-2 flex-wrap">
          <h1 className="text-lg font-semibold text-kb-text-primary">{ep.title || ep.ruleId}</h1>
          {statusBadge(ep)}
          <span className="px-2 py-0.5 rounded-full bg-kb-elevated text-kb-text-secondary text-[10px] font-mono">{ep.ruleId}</span>
        </div>
        <div className="text-xs text-kb-text-tertiary font-mono mt-0.5">{ep.resource}</div>
      </div>

      {/* Meta */}
      <div className="bg-kb-card border border-kb-border rounded-xl p-5">
        <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-4">
          <div><div className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">First seen</div><div className="text-[12px] font-mono text-kb-text-primary mt-0.5">{new Date(ep.firstSeen).toLocaleString()}</div></div>
          <div><div className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">{ep.resolvedAt ? 'Resolved' : 'Last seen'}</div><div className="text-[12px] font-mono text-kb-text-primary mt-0.5">{new Date(ep.resolvedAt || ep.lastSeen).toLocaleString()}</div></div>
          <div><div className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">Duration</div><div className="text-[12px] font-mono text-kb-text-primary mt-0.5">{episodeDuration(ep)}</div></div>
          <div><div className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">Flaps</div><div className="text-[12px] font-mono text-kb-text-primary mt-0.5">×{ep.flapCount}</div></div>
          <div><div className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">Max severity</div><div className="mt-0.5">{sevChip(ep.maxSeverity || ep.severity)}</div></div>
          {/* No Episode-id cell: the breadcrumb already carries it and the
              full id lives in the URL — a 7th cell only bought a wrapped
              second line with one lonely value (in-vivo find 31-ago). */}
          <div>
            <div className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">Cluster</div>
            <div className="text-[12px] font-mono text-kb-text-primary mt-0.5 truncate" title={ep.clusterId}>
              {ep.clusterName || `${ep.clusterId.slice(0, 8)}…`}
            </div>
            {/* Explicit, never automatic (in-vivo ask 01-sep): switching is a
                heavy side effect (kills port-forwards, reloads all cluster
                state) and this page also serves DEAD clusters that cannot be
                switched to — those just show no link. */}
            {switchTarget && (
              <button
                type="button"
                disabled={switchMutation.isPending}
                onClick={() => switchMutation.mutate(switchTarget.context)}
                className="mt-1 text-[10.5px] font-mono text-kb-accent hover:underline disabled:opacity-50"
              >
                Switch context →
              </button>
            )}
          </div>
        </div>
        {ep.prevEpisodeId && (
          <div className="mt-3 text-[11px] font-mono text-kb-text-secondary">
            reopened after an observation gap ·{' '}
            <Link to={`/insights/episodes/${ep.prevEpisodeId}${fromQS}`} className="underline underline-offset-2 hover:text-kb-text-primary">
              view the expired episode →
            </Link>
          </div>
        )}
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-[1fr_320px] gap-4 items-start">
        {/* Timeline */}
        <div className="bg-kb-card border border-kb-border rounded-xl p-5">
          <h2 className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary mb-4">
            Episode history — append-only transitions · most recent first
          </h2>
          {/* Reciente-primero (observación in-vivo 31-ago): en un episodio
              largo, lo que vienes a buscar es el estado más reciente; la
              narrativa se reconstruye leyendo hacia arriba. Consistente con
              la tarjeta de recurrencia. */}
          <div className="relative pl-6 before:content-[''] before:absolute before:left-[7px] before:top-2 before:bottom-2 before:w-px before:bg-kb-border">
            {[...transitions].reverse().map((t, i) => (
              <div key={i} className="relative mb-4 last:mb-0">
                <span className={`absolute -left-[22px] top-1 w-2.5 h-2.5 rounded-full border-2 ${dotClass(t.to)}`} />
                <div className="text-[10px] font-mono text-kb-text-tertiary">{new Date(t.at).toLocaleString()}</div>
                <div className="text-[13px] text-kb-text-primary">
                  <span className="font-mono font-medium">{t.from ? `${t.from} → ${t.to}` : t.to}</span>
                  {t.reason && <span className="text-kb-text-secondary"> — {t.reason}</span>}
                </div>
                <div className="text-[10px] font-mono text-kb-text-tertiary">actor: {t.actor}</div>
              </div>
            ))}
          </div>
        </div>

        <div className="space-y-4">
          {/* Standing silence (#54) — who, since when, until when, why. */}
          {mute && (
            <div className="bg-kb-card border border-kb-border rounded-xl p-4">
              <h3 className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary mb-2 flex items-center gap-1.5">
                <BellOff className="w-3 h-3" /> Silenced
              </h3>
              <p className="text-[11px] text-kb-text-secondary">
                This resource is silenced <span className="text-kb-text-primary font-medium">{muteTerms(mute)}</span>
                {mute.createdBy && <> · by {mute.createdBy}</>}
                {' · since '}
                {new Date(mute.createdAt).toLocaleString()}
              </p>
              {mute.reason && (
                <p className="text-[11px] font-mono text-kb-text-tertiary mt-1">— {mute.reason}</p>
              )}
              {hasRole('editor') && (
                <button
                  type="button"
                  disabled={unmute.isPending}
                  onClick={() => unmute.mutate(mute.id)}
                  className="mt-2 px-2.5 py-1 rounded-md border border-kb-border text-[10.5px] font-mono text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active transition-colors disabled:opacity-50"
                >
                  Unsilence
                </button>
              )}
            </div>
          )}

          {/* Recurrence */}
          <div className="bg-kb-card border border-kb-border rounded-xl p-4">
            <h3 className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary mb-2">Recurrence · same fingerprint</h3>
            {recurrence && recurrence.length > 0 ? (
              <div className="space-y-0.5">
                {/* Each row is a NAVIGABLE episode; the current one carries a
                    filled accent dot + tint so the selection is visible at a
                    glance and the rest read as clickable via hover (in-vivo
                    find 31-ago: nothing signalled either). */}
                {recurrence.map((r) => {
                  const current = r.id === ep.id
                  return (
                    <Link
                      key={r.id}
                      to={`/insights/episodes/${r.id}${fromQS}`}
                      aria-current={current ? 'true' : undefined}
                      className={`flex items-center gap-2 text-[11px] font-mono py-1 px-2 -mx-2 rounded-md transition-colors ${
                        current
                          ? 'bg-kb-accent-light text-kb-text-primary'
                          : 'text-kb-text-secondary hover:bg-kb-card-hover hover:text-kb-text-primary'
                      }`}
                    >
                      <span
                        className={`w-1.5 h-1.5 rounded-full shrink-0 ${current ? 'bg-kb-accent' : 'border border-kb-text-tertiary'}`}
                        aria-hidden
                      />
                      <span className="flex-1">{new Date(r.firstSeen).toLocaleDateString()} · {episodeDuration(r)}</span>
                      <span className={current ? 'text-kb-accent font-semibold' : 'text-kb-text-tertiary'}>
                        {current ? 'this one' : r.resolutionKind || r.status}
                      </span>
                    </Link>
                  )
                })}
                {recurrence.length >= 3 && (
                  <div className="text-[10px] font-mono text-status-warn pt-1">{recurrence.length} episodes on record — recurring pattern</div>
                )}
              </div>
            ) : (
              <div className="text-[11px] text-kb-text-tertiary">First occurrence on record.</div>
            )}
          </div>

          {/* Completeness — the capability registry crossing into the episode */}
          {capabilityChanges && capabilityChanges.length > 0 && (
            <div className="bg-kb-card border border-status-warn/30 rounded-xl p-4">
              <h3 className="text-[10px] font-mono uppercase tracking-wider text-status-warn mb-2 flex items-center gap-1.5">
                <AlertTriangle className="w-3 h-3" /> Completeness notice
              </h3>
              <p className="text-[11px] text-kb-text-secondary mb-2">
                Capability state changed during this episode — the evidence may be incomplete:
              </p>
              <div className="space-y-1">
                {capabilityChanges.map((c, i) => (
                  <div key={i} className="text-[11px] font-mono text-kb-text-secondary">
                    <span className="text-kb-text-tertiary">{new Date(c.at).toLocaleTimeString()}</span>{' '}
                    {c.capability}: {c.from || '·'} → {c.to}
                    {c.reason && <span className="text-kb-text-tertiary"> — {c.reason}</span>}
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
