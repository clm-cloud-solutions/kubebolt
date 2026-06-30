import { Eye } from 'lucide-react'

// metrics-only surfaces always state the SAME two upgrade paths so the user knows how to
// unlock live resource views + operations: expose the cluster API (direct-connect) OR run
// the agent with kube-proxy (reader/operator). MetricsOnlyNotice is the full-page variant;
// MetricsOnlyBanner is the slim top-of-page strip used where data still renders below it.

// MetricsOnlyNotice — full-page empty state (e.g. a proxy-only resource tab).
export function MetricsOnlyNotice({ compact = false }: { compact?: boolean }) {
  return (
    <div className={`flex flex-col items-center justify-center text-center ${compact ? 'py-12' : 'h-full'}`}>
      <div className="w-12 h-12 rounded-2xl bg-status-info-dim flex items-center justify-center mb-4">
        <Eye className="w-6 h-6 text-status-info" />
      </div>
      <h3 className="text-sm font-semibold text-kb-text-primary mb-1">Monitored-only cluster</h3>
      <p className="text-xs text-kb-text-tertiary mb-2 max-w-sm">
        This cluster ships metrics but has no live API connection, so live resource views (Pods,
        Deployments, Map, Kobi actions) aren&apos;t available. The metrics dashboards work — they read
        metrics directly.
      </p>
      <p className="text-xs text-kb-text-tertiary max-w-md">
        For live resource views &amp; operations, either{' '}
        <strong className="text-kb-text-secondary">(1) expose the cluster&apos;s API</strong> and connect
        it directly, or <strong className="text-kb-text-secondary">(2) enable the KubeBolt agent-proxy</strong>{' '}
        (<code className="text-kb-text-secondary">rbac.mode=reader</code> for read,{' '}
        <code className="text-kb-text-secondary">operator</code> for read + write).
      </p>
    </div>
  )
}

// MetricsOnlyBanner — slim strip pinned above a surface that still renders content below
// (Overview with KSM-derived data). Carries the same two-path message.
export function MetricsOnlyBanner() {
  return (
    <div className="flex items-start gap-2.5 rounded-[10px] border border-kb-border bg-status-info-dim px-3 py-2.5">
      <Eye className="w-4 h-4 text-status-info mt-0.5 shrink-0" />
      <p className="text-xs text-kb-text-secondary leading-snug">
        <span className="font-medium text-kb-text-primary">Monitored-only cluster.</span> Metrics
        dashboards are live; live resource views &amp; operations are off. To enable them, either{' '}
        <strong className="text-kb-text-primary">(1) expose the cluster&apos;s API</strong> and connect it
        directly, or <strong className="text-kb-text-primary">(2) enable the KubeBolt agent-proxy</strong>{' '}
        (<code className="text-kb-text-secondary">rbac.mode=reader</code> for read,{' '}
        <code className="text-kb-text-secondary">operator</code> for read + write).
      </p>
    </div>
  )
}
