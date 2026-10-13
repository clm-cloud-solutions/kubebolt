import { Unplug } from 'lucide-react'
import { clockOrDay } from '@/utils/localTime'
import { formatAge } from '@/utils/formatters'

// OutOfContactBanner — the cluster is reached through its agent and no agent
// is connected. KubeBolt keeps answering from its cache, so every page still
// draws the cluster as it was when contact was lost: this says so, and since
// when. It does not say why. From this side of the channel an agent that
// crashed, a node that went down with it and a network cut look the same; on a
// single-node cluster the agent goes down with the node (AKS lab, 2026-10-12:
// a deallocated node kept reading "1/1 ready" with no word of doubt).
export function OutOfContactBanner({ since }: { since?: string }) {
  return (
    <div role="status" className="px-4 py-1.5 bg-status-warn-dim border-b border-kb-border text-xs text-status-warn flex items-center gap-2 shrink-0">
      <Unplug className="w-3.5 h-3.5 shrink-0" />
      <span className="flex-1">
        <span className="font-medium">
          No contact with this cluster{since ? ` since ${clockOrDay(since)} (${formatAge(since)} ago)` : ''}.
        </span>{' '}
        What you see is its last known state. KubeBolt reaches it through its agent, and cannot tell whether the agent, its node or the network went down.
      </span>
    </div>
  )
}
