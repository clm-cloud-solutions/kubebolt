import { X, Cloud } from 'lucide-react'
import type { FlowEdge } from '@/services/api'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import type { CopilotTriggerPayload } from '@/services/copilot/triggers'

interface ExternalEndpointDetailPanelProps {
  // The synthetic node id (e.g. "ext:fqdn:api.github.com"). Used to
  // pick out the matching edges from the flow data.
  nodeId: string
  label: string
  fqdn?: string
  flows: FlowEdge[]
  onClose: () => void
}

// Aggregates everything the frontend already knows about a given
// external endpoint into a side panel. Source of truth is the same
// flow data that drew the cluster map — no new backend call. If we
// want historical rate, port breakdown, or drop reasons, that's a
// Phase B follow-up.
export function ExternalEndpointDetailPanel({
  nodeId,
  label,
  fqdn,
  flows,
  onClose,
}: ExternalEndpointDetailPanelProps) {
  // Edges that terminate at this external node: match on the same
  // keying rule the map uses (fqdn if known, else ip).
  const matching = flows.filter((f) => {
    if (f.dstPod) return false
    const edgeId = f.dstFqdn ? `ext:fqdn:${f.dstFqdn}` : `ext:ip:${f.dstIp ?? ''}`
    return edgeId === nodeId
  })

  // Resolved IPs for this FQDN (empty array when we only have an IP
  // and no DNS observation, in which case we fall back to showing
  // the node's label as the sole IP).
  const resolvedIPs = Array.from(
    new Set(
      matching
        .map((f) => f.dstIp)
        .filter((ip): ip is string => Boolean(ip)),
    ),
  )

  // Per-caller rate + verdict breakdown. Multiple flows from the
  // same pod can land here (one per verdict + dst_ip combo), so we
  // sum by (namespace, pod).
  type CallerAgg = {
    ns: string
    pod: string
    forwardedRate: number
    droppedRate: number
  }
  const callers = new Map<string, CallerAgg>()
  for (const f of matching) {
    const key = `${f.srcNamespace}/${f.srcPod}`
    const cur = callers.get(key) ?? {
      ns: f.srcNamespace,
      pod: f.srcPod,
      forwardedRate: 0,
      droppedRate: 0,
    }
    if (f.verdict === 'forwarded') cur.forwardedRate += f.ratePerSec
    else cur.droppedRate += f.ratePerSec
    callers.set(key, cur)
  }
  const callerList = Array.from(callers.values()).sort(
    (a, b) => (b.forwardedRate + b.droppedRate) - (a.forwardedRate + a.droppedRate),
  )

  const totalForwarded = callerList.reduce((s, c) => s + c.forwardedRate, 0)
  const totalDropped = callerList.reduce((s, c) => s + c.droppedRate, 0)

  // Ask Copilot payload. Reuses the flow_edge trigger — "is this
  // external connection expected?" framing is built into the
  // prompt when dstPod is empty. We aggregate the caller list so
  // the LLM sees who's talking to the endpoint.
  const primaryIp = resolvedIPs[0]
  const copilotPayload: CopilotTriggerPayload = {
    type: 'flow_edge',
    flow: {
      // srcNamespace/srcPod stay empty — the endpoint has many
      // callers, and the payload's callers array carries them.
      srcNamespace: '',
      srcPod: '',
      dstFqdn: fqdn,
      dstIp: primaryIp,
      verdict: totalDropped > 0 ? 'mixed' : 'forwarded',
      ratePerSec: totalForwarded + totalDropped,
      callers: callerList.slice(0, 20).map((c) => ({
        namespace: c.ns,
        pod: c.pod,
        forwardedRate: c.forwardedRate,
        droppedRate: c.droppedRate,
      })),
    },
  }

  return (
    <div className="absolute right-0 top-0 bottom-0 w-[320px] bg-kb-card border-l border-kb-border z-20 flex flex-col overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-kb-border shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <Cloud className="w-4 h-4 text-[#38bdf8] shrink-0" />
          <span className="text-sm font-mono text-kb-text-primary truncate" title={label}>
            {label}
          </span>
        </div>
        <button
          onClick={onClose}
          className="p-1 rounded hover:bg-kb-elevated text-kb-text-secondary hover:text-kb-text-primary transition-colors shrink-0"
        >
          <X className="w-4 h-4" />
        </button>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-4 space-y-4">
        {/* Identity block */}
        <div>
          <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1">
            Kind
          </div>
          <div className="text-xs font-mono text-kb-text-primary">
            External endpoint
          </div>
        </div>

        {fqdn && (
          <div>
            <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1">
              Hostname
            </div>
            <div className="text-xs font-mono text-kb-text-primary break-all">
              {fqdn}
            </div>
          </div>
        )}

        {/* Resolved IPs — interesting when one FQDN is backed by
            several A records (CDNs, round-robin), since we see them
            all through observed DNS answers. */}
        {resolvedIPs.length > 0 && (
          <div>
            <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1">
              Resolved IPs ({resolvedIPs.length})
            </div>
            <div className="space-y-1">
              {resolvedIPs.map((ip) => (
                <div key={ip} className="text-xs font-mono text-kb-text-secondary break-all">
                  {ip}
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Totals */}
        <div className="grid grid-cols-2 gap-3">
          <div>
            <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1">
              Forwarded
            </div>
            <div className="text-xs font-mono text-kb-text-primary tabular-nums">
              {totalForwarded.toFixed(2)} ev/s
            </div>
          </div>
          <div>
            <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1">
              Dropped
            </div>
            <div className={`text-xs font-mono tabular-nums ${totalDropped > 0 ? 'text-status-err' : 'text-kb-text-secondary'}`}>
              {totalDropped.toFixed(2)} ev/s
            </div>
          </div>
        </div>

        {/* Callers */}
        <div>
          <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-2">
            Callers ({callerList.length})
          </div>
          {callerList.length === 0 ? (
            <div className="text-[10px] text-kb-text-tertiary italic">
              No callers in the current window.
            </div>
          ) : (
            <div className="space-y-1.5">
              {callerList.map((c) => (
                <div
                  key={`${c.ns}/${c.pod}`}
                  className="flex items-baseline justify-between gap-2 text-[11px]"
                >
                  <div className="min-w-0 flex-1">
                    <div className="font-mono text-kb-text-primary truncate" title={`${c.ns}/${c.pod}`}>
                      {c.pod}
                    </div>
                    <div className="font-mono text-[9px] text-kb-text-tertiary truncate">
                      {c.ns}
                    </div>
                  </div>
                  <div className="text-right shrink-0">
                    <div className="font-mono tabular-nums text-kb-text-primary">
                      {c.forwardedRate.toFixed(2)} ev/s
                    </div>
                    {c.droppedRate > 0 && (
                      <div className="font-mono text-[9px] tabular-nums text-status-err">
                        + {c.droppedRate.toFixed(2)} dropped
                      </div>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="pt-2 border-t border-kb-border/60">
          <AskCopilotButton payload={copilotPayload} variant="text" label="Ask Kobi about this endpoint" />
        </div>

        <div className="text-[10px] text-kb-text-tertiary italic pt-2 border-t border-kb-border/60">
          Data reflects the last {/* same window as the flow query */}
          1 min of observed flows. External endpoints aren't in the
          Kubernetes API — this view is reconstructed entirely from
          Hubble events.
        </div>
      </div>
    </div>
  )
}
