import { useEffect, useState } from 'react'

// The KPI rows' enter animation (CountUp + the kb-enter-* classes) plays once
// per page per sign-in: signing in and landing on Home plays it; going to
// another page and back to Home does not. A cluster page counts once PER
// CLUSTER: switching to another cluster shows other figures, so its Overview
// plays again; switching back to one already seen does not. The pages that already played are
// kept in localStorage, so a reload or a second tab of the same session does
// not replay them either, and the list is cleared whenever a session starts
// or ends (AuthContext: login, signup, logout, a boot or a check that finds
// no session) — the next sign-in plays every page again.
//
// The decision is taken once per visit, by Layout, and written on <main> as
// data-kb-enter="play" | "skip": the rows of one visit mount at different
// times as their queries answer, and all of them must get the same answer.
// A visit starts when the PATHNAME changes, not on every navigation: sign-in
// navigates to Home twice in a row (LoginPage), and a second navigation to
// the page you are on must not turn a playing visit into a skipped one —
// flipping the attribute mid-visit cancels the charts and freezes the count.
// A page is marked as played when its first figure starts counting (CountUp),
// not when a card mounts: rows mount their cards before the data arrives
// (with "—"), so a visit that left in that first second never saw the
// animation and must still get it next time.

const KEY = 'kb-kpi-entered'

function read(): string[] {
  try {
    const raw = localStorage.getItem(KEY)
    const v = raw ? JSON.parse(raw) : []
    return Array.isArray(v) ? v : []
  } catch {
    return []
  }
}

function hasEntered(page: string): boolean {
  return read().includes(page)
}

function markEntered(page: string) {
  try {
    const pages = read()
    if (!pages.includes(page)) localStorage.setItem(KEY, JSON.stringify([...pages, page]))
  } catch {
    // Storage unavailable: the animation simply plays again next visit.
  }
}

// The last active cluster Layout saw. The cluster list answers a moment after
// a reload's first render; keying the visit with the cluster that was active
// before the reload (almost always the same one) keeps a reload of a page
// already seen quiet, instead of deciding it on the bare pathname and
// replaying it.
const CLUSTER_KEY = 'kb-kpi-entered-cluster'

export function lastEnterCluster(): string | undefined {
  try {
    return localStorage.getItem(CLUSTER_KEY) ?? undefined
  } catch {
    return undefined
  }
}

export function rememberEnterCluster(cluster: string) {
  try {
    if (localStorage.getItem(CLUSTER_KEY) !== cluster) localStorage.setItem(CLUSTER_KEY, cluster)
  } catch {
    // Storage unavailable: a reload decides on the pathname until the list answers.
  }
}

function unmark(page: string) {
  try {
    const pages = read()
    if (pages.includes(page)) localStorage.setItem(KEY, JSON.stringify(pages.filter((p) => p !== page)))
  } catch {
    // nothing to unmark
  }
}

/** Forget every page that played: the next sign-in plays them all again. */
export function resetEnterAnimations() {
  try {
    localStorage.removeItem(KEY)
  } catch {
    // nothing to clear
  }
}

export type EnterMode = 'play' | 'skip'

/** The key a visit is remembered by: the pathname, plus the cluster on cluster pages. */
export function enterKey(page: string, cluster?: string): string {
  return cluster ? `${page}@${cluster}` : page
}

/**
 * The enter mode for the visit to `page` (the pathname) on `cluster` (the
 * active cluster, cluster pages only). Recomputed only when the page changes
 * or the user switches cluster, so every row that mounts during one visit
 * gets the same answer, however many navigations land on the same page.
 *
 * A cluster that RESOLVES during the visit is not a switch: with nothing
 * remembered (see lastEnterCluster) the cluster list answers a moment after
 * the first render, and treating "unknown → known" as a new visit would flip
 * a playing row to skipped halfway (the charts snap to the end, the count
 * keeps going). The visit keeps its answer and only learns which cluster it
 * was on.
 */
export function useEnterMode(page: string, cluster?: string): EnterMode {
  const decide = () => ({
    page,
    cluster,
    mode: (hasEntered(enterKey(page, cluster)) ? 'skip' : 'play') as EnterMode,
  })
  const [state, setState] = useState(decide)
  // Cards that mounted before the cluster was known marked the bare pathname.
  // Once it is known, that mark moves to the page-on-cluster key, or the next
  // visit to this cluster would not find it and play again. (Called before the
  // early return below: hooks must run in the same order on every render.)
  useEffect(() => {
    if (state.mode !== 'play' || state.cluster === undefined) return
    const bare = enterKey(state.page)
    if (hasEntered(bare)) {
      markEntered(enterKey(state.page, state.cluster))
      unmark(bare)
    }
  }, [state])
  const switched = state.cluster !== undefined && cluster !== undefined && state.cluster !== cluster
  if (state.page !== page || switched) {
    const next = decide()
    setState(next)
    return next.mode
  }
  if (state.cluster === undefined && cluster !== undefined) setState({ ...state, cluster })
  return state.mode
}

/**
 * Marks the page of the enclosing <main data-kb-enter> as played. CountUp
 * calls it when a figure first shows a real number — the moment the visit
 * has actually seen its rows. `el` is any element inside the row.
 */
export function markEnteredAt(el: HTMLElement) {
  const host = el.closest<HTMLElement>('[data-kb-enter-page]')
  const page = host?.dataset.kbEnterPage
  if (page && host?.dataset.kbEnter === 'play') markEntered(page)
}

/** True when the enclosing page decided not to replay the enter animation. */
export function enterSkipped(el: HTMLElement): boolean {
  return el.closest<HTMLElement>('[data-kb-enter]')?.dataset.kbEnter === 'skip'
}
