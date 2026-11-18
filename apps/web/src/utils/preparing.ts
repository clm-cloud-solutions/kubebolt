import type { ClusterProgress, ClusterTypeProgress } from '@/types/kubernetes'

// The Preparing view's reading of a connect (GET /clusters/progress): how far
// it is, what is happening and why the wait is worth it, and what has just
// happened. Pure, so the view and its tests read the same thing.

export type StepState = 'done' | 'active' | 'pending'

export interface PrepareStep {
  key: 'agents' | 'access' | 'load' | 'health'
  label: string
  detail: string
  state: StepState
}

export interface PrepareView {
  pct: number
  what: string
  why: string
  steps: PrepareStep[]
  loadedObjects: number
  totalObjects: number
}

// A type the API server has not sized yet fits in one page at most.
const UNSIZED_WEIGHT = 500
// A collection this big gets named while the rest waits for it.
const LARGE_COLLECTION = 1000

const fmt = (n: number) => n.toLocaleString('en-US')

export function kindPlural(kind: string): string {
  if (/(s|x|ch|sh)$/.test(kind)) return kind + 'es'
  if (/[^aeiou]y$/.test(kind)) return kind.slice(0, -1) + 'ies'
  return kind + 's'
}

function weightOf(t: ClusterTypeProgress) {
  const w = t.total > 0 ? t.total : UNSIZED_WEIGHT
  return { w, got: t.synced ? w : Math.min(t.loaded, w) }
}

// The type the load is waiting on, when it is the large one: the last
// pending, or one of the last two, sized and big.
function waitingOn(types: ClusterTypeProgress[]): ClusterTypeProgress | null {
  const pending = types.filter((t) => !t.synced)
  if (pending.length === 0 || pending.length > 2) return null
  const big = [...pending].sort((a, b) => b.total - b.loaded - (a.total - a.loaded))[0]
  return big.total >= LARGE_COLLECTION ? big : null
}

function syncedCountOf(types: ClusterTypeProgress[]) {
  return types.filter((t) => t.synced).length
}

function andList(names: string[]) {
  return names.length === 1 ? names[0] : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
}

export function prepareView(p: ClusterProgress | undefined, name: string, prevPct = 0): PrepareView {
  const types = p?.types ?? []
  const ready = p?.state === 'ready'
  // A kubeconfig cluster has no agents to wait for.
  const viaAgent = p?.viaAgent !== false
  const agentsDone = ready || !viaAgent || (p?.agents ?? 0) > 0 || !!p?.accessChecked
  const accessDone = ready || !!p?.accessChecked
  const allSynced = ready || (types.length > 0 && types.every((t) => t.synced))

  let sumW = 0
  let sumGot = 0
  let loadedObjects = 0
  let totalObjects = 0
  for (const t of types) {
    const { w, got } = weightOf(t)
    sumW += w
    sumGot += got
    if (t.total > 0) {
      totalObjects += t.total
      loadedObjects += t.synced ? t.total : Math.min(t.loaded, t.total)
    }
  }
  const frac = allSynced ? 1 : sumW > 0 ? sumGot / sumW : 0
  let pct = (agentsDone ? 4 : 0) + (accessDone ? 6 : 0) + 84 * frac + (ready ? 6 : allSynced ? 3 : 0)
  pct = ready ? 100 : Math.min(99, Math.max(prevPct, Math.round(pct)))

  const big = waitingOn(types)
  // The one or two types the load is still waiting on, named: "1 of 26" says
  // nothing about why the bar has stopped.
  const pendingTypes = types.filter((t) => !t.synced)
  const lastPending = pendingTypes.length > 0 && pendingTypes.length <= 2 && types.length > 2 ? pendingTypes.map((t) => kindPlural(t.kind)) : []
  let what: string
  let why: string
  if (ready) {
    what = `${name} is ready`
    why = 'Every view is open.'
  } else if (!agentsDone) {
    what = `Reaching ${name} through its agents`
    why = 'The agents dial out to KubeBolt, so nothing in your cluster is opened to the outside.'
  } else if (!accessDone) {
    what = `Checking what KubeBolt may read in ${name}`
    why = 'KubeBolt loads only what its role allows, and tells you what it cannot see instead of guessing.'
  } else if (!allSynced) {
    what = big
      ? `Loading ${fmt(big.total)} ${kindPlural(big.kind)}, the largest collection in this cluster`
      : lastPending.length > 0
        ? `Loading ${andList(lastPending)}, the last of ${types.length} resource types`
        : `Loading the cluster's resource types · ${syncedCountOf(types)} of ${types.length} ready`
    why =
      'KubeBolt keeps a live copy of the cluster, so afterwards every list, the map and the insights open instantly instead of asking your API server on each click.'
  } else {
    what = 'Running the first health check'
    why = `So the insights you see describe ${name} as it is now, not as it was the last time someone looked.`
  }

  const syncedCount = types.filter((t) => t.synced).length
  const step = (done: boolean, active: boolean): StepState => (done ? 'done' : active ? 'active' : 'pending')
  const allSteps: PrepareStep[] = [
    {
      key: 'agents',
      label: 'Agents connected',
      detail: (p?.agents ?? 0) > 0 ? `${p?.agents} connected` : 'waiting for agents',
      state: step(agentsDone, true),
    },
    {
      key: 'access',
      label: 'Access checked',
      detail: accessDone && p?.probed ? `${p.readable} of ${p.probed} resource types` : 'checking permissions',
      state: step(accessDone, agentsDone),
    },
    {
      key: 'load',
      label: 'Loading the cluster',
      detail: allSynced
        ? `${types.length} of ${types.length} types`
        : big
          ? `${kindPlural(big.kind)} · ${fmt(Math.min(big.loaded, big.total))} of ${fmt(big.total)}`
          : lastPending.length > 0
            ? `${andList(lastPending)} · ${syncedCount} of ${types.length} types`
            : types.length > 0
            ? `${syncedCount} of ${types.length} types`
            : 'resource types',
      state: step(allSynced, accessDone),
    },
    {
      key: 'health',
      label: 'First health check',
      detail: ready ? 'done' : allSynced ? 'evaluating the rules' : 'after the load',
      state: step(ready, allSynced),
    },
  ]
  const steps = allSteps.filter((s) => viaAgent || s.key !== 'agents')
  return { pct, what, why, steps, loadedObjects, totalObjects }
}

// prepareEvents is what happened between two readings of a connect, as the
// lines of the Activity log.
export function prepareEvents(prev: ClusterProgress | undefined, next: ClusterProgress, name: string): string[] {
  const out: string[] = []
  const prevTypes = new Map((prev?.types ?? []).map((t) => [t.kind, t]))
  const types = next.types ?? []

  if (next.viaAgent !== false && (prev?.agents ?? 0) === 0 && next.agents > 0) {
    out.push(`${next.agents} agent${next.agents === 1 ? '' : 's'} connected`)
  }
  if (!prev?.accessChecked && next.accessChecked && next.probed > 0) {
    out.push(`Access checked · ${next.readable} of ${next.probed} resource types readable`)
  }

  const fresh = types.filter((t) => t.synced && !prevTypes.get(t.kind)?.synced)
  if (fresh.length > 0) {
    const names = fresh.map((t) => kindPlural(t.kind))
    const objects = fresh.reduce((n, t) => n + (t.total > 0 ? t.total : 0), 0)
    let label: string
    if (!prev?.types?.length && fresh.length > 3) label = `${fresh.length} resource types loaded`
    else if (names.length === 1) label = `${names[0]} loaded`
    else if (names.length <= 3) label = `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]} loaded`
    else label = `${names.slice(0, 2).join(', ')} and ${names.length - 2} more loaded`
    out.push(objects > 0 ? `${label} · ${fmt(objects)} objects` : label)
  }

  for (const t of types) {
    if (t.synced || t.total < LARGE_COLLECTION) continue
    const before = prevTypes.get(t.kind)
    const half = t.total / 2
    if ((before?.loaded ?? 0) < half && t.loaded >= half) {
      out.push(`${kindPlural(t.kind)} halfway · ${fmt(t.loaded)} of ${fmt(t.total)}`)
    }
  }

  const wasAll = (prev?.types?.length ?? 0) > 0 && (prev?.types ?? []).every((t) => t.synced)
  if (!wasAll && types.length > 0 && types.every((t) => t.synced)) {
    out.push(`All ${types.length} resource types loaded`)
  }
  if (prev?.state !== 'ready' && next.state === 'ready') {
    out.push(`${name} is ready`)
  }
  return out
}

// etaSeconds estimates what is left from the objects' arrival rate over the
// recent readings, or null while there is too little to say.
export function etaSeconds(samples: Array<{ at: number; loaded: number }>, remaining: number): number | null {
  if (remaining <= 0 || samples.length < 3) return null
  const first = samples[0]
  const last = samples[samples.length - 1]
  const dt = (last.at - first.at) / 1000
  const rate = dt > 0 ? (last.loaded - first.loaded) / dt : 0
  if (rate <= 0) return null
  return Math.max(1, Math.ceil(remaining / rate))
}
