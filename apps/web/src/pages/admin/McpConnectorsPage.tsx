import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { AlertTriangle, Ban, Clock, ExternalLink, Lock, Pause, Plug, RefreshCw, ShieldCheck } from 'lucide-react'
import { api, ApiError, type McpComingSoonConnector, type McpConnector } from '@/services/api'
import { EmptyState } from '@/components/shared/EmptyState'
import { McpConnectorDrawer } from '@/components/admin/McpConnectorDrawer'
import { isConfigured, isOffered } from '@/components/admin/mcp/connectorForm'
import {
  MCP_CONNECTORS_KEY,
  ConnectorReach,
  ConnectorStatusBadge,
  ConnectorTile,
  PlanUpgradeCta,
  UPGRADE_CTA,
  categoryLabel,
  connectorChips,
  effectiveStatus,
  planInvitation,
  safeDocsUrl,
  statusSentence,
  useUpgradeContactUrl,
} from '@/components/admin/mcp/ConnectorBits'

// McpConnectorsPage — the AI hub's Connectors tab (/admin/ai?tab=connectors).
//
// A short, curated catalog: AWS and Azure in v1, Google Cloud listed as
// coming soon. No filter chips or search for a list this size — the category
// headings are enough, and a search box over three cards reads as a
// placeholder for a bigger catalog.
//
// The drawer is URL-driven (&connector=<id>) so Kobi's tool cards, docs and
// the cluster map can deep-link straight into one connector. Opening and
// closing merge into the existing params so ?tab=connectors survives.
//
// Connectors are org-wide, not per cluster: nothing here depends on a
// connected cluster, and the page works with none.

// The feature flag off (KUBEBOLT_MCP_CONNECTORS=false) or no settings store:
// the backend does not mount the routes at all.
function isFeatureOff(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404
}

export function McpConnectorsPage() {
  const { data, error, isLoading, refetch, isRefetching } = useQuery({
    queryKey: MCP_CONNECTORS_KEY,
    queryFn: api.listMcpConnectors,
    // Status and lastError move as Kobi uses a connector; poll instead of a
    // Refresh button. Stop polling a route that does not exist.
    refetchInterval: (query) => (isFeatureOff(query.state.error) ? false : 30_000),
    retry: (count, err) =>
      !(err instanceof ApiError && [401, 403, 404, 503].includes(err.status)) && count < 2,
  })
  const [params, setParams] = useSearchParams()
  const openId = params.get('connector')
  // A locked connector that nothing on sale unlocks sends the org to us; the
  // address is looked up only when one is on the page.
  const contactUrl = useUpgradeContactUrl(
    (data?.connectors ?? []).some((c) => {
      const invite = planInvitation(c)
      return !!invite && !invite.upgradeTo
    }),
  )

  const setConnectorParam = (id: string | null) =>
    setParams((prev) => {
      const next = new URLSearchParams(prev)
      if (id) next.set('connector', id)
      else next.delete('connector')
      return next
    })

  if (isLoading) return <SkeletonGrid />

  if (error && !data) {
    if (isFeatureOff(error)) {
      return (
        <EmptyState
          icon={<Plug className="w-10 h-10" />}
          title="Connectors are turned off on this installation"
          message="Whoever runs this KubeBolt server can turn them on. Until then Kobi reads your clusters only."
        />
      )
    }
    const unavailable = error instanceof ApiError && error.status === 503
    return (
      <EmptyState
        icon={<AlertTriangle className="w-10 h-10" />}
        title={unavailable ? 'Connectors are unavailable right now' : "Couldn't load connectors"}
        message={unavailable ? 'KubeBolt could not reach its settings store. Try again in a moment.' : (error as Error).message}
        action={
          <button
            type="button"
            onClick={() => refetch()}
            disabled={isRefetching}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors disabled:opacity-50"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${isRefetching ? 'animate-spin' : ''}`} aria-hidden />
            Try again
          </button>
        }
      />
    )
  }

  const connectors = data?.connectors ?? []
  const comingSoon = data?.comingSoon ?? []
  const open = openId ? connectors.find((c) => c.id === openId) : undefined

  // Category sections in first-seen order; an empty category never renders.
  // A coming-soon connector sits in its category after the offered ones.
  const groups: { category: string; items: McpConnector[]; soon: McpComingSoonConnector[] }[] = []
  const groupFor = (category: string) => {
    let g = groups.find((x) => x.category === category)
    if (!g) {
      g = { category, items: [], soon: [] }
      groups.push(g)
    }
    return g
  }
  for (const c of connectors) groupFor(c.category).items.push(c)
  for (const c of comingSoon) groupFor(c.category).soon.push(c)

  // The drawer is a sibling of the page body, not its last child: inside the
  // space-y wrapper its fixed box picked up a 24px top margin and left a
  // strip of the page uncovered under the Topbar. The bottom padding is the
  // Kobi launcher's (48px, 20px off the corner): the last row of cards can
  // scroll clear of it instead of ending under it.
  return (
    <>
      <div className="space-y-6 pb-20">
        <div className="flex items-start gap-3 rounded-xl border border-kb-border border-l-2 border-l-kb-accent bg-kb-card px-4 py-3">
          <ShieldCheck className="w-4 h-4 text-kb-accent shrink-0 mt-0.5" aria-hidden />
          <p className="text-xs text-kb-text-secondary leading-relaxed">
            <span className="font-semibold text-kb-text-primary">Read-only.</span> Kobi reads status and metrics from
            your cloud&apos;s management APIs. It never changes anything, never reads secret values, and never connects
            to your databases. Credentials are never shared with the model.
          </p>
        </div>

        {error && data && (
          <div className="flex items-center gap-2 text-[11px] text-kobi-st-warn">
            <AlertTriangle className="w-3.5 h-3.5 shrink-0 text-status-warn" aria-hidden />
            Couldn&apos;t refresh — showing the last known state.
          </div>
        )}

        {connectors.length === 0 && comingSoon.length === 0 ? (
          <EmptyState
            icon={<Plug className="w-10 h-10" />}
            title="No connectors available yet"
            message="This edition of KubeBolt ships without connectors."
          />
        ) : (
          groups.map((g) => (
            <section key={g.category} aria-labelledby={`mcp-cat-${g.category}`}>
              <h2
                id={`mcp-cat-${g.category}`}
                className="text-[11px] font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider mb-3"
              >
                {g.category === 'cloud' ? 'Cloud providers' : categoryLabel(g.category)}
                <span className="font-normal"> · {g.items.length + g.soon.length}</span>
              </h2>
              <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
                {g.items.map((c) => (
                  <ConnectorCard
                    key={c.id}
                    connector={c}
                    contactUrl={contactUrl}
                    onOpen={() => setConnectorParam(c.id)}
                  />
                ))}
                {g.soon.map((c) => (
                  <ComingSoonCard key={c.id} connector={c} />
                ))}
              </div>
            </section>
          ))
        )}
      </div>
      {open && (
        <McpConnectorDrawer
          key={open.id}
          connector={open}
          contactUrl={contactUrl}
          onClose={() => setConnectorParam(null)}
        />
      )}
    </>
  )
}

// The card is not itself a button: it holds a Docs link and a CTA, and a
// clickable card around interactive children is nested interactive content.
// The CTA opens the drawer.
function ConnectorCard({
  connector: c,
  contactUrl,
  onOpen,
}: {
  connector: McpConnector
  contactUrl: string
  onOpen: () => void
}) {
  const status = effectiveStatus(c)
  const sentence = statusSentence(c)
  const invite = planInvitation(c, contactUrl)
  // "Kobi only" says something only where Autopilot reads the connector too.
  const servesAutopilot = (c.tools?.autopilot ?? []).length > 0
  const areas = (c.families ?? [])
    .filter((f) => f.kobi)
    .map((f) => ({ label: f.label, kobiOnly: servesAutopilot && !f.autopilot }))

  // Warn text takes the per-theme amber: the status amber reads at about
  // 2:1 on the light theme's white card, so it stays on the icon.
  const sentenceTone =
    sentence?.tone === 'error'
      ? 'text-status-error'
      : sentence?.tone === 'warn'
        ? 'text-kobi-st-warn'
        : sentence?.tone === 'ok'
          ? 'text-kb-text-secondary'
          : 'text-kb-text-tertiary'
  const iconTone = sentence?.tone === 'warn' ? 'text-status-warn' : ''

  return (
    <article className="bg-kb-card border border-kb-border rounded-xl p-5 flex flex-col gap-4" aria-labelledby={`mcp-card-${c.id}`}>
      <div className="flex items-start justify-between gap-2">
        <div className="flex items-center gap-3 min-w-0">
          <ConnectorTile id={c.id} />
          <div className="min-w-0">
            <h3 id={`mcp-card-${c.id}`} className="text-sm font-semibold text-kb-text-primary truncate">
              {c.name}
            </h3>
            <div className="text-[10px] font-mono text-kb-text-tertiary mt-0.5 uppercase tracking-wider">
              {(c.tools?.autopilot ?? []).length > 0 ? 'Kobi · Autopilot' : 'Kobi'}
            </div>
          </div>
        </div>
        <ConnectorStatusBadge connector={c} />
      </div>

      <p className="text-[11px] text-kb-text-secondary leading-relaxed line-clamp-3">{c.description}</p>

      <ConnectorReach areas={areas} reach={c.reach} onOpen={onOpen} />

      {/* The plan lock is an invitation, not a state: the plan, then what the
          upgrade gets. Secondary text on the page's own ground — the tertiary
          grey it used to be read under AA on both themes' cards. */}
      {invite && (
        <div className="flex items-start gap-2 rounded-lg border border-kb-border border-l-2 border-l-kb-accent bg-kb-bg px-3 py-2.5">
          <Lock className="w-3.5 h-3.5 shrink-0 mt-px text-kb-accent" aria-hidden />
          <p className="text-[11px] leading-relaxed text-kb-text-secondary">
            <span className="font-semibold text-kb-text-primary">{invite.headline}.</span>
            {invite.body && ` ${invite.body}`}
          </p>
        </div>
      )}

      {sentence && (
        <p className={`flex items-start gap-1.5 text-[11px] leading-relaxed ${sentenceTone}`}>
          {(sentence.tone === 'error' || (sentence.tone === 'warn' && status !== 'paused')) && (
            <AlertTriangle className={`w-3.5 h-3.5 shrink-0 mt-px ${iconTone}`} aria-hidden />
          )}
          {sentence.tone === 'ok' && <ShieldCheck className="w-3.5 h-3.5 shrink-0 mt-px text-kb-accent" aria-hidden />}
          {status === 'paused' && <Pause className={`w-3.5 h-3.5 shrink-0 mt-px ${iconTone}`} aria-hidden />}
          {status === 'unoffered' && <Ban className="w-3.5 h-3.5 shrink-0 mt-px" aria-hidden />}
          <span className="line-clamp-2">{sentence.text}</span>
        </p>
      )}

      <div className="flex flex-wrap gap-1.5">
        {connectorChips(c).map((chip, i) => (
          <span
            key={chip}
            className={`px-2 py-0.5 rounded-full text-[10px] font-mono border ${
              i === 0
                ? 'bg-status-info-dim text-status-info border-transparent'
                : 'bg-kb-elevated text-kb-text-secondary border-kb-border'
            }`}
          >
            {chip}
          </span>
        ))}
      </div>

      <div className="flex items-center justify-between gap-2 mt-auto pt-3 border-t border-kb-border">
        {safeDocsUrl(c.docsURL) ? (
          <a
            href={safeDocsUrl(c.docsURL)!}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-[11px] text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
          >
            <ExternalLink className="w-3 h-3" aria-hidden />
            Docs
          </a>
        ) : (
          <span />
        )}
        <CardAction connector={c} status={status} contactUrl={contactUrl} onOpen={onOpen} />
      </div>
    </article>
  )
}

// A connector that is built but not offered yet: on the page so it reads as
// next, not as never. Same shape as a card, with no plan, status or action —
// there is no drawer to open — and a dashed edge so it does not read as one
// more connector to set up.
function ComingSoonCard({ connector: c }: { connector: McpComingSoonConnector }) {
  const areas = (c.families ?? []).map((label) => ({ label, kobiOnly: false }))
  return (
    <article
      className="bg-kb-card border border-dashed border-kb-border rounded-xl p-5 flex flex-col gap-4"
      aria-labelledby={`mcp-card-${c.id}`}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex items-center gap-3 min-w-0">
          <ConnectorTile id={c.id} />
          <h3 id={`mcp-card-${c.id}`} className="text-sm font-semibold text-kb-text-primary truncate">
            {c.name}
          </h3>
        </div>
        <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-mono font-semibold uppercase tracking-wider shrink-0 bg-status-info-dim text-status-info">
          <Clock className="w-3 h-3" aria-hidden />
          Soon
        </span>
      </div>

      <p className="text-[11px] text-kb-text-secondary leading-relaxed line-clamp-3">{c.description}</p>

      <ConnectorReach areas={areas} upcoming />

      <p className="mt-auto pt-3 border-t border-kb-border text-[11px] text-kb-text-tertiary">
        Coming soon. There is nothing to set up yet.
      </p>
    </article>
  )
}

// One CTA vocabulary: Set up (not configured) · Manage (enabled, paused,
// needs attention) · Upgrade to <plan> (plan) — with a Preview so a locked
// connector's value (what it reads, how it is set up) is one click away, not
// hidden — and Details for a connector this install does not offer.
function CardAction({
  connector: c,
  status,
  contactUrl,
  onOpen,
}: {
  connector: McpConnector
  status: ReturnType<typeof effectiveStatus>
  contactUrl: string
  onOpen: () => void
}) {
  const secondary =
    'px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-kb-text-primary text-xs border border-kb-border transition-colors'
  const primary =
    'inline-flex items-center px-3.5 py-1.5 rounded-lg bg-kb-accent text-white text-xs font-semibold hover:opacity-90 transition-opacity'

  // Not settable on this install (the backend offered no auth mode — Azure on
  // a multi-tenant install, whose sidecar holds one identity per process):
  // "Set up" would promise a form the drawer cannot show, and Upgrade a plan
  // that would not turn it on (effectiveStatus never says 'plan' for it).
  // Decided by what is stored, whatever the status says: settings saved
  // before the install stopped offering it are still there to remove.
  if (!isOffered(c)) {
    return isConfigured(c) ? (
      <button type="button" onClick={onOpen} className={secondary} aria-label={`Manage ${c.name}`}>
        Manage
      </button>
    ) : (
      <button type="button" onClick={onOpen} className={secondary} aria-label={`Details for ${c.name}`}>
        Details
      </button>
    )
  }
  if (status === 'plan') {
    return (
      <div className="flex items-center gap-2">
        <button type="button" onClick={onOpen} className={secondary} aria-label={`Preview ${c.name}`}>
          Preview
        </button>
        <PlanUpgradeCta connector={c} contactUrl={contactUrl} className={UPGRADE_CTA} />
      </div>
    )
  }
  if (status === 'available') {
    return (
      <button type="button" onClick={onOpen} className={primary} aria-label={`Set up ${c.name}`}>
        Set up
      </button>
    )
  }
  return (
    <button type="button" onClick={onOpen} className={secondary} aria-label={`Manage ${c.name}`}>
      Manage
    </button>
  )
}

function SkeletonGrid() {
  return (
    <div className="space-y-6" aria-busy="true" aria-label="Loading connectors">
      <div className="h-14 rounded-xl bg-kb-card border border-kb-border animate-pulse" />
      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        {[0, 1, 2].map((i) => (
          <div key={i} className="bg-kb-card border border-kb-border rounded-xl p-5 space-y-4 animate-pulse">
            <div className="flex items-center gap-3">
              <div className="w-10 h-10 rounded-lg bg-kb-elevated" />
              <div className="space-y-1.5 flex-1">
                <div className="h-3 w-32 rounded bg-kb-elevated" />
                <div className="h-2.5 w-20 rounded bg-kb-elevated" />
              </div>
            </div>
            <div className="space-y-1.5">
              <div className="h-2.5 rounded bg-kb-elevated" />
              <div className="h-2.5 w-4/5 rounded bg-kb-elevated" />
            </div>
            <div className="h-7 w-20 rounded-lg bg-kb-elevated ml-auto" />
          </div>
        ))}
      </div>
    </div>
  )
}
