import { Link } from 'react-router-dom'
import { Layers, ArrowRight, CheckCircle2 } from 'lucide-react'
import type { ClusterOverview, WorkloadSummary } from '@/types/kubernetes'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'

interface WorkloadHealthProps {
  overview: ClusterOverview
}

interface HealthRowProps {
  label: string
  // kind is the canonical Kubernetes Kind we'd put in a Kobi payload
  // (Deployment, StatefulSet, DaemonSet, Job). The `label` above is
  // the user-facing plural form; we keep them separate so the prompt
  // payload stays correctly singular without parsing the label.
  kind: string
  ready: number
  total: number
  // Pre-filtered list of unhealthy workloads of this kind, used to
  // attach a meaningful resource ref to the Ask Kobi button. Empty
  // when nothing is degraded — the button doesn't render in that
  // case, so the row stays visually quiet.
  unhealthy: WorkloadSummary[]
}

function HealthRow({ label, kind, ready, total, unhealthy }: HealthRowProps) {
  const percent = total > 0 ? (ready / total) * 100 : 100
  const notReady = total - ready
  // Pick the first unhealthy workload as the "anchor" for Kobi's
  // prompt. If there are several, the prompt's `details.hint` tells
  // Kobi the count so it can decide whether to broaden via tool
  // calls — better than fabricating a synthetic "multiple" name.
  const anchor = unhealthy[0]

  return (
    <div className="flex items-center gap-3">
      <span className="text-[11px] text-kb-text-secondary w-24 shrink-0">{label}</span>
      <div className="flex-1 h-2 rounded-full overflow-hidden bg-[var(--kb-bar-track)]">
        <div className="flex h-full">
          <div
            className="h-full bg-status-ok transition-all duration-500"
            style={{ width: `${percent}%` }}
          />
          {notReady > 0 && (
            <div
              className="h-full bg-status-error transition-all duration-500"
              style={{ width: `${(notReady / total) * 100}%` }}
            />
          )}
        </div>
      </div>
      <span className="text-[10px] font-mono text-kb-text-secondary w-12 text-right shrink-0">
        {ready}/{total}
      </span>
      {/* Ask Kobi only when this kind has at least one unhealthy
          workload AND we have an anchor to send (Jobs aren't part of
          buildNamespaceWorkloads on the backend, so they always lack
          an anchor and the button stays hidden — that's intentional,
          a generic "Jobs are unhealthy" prompt without a target isn't
          actionable). */}
      {anchor && (
        <AskCopilotButton
          variant="icon"
          payload={{
            type: 'not_ready_resource',
            resource: {
              kind,
              namespace: anchor.namespace,
              name: anchor.name,
              status: anchor.status,
              details: {
                replicas: anchor.replicas,
                readyReplicas: anchor.readyReplicas,
                totalNotReadyOfKind: notReady,
                hint:
                  notReady > 1
                    ? `${notReady} ${label.toLowerCase()} are not ready cluster-wide; this is one of them`
                    : `1 ${kind.toLowerCase()} is not ready in this cluster`,
              },
            },
          }}
          label={`Ask Kobi about unhealthy ${label}`}
          className="shrink-0"
        />
      )}
    </div>
  )
}

// Route segment per kind for the Needs-attention deep links —
// matches the resource detail route shape /:type/:namespace/:name.
const KIND_ROUTE: Record<string, string> = {
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
}
const KIND_SHORT: Record<string, string> = {
  Deployment: 'dep',
  StatefulSet: 'sts',
  DaemonSet: 'ds',
}

const ATTENTION_CAP = 5

export function WorkloadHealth({ overview }: WorkloadHealthProps) {
  // Walk namespaceWorkloads once and bucket by kind so each HealthRow
  // can pull its own list in O(1). The connector only populates
  // Deployments / StatefulSets / DaemonSets here (see
  // buildNamespaceWorkloads); Jobs intentionally have no entry, which
  // is why the Jobs row never gets an Ask Kobi anchor.
  const unhealthyByKind = new Map<string, WorkloadSummary[]>()
  for (const nsw of overview.namespaceWorkloads ?? []) {
    for (const w of nsw.workloads ?? []) {
      if ((w.readyReplicas ?? 0) < (w.replicas ?? 0)) {
        const list = unhealthyByKind.get(w.kind) ?? []
        list.push(w)
        unhealthyByKind.set(w.kind, list)
      }
    }
  }
  // Needs-attention list: every unhealthy workload across kinds, in
  // the same kind order as the bars above. This fills the card's
  // lower half with the names the bars only count — the "29/29 vs
  // 28/29, but WHICH one?" gap.
  const attention = ['Deployment', 'StatefulSet', 'DaemonSet'].flatMap(
    (k) => unhealthyByKind.get(k) ?? [],
  )

  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4">
      <div className="flex items-center gap-2 mb-4">
        <span className="text-kb-text-secondary shrink-0">
          <Layers className="w-4 h-4" />
        </span>
        <h4 className="text-sm font-semibold text-kb-text-primary">Workload Health</h4>
      </div>
      <div className="space-y-3">
        <HealthRow
          label="Deployments"
          kind="Deployment"
          ready={overview.deployments?.ready ?? 0}
          total={overview.deployments?.total ?? 0}
          unhealthy={unhealthyByKind.get('Deployment') ?? []}
        />
        <HealthRow
          label="StatefulSets"
          kind="StatefulSet"
          ready={overview.statefulSets?.ready ?? 0}
          total={overview.statefulSets?.total ?? 0}
          unhealthy={unhealthyByKind.get('StatefulSet') ?? []}
        />
        <HealthRow
          label="DaemonSets"
          kind="DaemonSet"
          ready={overview.daemonSets?.ready ?? 0}
          total={overview.daemonSets?.total ?? 0}
          unhealthy={unhealthyByKind.get('DaemonSet') ?? []}
        />
        <HealthRow
          label="Jobs"
          kind="Job"
          ready={overview.jobs?.ready ?? 0}
          total={overview.jobs?.total ?? 0}
          unhealthy={[]}
        />
      </div>

      <div className="mt-4 pt-3 border-t border-kb-border/60">
        <div className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary mb-2">
          Needs attention
        </div>
        {attention.length === 0 ? (
          <div className="flex items-center gap-1.5 text-xs font-mono text-kb-text-tertiary">
            <CheckCircle2 className="w-3.5 h-3.5 text-status-ok" />
            all workloads healthy
          </div>
        ) : (
          <div className="space-y-1.5">
            {attention.slice(0, ATTENTION_CAP).map((w) => (
              <AttentionRow key={`${w.kind}/${w.namespace}/${w.name}`} workload={w} />
            ))}
            {attention.length > ATTENTION_CAP && (
              <div className="text-[10px] font-mono text-kb-text-tertiary pt-0.5">
                +{attention.length - ATTENTION_CAP} more degraded
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

// AttentionRow — one degraded workload: amber dot, kind tag, name
// (links to the resource detail page), ready ratio. Hover brightens
// and reveals the arrow, same affordance as the KPI legend rows.
function AttentionRow({ workload: w }: { workload: WorkloadSummary }) {
  const route = KIND_ROUTE[w.kind]
  const ratio = `${w.readyReplicas ?? 0}/${w.replicas ?? 0}`
  const inner = (
    <>
      <span className="w-2 h-2 rounded-full shrink-0 bg-status-warn" />
      <span className="text-kb-text-tertiary shrink-0 w-7">{KIND_SHORT[w.kind] ?? w.kind.toLowerCase()}</span>
      <span
        className="text-kb-text-secondary group-hover:text-kb-text-primary transition-colors truncate"
        title={`${w.namespace}/${w.name}`}
      >
        {w.name}
      </span>
      <span className="ml-auto tabular-nums text-status-warn shrink-0">{ratio}</span>
      <ArrowRight className="w-3 h-3 shrink-0 text-kb-text-tertiary opacity-0 group-hover:opacity-100 transition-opacity" />
    </>
  )
  if (!route) {
    return <div className="flex items-center gap-2 text-xs font-mono">{inner}</div>
  }
  return (
    <Link
      to={`/${route}/${w.namespace}/${w.name}`}
      className="flex items-center gap-2 text-xs font-mono group"
    >
      {inner}
    </Link>
  )
}
