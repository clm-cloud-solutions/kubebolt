import { Link } from 'react-router-dom'
import { Rocket } from 'lucide-react'
import { formatAge } from '@/utils/formatters'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import type { DeployEvent } from '@/services/api'

interface Props {
  deploys: DeployEvent[]
  windowMinutes: number
}

const KIND_TO_PATH: Record<string, string> = {
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
}

// DeploysList renders the same data the chart triangles encode, but
// in a structured form: workload name, namespace, image tag, time
// relative-to-now. It's the "give me the table view of what just
// changed" counterpart to the visual markers — same source, deeper
// content, click-to-drill into each workload.
//
// Always visible. When the window has no deploys, renders a slim
// empty-state row so the user knows the panel exists, the data was
// fetched, and nothing happened — confirms the silence rather than
// leaving them guessing whether the section disappeared.
export function DeploysList({ deploys, windowMinutes }: Props) {
  const hasDeploys = deploys && deploys.length > 0

  // Cap the visible rows so a busy window (50+ deploys) doesn't push
  // the rest of the page below the fold. The header line carries the
  // total count so the user knows there's more if they widen the
  // range or filter.
  const VISIBLE_LIMIT = 10
  const visible = hasDeploys ? deploys.slice(0, VISIBLE_LIMIT) : []
  const overflow = hasDeploys ? deploys.length - visible.length : 0

  // Kobi payload — same rows the user sees, with the image (if
  // any) and a relative "ago" so the LLM doesn't have to do
  // timestamp math. Kept compact: name/namespace/kind + image +
  // age. Empty windows skip the button (no rows to discuss).
  const kobiRows = visible.map((d) => {
    const blob: Record<string, string | number> = {
      workload: `${d.namespace}/${d.name}`,
      kind: d.kind,
      ago: formatAge(d.deployedAt),
    }
    if (d.image) blob.image = d.image
    return blob
  })

  return (
    <div className="rounded-lg border border-kb-border bg-kb-card p-4">
      <div className="flex items-center justify-between mb-3 gap-3">
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-kb-text-secondary shrink-0">
            <Rocket className="w-4 h-4" />
          </span>
          <h4 className="text-sm font-semibold text-kb-text-primary truncate">
            Recent Deploys
          </h4>
          {hasDeploys && (
            <AskCopilotButton
              payload={{
                type: 'panel_inquiry',
                panel: 'recent_deploys',
                rangeLabel: `last ${formatWindow(windowMinutes)}`,
                rows: kobiRows,
                truncatedFromTotal: deploys.length,
              }}
              variant="icon"
              label="Ask Kobi about recent deploys"
            />
          )}
        </div>
        <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">
          {hasDeploys
            ? `${deploys.length} in last ${formatWindow(windowMinutes)}`
            : `last ${formatWindow(windowMinutes)}`}
        </span>
      </div>

      {!hasDeploys && (
        <div className="text-[11px] text-kb-text-tertiary py-3">
          No deploys in this range — widen it to look further back, or trigger a rollout
          to populate.
        </div>
      )}

      {/* Scrollable list. Cap at ~280px (matches EventsFeed) so a
          busy 7d window doesn't push the rest of the page below
          the fold. Overflow scrolls vertically; the +N more line
          stays inside the scroll region so reaching the bottom
          confirms the cap was hit. */}
      <ul className="space-y-1 max-h-[280px] overflow-y-auto">
        {visible.map((d) => {
          const path = KIND_TO_PATH[d.kind]
          // Inner content for the Link region. Hover bg moved up to
          // the li wrapper so the highlight covers both the Link
          // and the per-row Kobi button hanging on its right.
          const inner = (
            <div className="flex items-baseline gap-3 py-1.5 min-w-0">
              <div className="min-w-0 flex-1">
                <div className="flex items-baseline gap-1.5 truncate">
                  <span className="text-xs text-kb-text-primary truncate">{d.name}</span>
                  <span className="text-[10px] font-mono text-kb-text-tertiary truncate">
                    {d.namespace}
                  </span>
                  <span className="text-[9px] font-mono uppercase tracking-[0.06em] text-kb-text-tertiary shrink-0">
                    {d.kind}
                  </span>
                </div>
                {d.image && (
                  <div
                    className="text-[10px] font-mono text-kb-text-tertiary truncate mt-0.5"
                    title={d.image}
                  >
                    {d.image}
                  </div>
                )}
              </div>
              <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0 tabular-nums">
                {formatAge(d.deployedAt)}
              </span>
            </div>
          )
          // Per-row Kobi blob — workload identity + image + age.
          // Single-row payload triggers the singular phrasing in
          // buildTriggerPrompt so the LLM focuses on this rollout.
          const rowBlob: Record<string, string | number> = {
            workload: `${d.namespace}/${d.name}`,
            kind: d.kind,
            ago: formatAge(d.deployedAt),
          }
          if (d.image) rowBlob.image = d.image
          // Use the timestamp in the key so multiple rollouts of the
          // same workload (each producing a new ReplicaSet) don't
          // collide on key.
          const key = `${d.namespace}/${d.kind}/${d.name}/${d.deployedAt}`
          return (
            <li
              key={key}
              className="group flex items-center gap-1 px-2 rounded transition-colors hover:bg-kb-card-hover focus-within:bg-kb-card-hover"
            >
              {path ? (
                <Link
                  to={`/${path}/${encodeURIComponent(d.namespace)}/${encodeURIComponent(d.name)}`}
                  className="flex-1 min-w-0"
                >
                  {inner}
                </Link>
              ) : (
                <div className="flex-1 min-w-0">{inner}</div>
              )}
              <AskCopilotButton
                payload={{
                  type: 'panel_inquiry',
                  panel: 'recent_deploys',
                  rangeLabel: `last ${formatWindow(windowMinutes)}`,
                  rows: [rowBlob],
                }}
                variant="icon"
                label="Ask Kobi about this rollout"
                className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity shrink-0"
              />
            </li>
          )
        })}
        {overflow > 0 && (
          <li className="text-[10px] font-mono text-kb-text-tertiary text-center pt-1.5">
            +{overflow} more · widen the range to see all
          </li>
        )}
      </ul>
    </div>
  )
}

function formatWindow(minutes: number): string {
  if (minutes <= 60) return `${minutes}m`
  if (minutes <= 1440) return `${Math.round(minutes / 60)}h`
  return `${Math.round(minutes / 1440)}d`
}
