import type { Insight } from '@/types/kubernetes'
import { insightResourcePath } from './insightResource'

// insightCases — the open insights as cases an operator can act on: one per
// (rule, object), worst first. Among equals, what started in the last day
// comes first, oldest first, because the oldest of a fresh incident is usually
// its cause; what has been open for days (a PDB matching nothing since August)
// comes after, newest first, so a standing warning never hides today's. The
// Overview names the first case on the card whose question it answers (Nodes,
// Pods) and lists the rest on the Insights card, each one click from its object.

export type Severity = Insight['severity']

export interface InsightCase {
  rule: string
  severity: Severity
  /** The object, "Kind/namespace/name" or "Kind/name"; for a workload case, its oldest pod. */
  resource: string
  /** Who, in the words an operator uses: a pod's workload, else the object's name. */
  name: string
  /** When the oldest occurrence folded into this case was first seen. */
  since: string
  /** Pods folded into a workload case; 0 for anything that is not a pod. */
  pods: number
  /** The object's detail page, or null when it has none. */
  to: string | null
}

export type CaseRow =
  | { kind: 'case'; case: InsightCase }
  | { kind: 'rule'; rule: string; severity: Severity; count: number; since: string }

const SEV_RANK: Record<Severity, number> = { critical: 0, warning: 1, info: 2 }

// The rules about a pod not running as it should: what the Pods card counts.
export const POD_RULES = new Set([
  'crash-loop',
  'oom-killed',
  'image-pull-backoff',
  'missing-config-dependency',
  'readiness-probe-failing',
  'liveness-probe-failing',
  'frequent-restarts',
  'evicted-pods',
])
export const NODE_RULES = new Set(['node-not-ready'])

// A rule with this many cases becomes one row in a list, so one noisy rule
// can't push every other problem off the card.
export const GROUP_AT = 3

// Kubernetes' random suffixes use an alphabet without vowels, so a name
// segment like "exporter" is never mistaken for one.
const H = '[bcdfghjklmnpqrstvwxz2456789]'
const REPLICASET_POD = new RegExp(`^(.+)-${H}{5,10}-${H}{5}$`)
const SUFFIXED_POD = new RegExp(`^(.+)-${H}{5}$`)

// podWorkload — the workload a pod belongs to, from its name: a Deployment's
// "<name>-<hash>-<suffix>", a DaemonSet's or Job's "<name>-<suffix>". A
// StatefulSet's "<name>-0" keeps its ordinal: the pod is the unit there. A
// label only; links still open the exact pod.
export function podWorkload(pod: string): string {
  return pod.match(REPLICASET_POD)?.[1] ?? pod.match(SUFFIXED_POD)?.[1] ?? pod
}

const t = (iso: string) => Date.parse(iso) || 0

// What started within this window is part of what is happening now.
export const RECENT_MS = 24 * 3600_000

export function openCases(items: Insight[] | undefined, rules?: Set<string>, now = Date.now()): InsightCase[] {
  const byKey = new Map<string, InsightCase>()
  for (const i of items ?? []) {
    if (i.resolved) continue
    const rule = i.ruleId || i.title
    if (rules && !rules.has(rule)) continue
    const parts = i.resource.split('/')
    const isPod = parts[0] === 'Pod'
    const object = parts[parts.length - 1] || i.resource
    const name = isPod ? podWorkload(object) : object
    // Pods of one workload with the same problem are one case.
    const key = isPod ? `${rule}\0${parts[1] ?? ''}\0${name}` : `${rule}\0${i.resource}`
    const cur = byKey.get(key)
    if (!cur) {
      byKey.set(key, {
        rule,
        severity: i.severity,
        resource: i.resource,
        name,
        since: i.firstSeen,
        pods: isPod ? 1 : 0,
        to: insightResourcePath(i.resource),
      })
      continue
    }
    if (isPod) cur.pods++
    if (SEV_RANK[i.severity] < SEV_RANK[cur.severity]) cur.severity = i.severity
    if (t(i.firstSeen) < t(cur.since)) {
      cur.since = i.firstSeen
      cur.resource = i.resource
      cur.to = insightResourcePath(i.resource)
    }
  }
  const recent = (c: InsightCase) => now - t(c.since) <= RECENT_MS
  return [...byKey.values()].sort((a, b) => {
    if (a.severity !== b.severity) return SEV_RANK[a.severity] - SEV_RANK[b.severity]
    const ra = recent(a)
    if (ra !== recent(b)) return ra ? -1 : 1
    return ra ? t(a.since) - t(b.since) : t(b.since) - t(a.since)
  })
}

// caseRows — at most `max` rows for a list, folding every rule with GROUP_AT
// or more cases into one row at the place of its worst case. `more` is how
// many cases the rows leave out.
export function caseRows(cases: InsightCase[], max: number): { rows: CaseRow[]; more: number } {
  const perRule = new Map<string, number>()
  for (const c of cases) perRule.set(c.rule, (perRule.get(c.rule) ?? 0) + 1)
  const rows: CaseRow[] = []
  const folded = new Set<string>()
  for (const c of cases) {
    const count = perRule.get(c.rule) ?? 1
    if (count < GROUP_AT) rows.push({ kind: 'case', case: c })
    else if (!folded.has(c.rule)) {
      folded.add(c.rule)
      // Cases arrive worst and oldest first: the first one speaks for its rule.
      rows.push({ kind: 'rule', rule: c.rule, severity: c.severity, count, since: c.since })
    }
  }
  const shown = rows.slice(0, max)
  const covered = shown.reduce((n, r) => n + (r.kind === 'rule' ? r.count : 1), 0)
  return { rows: shown, more: cases.length - covered }
}
