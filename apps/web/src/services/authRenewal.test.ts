import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { fetchWithAuth, freshAccessToken, setAccessToken, clearAccessToken, getAccessToken } from './api'

// Every 15 minutes the access token expired and the next requests answered
// 401 "invalid or expired token" before a refresh and a retry: a burst of red
// rows in DevTools in every open tab, and an error in Kobi's panel, whose chat
// builds its own request. These tests pin that a token is renewed a minute
// before it expires, and that the 401 → refresh → retry path is still there
// for what renewal cannot foresee.

const DATA = 'https://api.test/api/v1/clusters'

let now = Date.UTC(2026, 10, 12, 12, 0, 0)

function b64url(obj: unknown): string {
  return btoa(JSON.stringify(obj)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

let serial = 0
// jwt builds a token issued `skewSec` away from this test's clock (a server
// clock behind or ahead) and valid for `lifetimeSec`.
function jwt(lifetimeSec: number, skewSec = 0): string {
  const iat = Math.floor(now / 1000) + skewSec
  return `${b64url({ alg: 'HS256' })}.${b64url({ iat, exp: iat + lifetimeSec, n: ++serial })}.sig`
}

interface Call {
  url: string
  auth: string | null
}

// server answers like the API: the refresh mints a token when a session
// exists; a data route accepts only tokens it knows.
function server(opts: { session: boolean }) {
  const valid = new Set<string>()
  const calls: Call[] = []
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    const headers = (init?.headers ?? {}) as Record<string, string>
    const auth = headers.Authorization ?? null
    calls.push({ url, auth })
    if (url.endsWith('/auth/refresh')) {
      if (!opts.session) return new Response('{"error":"invalid refresh token"}', { status: 401 })
      const token = jwt(900)
      valid.add(token)
      return new Response(JSON.stringify({ accessToken: token }), { status: 200 })
    }
    if (!auth) return new Response('{"error":"authentication required"}', { status: 401 })
    if (!valid.has(auth.replace('Bearer ', ''))) {
      return new Response('{"error":"invalid or expired token"}', { status: 401 })
    }
    return new Response('[]', { status: 200 })
  })
  vi.stubGlobal('fetch', fetchMock)
  return {
    calls,
    accept(token: string) {
      valid.add(token)
      return token
    },
    refreshes: () => calls.filter((c) => c.url.endsWith('/auth/refresh')).length,
    dataCalls: () => calls.filter((c) => c.url === DATA),
  }
}

beforeEach(() => {
  now = Date.UTC(2026, 10, 12, 12, 0, 0)
  vi.spyOn(Date, 'now').mockImplementation(() => now)
  clearAccessToken()
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('fetchWithAuth', () => {
  it('with no token refreshes nothing, before or after (the login page)', async () => {
    const s = server({ session: false })

    const res = await fetchWithAuth(DATA)

    expect(res.status).toBe(401)
    expect(s.calls.map((c) => c.url)).toEqual([DATA])
  })

  it('sends a fresh token as it is', async () => {
    const s = server({ session: true })
    setAccessToken(s.accept(jwt(900)))
    now += 60_000

    await fetchWithAuth(DATA)

    expect(s.calls.map((c) => c.url)).toEqual([DATA])
  })

  it('renews a token within a minute of expiring before the request uses it', async () => {
    const s = server({ session: true })
    const old = s.accept(jwt(900))
    setAccessToken(old)
    now += 845_000

    const res = await fetchWithAuth(DATA)

    expect(res.status).toBe(200)
    expect(s.calls[0].url).toMatch(/\/auth\/refresh$/)
    expect(s.dataCalls()).toHaveLength(1)
    expect(s.dataCalls()[0].auth).not.toBe(`Bearer ${old}`)
  })

  it('a burst of requests at the deadline shares one refresh', async () => {
    const s = server({ session: true })
    setAccessToken(s.accept(jwt(900)))
    now += 850_000

    const all = await Promise.all(Array.from({ length: 9 }, () => fetchWithAuth(DATA)))

    expect(all.every((r) => r.status === 200)).toBe(true)
    expect(s.refreshes()).toBe(1)
  })

  it('a laptop clock an hour ahead of the server does not renew on every request', async () => {
    const s = server({ session: true })
    // Issued by a server whose clock is an hour behind ours: by exp alone the
    // token looks long expired. Its lifetime is what counts.
    setAccessToken(s.accept(jwt(900, -3600)))
    now += 10_000

    await fetchWithAuth(DATA)

    expect(s.refreshes()).toBe(0)
  })

  it('a short-lived token is renewed at half its life, not on every request', async () => {
    const s = server({ session: true })
    setAccessToken(s.accept(jwt(60)))
    now += 20_000
    await fetchWithAuth(DATA)
    expect(s.refreshes()).toBe(0)

    now += 15_000
    await fetchWithAuth(DATA)
    expect(s.refreshes()).toBe(1)
  })

  it('a renewal that finds no session is not tried again on the 401', async () => {
    const s = server({ session: false })
    setAccessToken(jwt(900)) // already refused by the server
    now += 845_000

    const res = await fetchWithAuth(DATA)

    expect(res.status).toBe(401)
    expect(s.refreshes()).toBe(1)
  })

  it('a 401 on a token it did not expect still refreshes and retries', async () => {
    const s = server({ session: true })
    setAccessToken(jwt(900)) // not accepted by the server: revoked, or a restarted signer

    const res = await fetchWithAuth(DATA)

    expect(res.status).toBe(200)
    expect(s.calls.map((c) => c.url)).toEqual([DATA, expect.stringMatching(/\/auth\/refresh$/), DATA])
  })

  it('a token whose claims cannot be read is never renewed ahead', async () => {
    const s = server({ session: true })
    setAccessToken('opaque-token')
    now += 24 * 3600_000

    await fetchWithAuth(DATA)

    expect(s.calls[0].url).toBe(DATA)
  })
})

describe('freshAccessToken', () => {
  it('hands Kobi a renewed token when the one in memory is about to expire', async () => {
    const s = server({ session: true })
    const old = s.accept(jwt(900))
    setAccessToken(old)
    now += 850_000

    const token = await freshAccessToken()

    expect(token).not.toBe(old)
    expect(token).toBe(getAccessToken())
    expect(s.refreshes()).toBe(1)
  })
})
