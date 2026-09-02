import type { InsightMute } from '@/services/api'
import type { Insight } from '@/types/kubernetes'

// The mute overlay's pure logic (#54). A mute keys on (ruleId, resource) —
// the cluster scoping already happened server-side (the list endpoint
// returns the request cluster's mutes).
//
// Piercing (#54 §5, «muted-but-worsening»): a muted resource whose CURRENT
// severity is critical reappears in the default view, marked. Silencing an
// info-level nuisance must never be able to swallow its escalation.

export function muteKey(ruleId: string, resource: string): string {
  return `${ruleId}\u0000${resource}`
}

export function buildMuteIndex(mutes: InsightMute[]): Map<string, InsightMute> {
  const index = new Map<string, InsightMute>()
  for (const m of mutes) index.set(muteKey(m.ruleId, m.resource), m)
  return index
}

export interface MutedInsight {
  insight: Insight
  mute?: InsightMute
  pierced: boolean
}

export interface MutePartition {
  // What the default view shows: unmuted insights plus pierced ones.
  visible: MutedInsight[]
  // Muted and NOT pierced — hidden by default, counted in the header.
  hidden: MutedInsight[]
}

export function partitionByMutes(
  insights: Insight[],
  index: Map<string, InsightMute>,
): MutePartition {
  const visible: MutedInsight[] = []
  const hidden: MutedInsight[] = []
  for (const insight of insights) {
    const mute = insight.ruleId ? index.get(muteKey(insight.ruleId, insight.resource)) : undefined
    if (!mute) {
      visible.push({ insight, pierced: false })
    } else if (insight.severity === 'critical') {
      visible.push({ insight, mute, pierced: true })
    } else {
      hidden.push({ insight, mute, pierced: false })
    }
  }
  return { visible, hidden }
}
