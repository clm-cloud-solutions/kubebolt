import type { KubeEvent } from '@/types/kubernetes'

// groupEvents — the Overview's Recent Events folded by the object they are
// about. A pod that cannot pull its image emits Pulling, Failed (ErrImagePull)
// and BackOff (ImagePullBackOff) over and over: three rows for one problem,
// pushing every other event out of the panel. Grouped, it is one row that says
// what is going on, with the raw events one click away — they stay, because
// they are what a diagnosis reads.
//
// The key is the exact object (namespace + Kind/Name): no guessing that two
// pods belong to the same Deployment from their names.

export interface EventGroup {
  key: string
  object: string
  namespace: string
  type: 'Warning' | 'Normal'
  // The event that speaks for the group: the newest Warning, else the newest.
  lead: KubeEvent
  // A plain-language name for a known failure pattern, or null.
  summary: string | null
  // Newest first.
  events: KubeEvent[]
}

const ts = (e: KubeEvent) => (e.timestamp ? Date.parse(e.timestamp) || 0 : 0)

export function groupEvents(events: KubeEvent[]): EventGroup[] {
  const byKey = new Map<string, KubeEvent[]>()
  for (const e of events) {
    const key = `${e.namespace}\0${e.object}`
    const list = byKey.get(key)
    if (list) list.push(e)
    else byKey.set(key, [e])
  }
  const groups: EventGroup[] = []
  for (const [key, list] of byKey) {
    list.sort((a, b) => ts(b) - ts(a))
    const warnings = list.filter((e) => e.type === 'Warning')
    groups.push({
      key,
      object: list[0].object,
      namespace: list[0].namespace,
      type: warnings.length > 0 ? 'Warning' : 'Normal',
      lead: warnings[0] ?? list[0],
      summary: summarize(warnings),
      events: list,
    })
  }
  // Still a feed of RECENT events: newest group first.
  return groups.sort((a, b) => ts(b.events[0]) - ts(a.events[0]))
}

// summarize names the failure a set of Warning events describes, in the words
// an operator would use. Only patterns Kubernetes reports unambiguously; the
// rest keeps the event's own message.
export function summarize(warnings: KubeEvent[]): string | null {
  const has = (pred: (e: KubeEvent) => boolean) => warnings.some(pred)
  const reason = (r: string) => (e: KubeEvent) => e.reason === r
  if (
    has(reason('ErrImagePull')) ||
    has(reason('ImagePullBackOff')) ||
    has((e) => (e.reason === 'Failed' || e.reason === 'BackOff') && /pull|image/i.test(e.message))
  )
    return 'Image pull failing'
  if (has(reason('OOMKilling')) || has((e) => /OOMKilled/.test(e.message))) return 'Out of memory'
  if (has((e) => e.reason === 'BackOff' && /restarting failed container/i.test(e.message)))
    return 'Container crash-looping'
  if (has(reason('FailedScheduling'))) return "Can't be scheduled"
  if (has((e) => e.reason === 'Unhealthy' && /readiness/i.test(e.message))) return 'Readiness probe failing'
  if (has((e) => e.reason === 'Unhealthy' && /liveness/i.test(e.message))) return 'Liveness probe failing'
  if (has(reason('FailedMount')) || has(reason('FailedAttachVolume'))) return "Volume can't be mounted"
  if (has(reason('Evicted'))) return 'Evicted'
  if (has(reason('FailedCreate'))) return "Can't create pods"
  return null
}
