import { describe, it, expect, vi } from 'vitest'
import { abortableSleep, retryWhileBusy } from './busyRetry'
import { CopilotRefusal, sendCopilotChat } from './chat'

const busy = (retryAfterSec = 7) => new CopilotRefusal('busy', 'Kobi is already answering as many questions as your plan allows at once.', retryAfterSec)

async function* refused(err: Error): AsyncGenerator<string> {
  throw err
}
async function* events(...xs: string[]): AsyncGenerator<string> {
  for (const x of xs) yield x
}

async function drain(gen: AsyncGenerator<string>): Promise<string[]> {
  const out: string[] = []
  for await (const x of gen) out.push(x)
  return out
}

function harness(attempts: Array<() => AsyncGenerator<string>>) {
  let n = 0
  const open = vi.fn(() => attempts[n++]())
  const waits: Array<number | null> = []
  const slept: number[] = []
  const opts = {
    retries: 1,
    signal: new AbortController().signal,
    onWait: (u: number | null) => waits.push(u),
    sleep: async (ms: number) => {
      slept.push(ms)
    },
    now: () => 1_000,
  }
  return { open, waits, slept, opts }
}

describe('retryWhileBusy', () => {
  it('waits what the refusal says and asks again', async () => {
    const h = harness([() => refused(busy(7)), () => events('meta', 'done')])
    expect(await drain(retryWhileBusy(h.open, h.opts))).toEqual(['meta', 'done'])
    expect(h.open).toHaveBeenCalledTimes(2)
    expect(h.slept).toEqual([7_000])
    // Counting down, then asked again (still waiting in line), then started.
    expect(h.waits).toEqual([8_000, 0, null])
  })

  it('stops after its retries and hands the last refusal on', async () => {
    const h = harness([() => refused(busy()), () => refused(busy())])
    await expect(drain(retryWhileBusy(h.open, h.opts))).rejects.toMatchObject({ kind: 'busy' })
    expect(h.open).toHaveBeenCalledTimes(2)
    expect(h.waits).toEqual([8_000, 0, null])
  })

  it('never asks again once the answer has started', async () => {
    async function* startedThenRefused(): AsyncGenerator<string> {
      yield 'meta'
      throw busy()
    }
    const h = harness([startedThenRefused, () => events('done')])
    await expect(drain(retryWhileBusy(h.open, h.opts))).rejects.toBeInstanceOf(CopilotRefusal)
    expect(h.open).toHaveBeenCalledTimes(1)
  })

  it('passes any other failure straight on, credits included', async () => {
    for (const err of [new Error('network'), new CopilotRefusal('credits', 'spent')]) {
      const h = harness([() => refused(err), () => events('done')])
      await expect(drain(retryWhileBusy(h.open, h.opts))).rejects.toBe(err)
      expect(h.open).toHaveBeenCalledTimes(1)
    }
  })
})

describe('abortableSleep', () => {
  it('ends the wait with an AbortError when Stop is pressed', async () => {
    const ac = new AbortController()
    const wait = abortableSleep(60_000, ac.signal)
    ac.abort()
    await expect(wait).rejects.toMatchObject({ name: 'AbortError' })
  })
})

describe('sendCopilotChat refusals', () => {
  function respond(status: number, body: unknown, headers: Record<string, string> = {}) {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify(body), { status, headers })),
    )
  }

  it('reads a full turn queue as busy, with its Retry-After', async () => {
    respond(429, { error: 'Kobi is already answering as many questions as your plan allows at once.', limitType: 'concurrent_turns' }, { 'Retry-After': '10' })
    await expect(sendCopilotChat([], '/').next()).rejects.toMatchObject({ kind: 'busy', retryAfterSec: 10 })
    vi.unstubAllGlobals()
  })

  it('leaves any other refusal a plain error', async () => {
    respond(503, { error: 'no cluster selected — choose a cluster to continue' })
    const err = await sendCopilotChat([], '/').next().catch((e: unknown) => e)
    expect(err).not.toBeInstanceOf(CopilotRefusal)
    expect((err as Error).message).toBe('no cluster selected — choose a cluster to continue')
    vi.unstubAllGlobals()
  })
})
