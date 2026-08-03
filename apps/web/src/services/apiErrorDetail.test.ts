import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from './api'

// A rejected settings save answers with three pieces:
//
//   {"error": "validation failed", "field": "...", "message": "<what is wrong>"}
//
// `error` is the CATEGORY and `message` is the reason. The helper read
// `json.error || json.message`, so it always surfaced the category and dropped
// the reason: the operator saw a flat "validation failed" with no indication of
// which field or why. settings_handlers.go returns that shape from three
// places, including the Slack webhook validator whose `message` carries the
// real parse error.
//
// Exercised through the public call rather than by exporting the helper, so
// this pins the contract a caller actually sees.

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: false,
    status,
    statusText: 'Bad Request',
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response
}

describe('API error detail', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn())
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('surfaces the reason, not the category', async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse(400, {
        error: 'validation failed',
        field: 'slack.webhookURL',
        message: 'must be an absolute https URL',
      }),
    )
    await expect(api.renameCluster('ctx', 'x')).rejects.toThrow('must be an absolute https URL')
  })

  it('keeps the field reachable for callers that highlight it', async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse(400, {
        error: 'validation failed',
        field: 'slack.webhookURL',
        message: 'must be an absolute https URL',
      }),
    )
    const err = await api.renameCluster('ctx', 'x').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).payload?.field).toBe('slack.webhookURL')
  })

  it('still reports a response that carries only `error`', async () => {
    // The common shape from respondError. Preferring `message` must not lose it.
    vi.mocked(fetch).mockResolvedValue(jsonResponse(409, { error: 'cluster already exists' }))
    await expect(api.renameCluster('ctx', 'x')).rejects.toThrow('cluster already exists')
  })

  it('falls back to the status text when the body names neither', async () => {
    vi.mocked(fetch).mockResolvedValue(jsonResponse(400, { unrelated: true }))
    await expect(api.renameCluster('ctx', 'x')).rejects.toThrow('Bad Request')
  })
})
