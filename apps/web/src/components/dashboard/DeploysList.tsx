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
//
// DEFERRED — "Impact" column (decision 2026-07-16, redesign review):
// the capacity mockup (design/kubebolt-capacity-redesign.html) adds a
// per-deploy Impact verdict (stable / latency▲ / OOMs▲). It is NOT
// implemented because we have no precise source for it today: it
// requires comparing latency (Hubble) + OOM counts in windows before
// and after each rollout, and a naive pre/post diff on a shared-node
// cluster produces false cause-and-effect verdicts (the design README
// itself flags the column as "abierta a revisión"). Revisit when a
// deploy-correlation signal exists server-side (the UptimeBolt deploy
// correlation engine is the likely donor); until then the deploy
// markers over the trend charts remain the honest way to eyeball
// impact.
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

      {/* Table view (mockup grammar): the list packed name + image
          into two stacked lines, which made scanning any single
          attribute across rollouts slow. Columns give each fact its
          own lane — workload, namespace, kind, image tag, when — at
          the cost of a horizontal scroll on narrow viewports (the
          wrapper owns it; the page never scrolls sideways). Capped
          rows + overflow line unchanged. This is also where the
          deferred Impact column lands when a correlation source
          exists (see header comment). */}
      {hasDeploys && (
        <div className="overflow-x-auto max-h-[280px] overflow-y-auto">
          <table className="w-full text-[11px]">
            <thead>
              <tr className="text-left text-[10px] font-mono font-semibold uppercase tracking-[0.07em] text-kb-text-secondary">
                <th className="pb-2 pr-3">Workload</th>
                <th className="pb-2 pr-3">Namespace</th>
                <th className="pb-2 pr-3">Kind</th>
                <th className="pb-2 pr-3">Image</th>
                <th className="pb-2 pr-3 text-right">When</th>
                {/* Fixed-width slot for the per-row Ask-Kobi button —
                    reserved even before the button mounts (it renders
                    async after the Kobi config loads), so its
                    appearance never widens the table and flashes a
                    horizontal scrollbar. */}
                <th className="pb-2 w-8 min-w-[32px]" aria-label="Ask Kobi" />
              </tr>
            </thead>
            <tbody>
              {visible.map((d) => {
                const path = KIND_TO_PATH[d.kind]
                // Per-row Kobi blob — workload identity + image + age.
                // Single-row payload triggers the singular phrasing in
                // buildTriggerPrompt so the LLM focuses on this rollout.
                const rowBlob: Record<string, string | number> = {
                  workload: `${d.namespace}/${d.name}`,
                  kind: d.kind,
                  ago: formatAge(d.deployedAt),
                }
                if (d.image) rowBlob.image = d.image
                // Timestamp in the key so multiple rollouts of the same
                // workload (each a new ReplicaSet) don't collide.
                const key = `${d.namespace}/${d.kind}/${d.name}/${d.deployedAt}`
                return (
                  <tr
                    key={key}
                    className="group border-t border-kb-border transition-colors hover:bg-kb-card-hover"
                  >
                    <td className="py-1.5 pr-3 max-w-[180px]">
                      {path ? (
                        <Link
                          to={`/${path}/${encodeURIComponent(d.namespace)}/${encodeURIComponent(d.name)}`}
                          className="text-kb-text-primary font-medium truncate block hover:text-kb-accent transition-colors"
                        >
                          {d.name}
                        </Link>
                      ) : (
                        <span className="text-kb-text-primary font-medium truncate block">{d.name}</span>
                      )}
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-kb-text-tertiary truncate max-w-[120px]">
                      {d.namespace}
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-[9px] uppercase tracking-[0.06em] text-kb-text-tertiary">
                      {d.kind}
                    </td>
                    <td
                      className="py-1.5 pr-3 font-mono text-kb-text-secondary truncate max-w-[220px]"
                      title={d.image}
                    >
                      {d.image ? shortImage(d.image) : '—'}
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-kb-text-tertiary text-right tabular-nums whitespace-nowrap">
                      {formatAge(d.deployedAt)}
                    </td>
                    <td className="py-1.5 w-8 min-w-[32px]">
                      <AskCopilotButton
                        payload={{
                          type: 'panel_inquiry',
                          panel: 'recent_deploys',
                          rangeLabel: `last ${formatWindow(windowMinutes)}`,
                          rows: [rowBlob],
                        }}
                        variant="icon"
                        label="Ask Kobi about this rollout"
                        className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity"
                      />
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
          {overflow > 0 && (
            <div className="text-[10px] font-mono text-kb-text-tertiary text-center pt-1.5">
              +{overflow} more · widen the range to see all
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// shortImage strips the registry/org path — in a table column the
// distinguishing facts are repo name + tag ("sm-store-backend:bfdf369"),
// not "ghcr.io/clm-cloud-solutions/". Full ref stays in the title attr.
function shortImage(image: string): string {
  const lastSegment = image.split('/').pop() ?? image
  return lastSegment
}

function formatWindow(minutes: number): string {
  if (minutes <= 60) return `${minutes}m`
  if (minutes <= 1440) return `${Math.round(minutes / 60)}h`
  return `${Math.round(minutes / 1440)}d`
}
