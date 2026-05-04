import type { ClusterOverview } from '@/types/kubernetes'

interface Props {
  overview: ClusterOverview
}

// OverviewHeader anchors the page with a title + identity subtitle.
// Stays inside the existing type system (DM Sans + JetBrains Mono):
// no italic serif accent, no new font weights — the mockup's italic
// "overview" word is decoration we deliberately don't replicate.
export function OverviewHeader({ overview }: Props) {
  const clusterName = overview.clusterName ?? '—'
  const nodes = overview.nodes?.total ?? 0
  const namespaces = overview.namespaces?.total ?? 0

  return (
    <header className="space-y-1">
      <h1 className="text-2xl font-semibold text-kb-text-primary leading-none tracking-tight">
        Cluster overview
      </h1>
      <p className="text-[11px] font-mono text-kb-text-tertiary">
        Live snapshot · {clusterName} · {nodes} {nodes === 1 ? 'node' : 'nodes'} ·{' '}
        {namespaces} {namespaces === 1 ? 'namespace' : 'namespaces'}
      </p>
    </header>
  )
}
