import { Layers } from 'lucide-react'
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
    </div>
  )
}
