import { CopilotRefusal } from './chat'

// How many times the panel asks again on its own when Kobi is busy. One: the
// server already queued the question ~15 s before refusing, so a second
// refusal means the plan's limit is genuinely full and the person decides.
export const BUSY_AUTO_RETRIES = 1

// The wait state retryWhileBusy reports: the epoch ms it asks again while it
// counts down, ASKED_AGAIN once it has (the server may queue the question a
// few seconds more before the first event), null once the answer starts or
// the turn ends.
export const ASKED_AGAIN = 0

export interface RetryWhileBusyOptions {
  retries: number
  signal: AbortSignal
  onWait: (until: number | null) => void
  sleep?: (ms: number, signal: AbortSignal) => Promise<void>
  now?: () => number
}

/**
 * retryWhileBusy yields the events of open() and, when it is refused as
 * 'busy' before its first event, waits the refusal's Retry-After and opens
 * it again, up to `retries` times. The wait ends early, with an AbortError,
 * when `signal` aborts (the panel's Stop). Anything else is rethrown as is,
 * and so is the last refusal.
 */
export async function* retryWhileBusy<T>(
  open: () => AsyncGenerator<T>,
  { retries, signal, onWait, sleep = abortableSleep, now = Date.now }: RetryWhileBusyOptions,
): AsyncGenerator<T> {
  let waiting = false
  const done = () => {
    if (waiting) onWait(null)
    waiting = false
  }
  for (let attempt = 0; ; attempt++) {
    let started = false
    try {
      for await (const event of open()) {
        if (!started) done()
        started = true
        yield event
      }
      done()
      return
    } catch (err) {
      const busy = err instanceof CopilotRefusal && err.kind === 'busy'
      if (!busy || started || attempt >= retries || signal.aborted) {
        done()
        throw err
      }
      const waitMs = (err.retryAfterSec ?? 10) * 1000
      waiting = true
      onWait(now() + waitMs)
      try {
        await sleep(waitMs, signal)
      } catch (stopped) {
        done()
        throw stopped
      }
      onWait(ASKED_AGAIN)
    }
  }
}

export function abortableSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const aborted = () => new DOMException('The wait was stopped', 'AbortError')
    if (signal.aborted) {
      reject(aborted())
      return
    }
    const onAbort = () => {
      window.clearTimeout(timer)
      reject(aborted())
    }
    const timer = window.setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    signal.addEventListener('abort', onAbort, { once: true })
  })
}
