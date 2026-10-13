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
  // A Deployment rolling out a new ReplicaSet, or null.
  rollout: Rollout | null
}

// A rollout names the ReplicaSet it brought up. Its pods are named after it
// ("<replicaset>-<suffix>"), so a pod of that ReplicaSet that started failing
// soon after is matched by name exactly, not guessed.
export interface Rollout {
  replicaSet: string
  at: string
  // The first pod of that ReplicaSet to start failing within ROLLOUT_WINDOW_S.
  then: { summary: string | null; afterS: number } | null
}

// How long after a rollout a failing pod of it still reads as "then".
export const ROLLOUT_WINDOW_S = 15 * 60

// A ReplicaSet its Deployment created (GET /deploys): when, not which one.
export interface DeployMark {
  namespace: string
  name: string
  deployedAt: string
}

// A scale-up this soon after its Deployment created a ReplicaSet is that
// rollout. The few seconds before absorb the two clocks (creation stamp,
// event stamp) disagreeing.
export const DEPLOY_MATCH_S = 120

const ts = (e: KubeEvent) => (e.timestamp ? Date.parse(e.timestamp) || 0 : 0)

export function groupEvents(events: KubeEvent[], deploys: DeployMark[] = []): EventGroup[] {
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
      rollout: rolloutOf(list, deploys),
    })
  }
  linkRollouts(groups)
  // Still a feed of RECENT events: newest group first.
  return groups.sort((a, b) => ts(b.events[0]) - ts(a.events[0]))
}

const SCALED = /^Scaled (up|down) replica set (\S+) /

// rolloutOf — whether a Deployment's events describe a rollout, and of which
// ReplicaSet. Scaling a ReplicaSet that already existed (kubectl scale, an
// HPA, waking from zero) is not one.
//   - A scale-up right after the Deployment created a ReplicaSet is: that
//     catches the rollout whose new pods never get ready, where the old
//     ReplicaSet is never scaled down.
//   - Without one that new (no /deploys, or a rollback that reuses an old
//     ReplicaSet): two different ReplicaSets scaled, the new one up.
export function rolloutOf(list: KubeEvent[], deploys: DeployMark[] = []): Rollout | null {
  const head = list[0]
  if (!head?.object.startsWith('Deployment/')) return null
  const name = head.object.slice('Deployment/'.length)
  const scaled = list
    .filter((e) => e.reason === 'ScalingReplicaSet')
    .map((e) => ({ e, m: SCALED.exec(e.message) }))
    .filter((x): x is { e: KubeEvent; m: RegExpExecArray } => x.m != null)
  // Newest first: the latest scale-up names the ReplicaSet being rolled out.
  const ups = scaled.filter((x) => x.m[1] === 'up')
  const created = deploys.filter((d) => d.namespace === head.namespace && d.name === name).map((d) => Date.parse(d.deployedAt))
  const fresh = ups.find((x) =>
    created.some((c) => {
      const dt = ts(x.e) - c
      return dt >= -5000 && dt <= DEPLOY_MATCH_S * 1000
    }),
  )
  const up = fresh ?? (new Set(scaled.map((x) => x.m[2])).size >= 2 ? ups[0] : undefined)
  return up ? { replicaSet: up.m[2], at: up.e.timestamp, then: null } : null
}

// linkRollouts attaches to each rollout the first of its pods to fail after it.
function linkRollouts(groups: EventGroup[]) {
  for (const g of groups) {
    if (!g.rollout) continue
    const at = Date.parse(g.rollout.at) || 0
    const prefix = `Pod/${g.rollout.replicaSet}-`
    let best: { summary: string | null; first: number } | null = null
    for (const w of groups) {
      if (w.type !== 'Warning' || w.namespace !== g.namespace || !w.object.startsWith(prefix)) continue
      const first = Math.min(...w.events.filter((e) => e.type === 'Warning').map(ts))
      if (first < at || first - at > ROLLOUT_WINDOW_S * 1000) continue
      if (!best || first < best.first) best = { summary: w.summary, first }
    }
    if (best) g.rollout.then = { summary: best.summary, afterS: Math.round((best.first - at) / 1000) }
  }
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
