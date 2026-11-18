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
  // Bytes received, and the estimate of what the sized collections still
  // have to send (bytes per object seen × objects left).
  receivedBytes: number
  remainingBytes: number
  what: string
  why: string
  steps: PrepareStep[]
  loadedObjects: number
  totalObjects: number
}

// A type the API server has not sized fits in one page, and is usually far
// smaller than one: weighing it as a full page let 25 small types fill the bar
// before a cluster's one big collection had said its size.
const SMALL_WEIGHT = 25
// While a type has not answered its first page, the bar does not know how big
// it is: for the first EARLY_SECONDS of the load, the load part stops at
// EARLY_CAP. After that a type that has still said nothing is taken as small
// (a single heavy page, which no count can size).
const EARLY_CAP = 0.35
const EARLY_SECONDS = 8
// A collection this big gets named while the rest waits for it.
const LARGE_COLLECTION = 1000

const fmt = (n: number) => n.toLocaleString('en-US')

export function kindPlural(kind: string): string {
  if (/(s|x|ch|sh)$/.test(kind)) return kind + 'es'
  if (/[^aeiou]y$/.test(kind)) return kind.slice(0, -1) + 'ies'
  return kind + 's'
}

function weightOf(t: ClusterTypeProgress) {
  if (t.total > 0) return { w: t.total, got: t.synced ? t.total : Math.min(t.loaded, t.total) }
  return { w: SMALL_WEIGHT, got: t.synced ? SMALL_WEIGHT : 0 }
}

// The type the load is waiting on, when it is the large one: the last
// pending, or one of the last two, sized and big.
function waitingOn(types: ClusterTypeProgress[]): ClusterTypeProgress | null {
  const pending = types.filter((t) => !t.synced)
  if (pending.length === 0 || pending.length > 2) return null
  const big = [...pending].sort((a, b) => b.total - b.loaded - (a.total - a.loaded))[0]
  return big.total >= LARGE_COLLECTION ? big : null
}

// accessLine says what the permission probe found, as what is missing: its
// totals count kinds the cache does not load, and printing them next to the
// types loaded read as two different counts of the same thing.
function accessLine(p: ClusterProgress, named = false): string {
  const denied = Math.max(0, p.probed - p.readable)
  if (denied === 0) return 'full read access'
  const head = `${denied} kind${denied === 1 ? '' : 's'} not readable`
  const names = p.denied ?? []
  if (!named || names.length === 0) return head
  return `${head}: ${names.slice(0, 3).join(', ')}${names.length > 3 ? ` and ${names.length - 3} more` : ''}`
}

function syncedCountOf(types: ClusterTypeProgress[]) {
  return types.filter((t) => t.synced).length
}

function andList(names: string[]) {
  return names.length === 1 ? names[0] : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
}

export function prepareView(p: ClusterProgress | undefined, name: string, prevPct = 0, nowMs = Date.now()): PrepareView {
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
  // The bar follows bytes when the API reports them: they are what the tunnel
  // takes time to carry. Objects weigh very differently by kind (a
  // ReplicaSet is many times an Event), so counting them let a cluster's
  // light kinds fill the bar while its heavy one was still arriving.
  let receivedBytes = 0
  let remainingBytes = 0
  for (const t of types) {
    const b = t.bytes ?? 0
    receivedBytes += b
    if (!t.synced && t.total > 0 && t.loaded > 0 && b > 0) {
      const seen = Math.min(t.loaded, t.total)
      remainingBytes += (b / seen) * Math.max(0, t.total - seen)
    }
  }
  let frac = allSynced
    ? 1
    : receivedBytes > 0
      ? receivedBytes / (receivedBytes + remainingBytes)
      : sumW > 0
        ? sumGot / sumW
        : 0
  // Types that have not answered their first page: neither loaded nor sized.
  const unanswered = types.filter((t) => !t.synced && t.total === 0 && t.loaded === 0).length
  const loadingFor = p?.startedAt ? (nowMs - Date.parse(p.startedAt)) / 1000 : 0
  if (!allSynced && unanswered > 0 && loadingFor < EARLY_SECONDS) frac = Math.min(frac, EARLY_CAP)
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
      detail: accessDone && p?.probed ? accessLine(p) : 'checking permissions',
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
  return { pct, what, why, steps, loadedObjects, totalObjects, receivedBytes, remainingBytes }
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
    out.push(`Access checked · ${accessLine(next, true)}`)
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
