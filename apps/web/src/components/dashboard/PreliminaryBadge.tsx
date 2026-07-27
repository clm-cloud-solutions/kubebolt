import { Clock } from 'lucide-react'

// PreliminaryBadge flags right-sizing / idle / savings figures whose P95 window
// hasn't accumulated enough history to trust. On a freshly-connected cluster the
// current (often low) load reads as the steady state, so reclaim/savings look
// aggressive — this keeps the Cost Beta honest: the numbers are directional, not
// a mandate to shave the headroom a real demand spike would need.
//
// Renders nothing until we actually know the span (avoids crying wolf while the
// window query loads). Consumers gate on `preliminary` from useRightSizing.
export function PreliminaryBadge({ windowDays }: { windowDays?: number }) {
  const span =
    windowDays == null
      ? null
      : windowDays < 1
        ? `~${Math.max(1, Math.round(windowDays * 24))}h`
        : `~${windowDays.toFixed(windowDays < 10 ? 1 : 0)}d`

  return (
    <span
      className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-mono text-status-warn bg-status-warn-dim border border-status-warn/30 shrink-0"
      title={`Preliminary — based on ${span ?? 'a short window'} of the 7-day P95 baseline. On a young cluster the current (often low) load can masquerade as steady state and miss daily demand peaks, so treat reclaim / savings as directional and keep headroom for spikes. These firm up as history accumulates toward the full 7 days.`}
    >
      <Clock className="w-3 h-3" />
      Preliminary{span ? ` · ${span}/7d` : ''}
    </span>
  )
}
