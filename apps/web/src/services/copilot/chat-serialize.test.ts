import { describe, it, expect, vi, beforeEach } from 'vitest'
import { compactCopilotSession } from './chat'
import type { CopilotMessage } from './types'

// Prefer a minimal network stub over importing the full api module so the
// assertion is focused on the request body shape.
const originalFetch = globalThis.fetch

function makeMsg(overrides: Partial<CopilotMessage>): CopilotMessage {
  return {
    id: 'm',
    role: 'user',
    content: 'hi',
    timestamp: new Date(),
    ...overrides,
  }
}

describe('compactCopilotSession — serialization', () => {
  beforeEach(() => {
    globalThis.fetch = originalFetch
  })

  it('filters compact-notice messages before sending', async () => {
    let capturedBody: any = null
    globalThis.fetch = vi.fn(async (_url, init) => {
      capturedBody = JSON.parse(init!.body as string)
      return new Response(
        JSON.stringify({
          summary: '',
          messages: [],
          tokensBefore: 0,
          tokensAfter: 0,
          turnsFolded: 0,
          model: 'x',
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      )
    }) as any

    const messages: CopilotMessage[] = [
      makeMsg({ id: 'u1', role: 'user', content: 'hello' }),
      makeMsg({
        id: 'n1',
        role: 'system',
        content: '',
        kind: 'compact-notice',
        compactMeta: {
          turnsFolded: 2,
          toolResultsStubbed: 0,
          tokensBefore: 100,
          tokensAfter: 20,
          auto: true,
        },
      }),
      makeMsg({ id: 'a1', role: 'assistant', content: 'hi back' }),
    ]

    await compactCopilotSession(messages, true)

    expect(capturedBody).not.toBeNull()
    expect(capturedBody.resetAll).toBe(true)
    expect(capturedBody.messages).toHaveLength(2)
    expect(capturedBody.messages.find((m: any) => m.id === 'n1')).toBeUndefined()
    // Standard message fields round-trip
    expect(capturedBody.messages[0].role).toBe('user')
    expect(capturedBody.messages[0].content).toBe('hello')
  })

  it('throws on non-ok HTTP response', async () => {
    globalThis.fetch = vi.fn(async () => new Response('nope', { status: 503 })) as any
    await expect(compactCopilotSession([makeMsg({})], false)).rejects.toThrow(/503/)
  })
})

describe('serializeMessages — trigger', () => {
  it('carries the trigger of a message the UI wrote, and none for a typed one', async () => {
    const { serializeMessages } = await import('./chat')
    const out = serializeMessages([
      makeMsg({ content: 'Puedes escalar a 0 a oomy-app?' }),
      makeMsg({ content: 'I ran the action…', trigger: 'action_stalled' }),
    ])
    expect(out[0].trigger).toBeUndefined()
    expect(out[1].trigger).toBe('action_stalled')
  })
})

describe('serializeMessages — view', () => {
  it('sends back the page the server stamped, so the next turn reads the history the same', async () => {
    const { serializeMessages } = await import('./chat')
    const out = serializeMessages([
      makeMsg({ content: 'La tienda está dando errores', view: '/deployments/shop-frontend/shop-web' }),
      makeMsg({ content: 'Nuevo mensaje' }),
    ])
    expect(out[0].view).toBe('/deployments/shop-frontend/shop-web')
    expect(out[1].view).toBeUndefined()
  })
})
