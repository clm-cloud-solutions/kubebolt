// freshnessOf — how current a polled answer is, in the words the chrome shows.
//
//   live         the last poll answered and the next one is on schedule
//   delayed      a whole cycle went by without a new answer
//   offline      the last poll failed (the cluster or the API did not answer)
//   connecting   nothing has answered yet
//
// A poll fires at `intervalMs` and takes a moment to come back, so the age of
// the data runs up to one interval and a bit on every healthy cycle. Colouring
// that as a warning painted the indicator amber half of the time with nothing
// wrong; only a MISSED cycle (more than twice the interval) is a delay.
export type Freshness = 'live' | 'delayed' | 'offline' | 'connecting'

export function freshnessOf(opts: { updatedAt: number; intervalMs: number; now: number; failed?: boolean }): Freshness {
  if (opts.failed) return 'offline'
  if (!opts.updatedAt) return 'connecting'
  return opts.now - opts.updatedAt > 2 * opts.intervalMs ? 'delayed' : 'live'
}
