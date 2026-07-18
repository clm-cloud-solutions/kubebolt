import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { LayoutDashboard } from 'lucide-react'
import { api } from '@/services/api'
import type { ClusterOverview } from '@/types/kubernetes'

interface Props {
  overview: ClusterOverview
  // Current sub-tab, rendered as the breadcrumb leaf and used to pick
  // the subtitle's framing line. Defaults keep older call sites valid.
  tab?: 'Overview' | 'Capacity' | 'Reliability'
  // Optional chip rendered next to the title (e.g. Reliability's
  // "Hubble L7 · live" badge).
  badge?: ReactNode
}

// Per-tab framing line — tells the user what QUESTION this tab answers
// before the identity facts (design §7: continuity of altitude).
const TAB_FRAMING: Record<NonNullable<Props['tab']>, string> = {
  Overview: 'Live snapshot',
  Capacity: 'Sizing & consumption',
  Reliability: 'Golden signals',
}

// OverviewHeader anchors the three dashboard sub-tabs with a
// breadcrumb (Clusters / <cluster> / <tab> — the drill-down path back
// to the cluster list; "Fleet" replaces the root once that surface
// ships), the cluster name as the H1, and an identity subtitle that
// now includes the backend-detected cloud provider + region when the
// connector has warmed them. Stays inside the existing type system
// (DM Sans + JetBrains Mono). The icon tile is deliberately NEUTRAL
// (elevated surface, no accent) — this is the calm "command center",
// so it reads considered without competing with the accent-lit
// exclusive zones (Autopilot) that reserve the brand-green glow.
export function OverviewHeader({ overview, tab = 'Overview', badge }: Props) {
  // overview.clusterName carries the connector's context id — for
  // agent-proxy clusters that's "agent:<uid>", not something a human
  // recognizes. The friendly name lives in the clusters list as
  // displayName (set from the agent's kubebolt.io/cluster-name label
  // or edited by the user); `name` there is the raw context id too,
  // so it's only the second fallback. Same queryKey as the Topbar →
  // shared cache, no extra round-trip.
  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    refetchInterval: 60_000,
  })
  const active = clusters?.find((c) => c.active)
  const clusterName = active?.displayName || active?.name || overview.clusterName || '—'
  const nodes = overview.nodes?.total ?? 0
  const namespaces = overview.namespaces?.total ?? 0

  // Identity facts, provider-first — skip whatever the backend hasn't
  // determined (empty provider/region on restricted SAs or cold caches).
  const facts: string[] = [TAB_FRAMING[tab]]
  if (overview.cloudProvider) facts.push(overview.cloudProvider)
  if (overview.region) facts.push(overview.region)
  if (overview.kubernetesVersion) facts.push(overview.kubernetesVersion)
  facts.push(`${nodes} ${nodes === 1 ? 'node' : 'nodes'}`)
  facts.push(`${namespaces} ${namespaces === 1 ? 'namespace' : 'namespaces'}`)

  return (
    <header className="space-y-1.5">
      <nav
        className="flex items-center gap-1.5 text-[10px] font-mono text-kb-text-tertiary"
        aria-label="Breadcrumb"
      >
        <Link to="/clusters" className="hover:text-kb-accent transition-colors">
          Clusters
        </Link>
        <span>/</span>
        <span className="text-kb-text-secondary">{clusterName}</span>
        <span>/</span>
        <span className="text-kb-text-primary">{tab}</span>
      </nav>
      <div className="flex items-center gap-3">
        <div className="w-10 h-10 rounded-xl bg-kb-elevated border border-kb-border flex items-center justify-center shrink-0">
          <LayoutDashboard className="w-5 h-5 text-kb-text-secondary" />
        </div>
        <div className="space-y-0.5 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <h1 className="text-2xl font-semibold text-kb-text-primary leading-none tracking-tight truncate">
              {clusterName}
            </h1>
            {badge}
          </div>
          <p className="text-[11px] font-mono text-kb-text-tertiary truncate">
            {facts.join(' · ')}
          </p>
        </div>
      </div>
    </header>
  )
}
